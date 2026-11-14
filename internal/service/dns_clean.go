// dns_clean.go 过期/孤儿 DNS 记录清理 (对齐 PHP Job::CleanExpiredDnsRecords)。
//
// 三条清理路径: A 心跳过期 (0<hb<threshold) / B 节点已删孤儿 / C node_id=0 无法归属
// (反推→保护→形似三重判定)。删除顺序: CF 成功 (或 404 或纯本地) 才删本地行;
// CF 非 404 失败保留本地 (保留 cf_record_id 以便重试)。清空后节点名追加 "DNS❌"。
package service

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/notify"
	"spanel-go/internal/pkg/timex"
)

// 保护性子域 (系统记录, 宁留不误删)
var dnsProtectedSubdomains = map[string]bool{
	"www": true, "rss": true, "ech": true, "mail": true, "smtp": true,
	"imap": true, "mx": true, "ns": true, "ns1": true, "ns2": true,
	"sub": true, "sub-web": true, "panel": true, "api": true, "admin": true,
	"ftp": true, "cdn": true, "blog": true, "autodiscover": true,
	"autoconfig": true, "_dmarc": true, "_domainkey": true,
}

var (
	dnsLooksLikeNodeRe1 = regexp.MustCompile(`[a-zA-Z]\d{2,}$`) // 字母+≥2位数字结尾 (s6368/ipv6n64)
	dnsLooksLikeNodeRe2 = regexp.MustCompile(`^\d{4,}$`)        // 纯数字≥4位 (4322651)
	dnsResolveSubRe1    = regexp.MustCompile(`^s(\d+)$`)
	dnsResolveSubRe2    = regexp.MustCompile(`([a-zA-Z]+)(\d{3,})$`) // 注意 ≥3 位, 与 looksLike 不同
)

func dnsIsProtectedSubdomain(sub string) bool {
	sub = strings.TrimSpace(sub)
	if sub == "" || sub == "@" {
		return true
	}
	if strings.HasPrefix(sub, "*") {
		return true
	}
	return dnsProtectedSubdomains[strings.ToLower(sub)]
}

func dnsLooksLikeNodeRecord(sub string) bool {
	sub = strings.TrimSpace(sub)
	if sub == "" || sub == "@" {
		return false
	}
	return dnsLooksLikeNodeRe1.MatchString(sub) || dnsLooksLikeNodeRe2.MatchString(sub)
}

// dnsResolveNodeIdBySubdomain 反推 node_id (对齐 DnsReconcileService::resolveNodeIdBySubdomain):
// s{id} 精确匹配 / 字母前缀+≥3位数字结尾取数字段; 纯数字子域不反推。
func dnsResolveNodeIdBySubdomain(sub string, nodeExist map[int32]bool) int32 {
	sub = strings.TrimSpace(sub)
	if sub == "" || sub == "@" {
		return 0
	}
	if m := dnsResolveSubRe1.FindStringSubmatch(sub); m != nil {
		var id int32
		fmt.Sscanf(m[1], "%d", &id)
		if id > 0 && nodeExist[id] {
			return id
		}
		return 0
	}
	if m := dnsResolveSubRe2.FindStringSubmatch(sub); m != nil {
		var id int32
		fmt.Sscanf(m[2], "%d", &id)
		if id > 0 && nodeExist[id] {
			return id
		}
		return 0
	}
	return 0
}

// CleanExpiredDnsRecords 清理过期/孤儿 DNS (L2 通道, dailyjob + 手动 cleanExpiredDns)。
func CleanExpiredDnsRecords(ctx context.Context, dryRun bool, logf func(string, ...any)) map[string]int64 {
	stats := map[string]int64{
		"candidate_stale": 0, "candidate_orphan_gone": 0, "candidate_orphan_unres": 0,
		"skipped_alive": 0, "skipped_heartbeat_ok": 0, "skipped_protected": 0,
		"skipped_resolvable": 0, "skipped_unknown": 0, "skipped_node_gone": 0,
		"skipped_heartbeat_restored": 0, "deleted_cf": 0, "deleted_db": 0,
		"failed_cf": 0, "skipped_no_zone": 0, "skipped_mark_residual": 0, "marked_nodes": 0,
	}
	var days int = 32
	markName := true
	if cfg := config.GlobalConfig; cfg != nil {
		if cfg.Jobs.Params.NodeDnsCleanupInactiveDays != 0 {
			days = cfg.Jobs.Params.NodeDnsCleanupInactiveDays
		}
		markName = config.BoolOrDefault(cfg.Jobs.Params.NodeDnsCleanupMarkNodeName, true)
	}
	if days <= 0 {
		if logf != nil {
			logf("🧹 DNS清理: Disabled by config: node_dns_cleanup_inactive_days=%d", days)
		}
		return stats
	}
	threshold := timex.Unix() - int64(days)*86400

	// 节点快照: id → (心跳, 是否存在)
	heartbeat := map[int32]int64{}
	var nodes []model.Node
	_ = database.DB.WithContext(ctx).Select("id", "node_heartbeat").Find(&nodes).Error
	for i := range nodes {
		heartbeat[nodes[i].ID] = nodes[i].NodeHeartbeat
	}

	type candidate struct {
		rec    model.DnsRecord
		reason string // stale_heartbeat | orphan_node_gone | orphan_unresolved
	}
	var candidates []candidate

	var recs []model.DnsRecord
	_ = database.DB.WithContext(ctx).Find(&recs).Error
	for i := range recs {
		rec := recs[i]
		switch {
		case rec.NodeID > 0:
			hb, exist := heartbeat[rec.NodeID]
			if !exist {
				candidates = append(candidates, candidate{rec, "orphan_node_gone"})
				stats["candidate_orphan_gone"]++
				continue
			}
			switch {
			case hb <= 0:
				stats["skipped_alive"]++ // 从未上线, 保留
			case hb >= threshold:
				stats["skipped_heartbeat_ok"]++
			default:
				candidates = append(candidates, candidate{rec, "stale_heartbeat"})
				stats["candidate_stale"]++
			}
		default: // node_id = 0
			if nid := dnsResolveNodeIdBySubdomain(rec.Subdomain, boolMap(heartbeat)); nid > 0 {
				stats["skipped_resolvable"]++
				continue
			}
			if dnsIsProtectedSubdomain(rec.Subdomain) {
				stats["skipped_protected"]++
				continue
			}
			if !dnsLooksLikeNodeRecord(rec.Subdomain) {
				stats["skipped_unknown"]++
				continue
			}
			candidates = append(candidates, candidate{rec, "orphan_unresolved"})
			stats["candidate_orphan_unres"]++
		}
	}

	deletedNodeIDs := map[int32]bool{}
	for _, c := range candidates {
		rec := c.rec
		// A 路径二次确认 (防候选筛选期间节点恢复)
		if c.reason == "stale_heartbeat" {
			hb, exist := heartbeat[rec.NodeID]
			if !exist {
				stats["skipped_node_gone"]++
				continue
			}
			if hb <= 0 {
				stats["skipped_node_gone"]++
				continue
			}
			if hb >= threshold {
				stats["skipped_heartbeat_restored"]++
				continue
			}
		}

		fqdn := rec.Subdomain + "." + rec.RootDomain
		if dryRun {
			if logf != nil {
				logf("Would delete [%s]: %s type=%s (cf=%s | local-only)",
					c.reason, fqdn, rec.RecordType, ternary(rec.CFRecordID != "", rec.CFRecordID, "-"))
			}
			continue
		}

		// 删 CF (仅当有 cf_record_id); 404 视为已删
		cfOK := true
		if rec.CFRecordID != "" {
			zoneID, err := ZoneIDForDomain(rec.RootDomain)
			if err != nil || zoneID == "" {
				cfOK = false
				stats["skipped_no_zone"]++
			} else {
				if err := CFDeleteRecord(zoneID, rec.CFRecordID, TokenForDomain(rec.RootDomain)); err != nil {
					if IsCFNotFound(err) {
						stats["deleted_cf"]++
					} else {
						cfOK = false
						stats["failed_cf"]++
						if logf != nil {
							logf("CF delete failed %s: %v", fqdn, err)
						}
					}
				} else {
					stats["deleted_cf"]++
				}
			}
		}
		if !cfOK {
			continue // 本地保留, 以便重试
		}
		if err := database.DB.WithContext(ctx).Delete(&model.DnsRecord{}, rec.ID).Error; err == nil {
			stats["deleted_db"]++
			if rec.NodeID > 0 {
				deletedNodeIDs[rec.NodeID] = true
			}
		}
	}

	// "DNS❌" 标记 (仅非 dry-run 且开启)
	if !dryRun && markName && len(deletedNodeIDs) > 0 {
		for nid := range deletedNodeIDs {
			var n model.Node
			if err := database.DB.WithContext(ctx).First(&n, nid).Error; err != nil {
				continue // 节点行已删
			}
			var residual int64
			database.DB.WithContext(ctx).Model(&model.DnsRecord{}).
				Where("node_id = ?", nid).Count(&residual)
			if residual > 0 {
				stats["skipped_mark_residual"]++
				continue
			}
			if strings.Contains(n.Name, "DNS❌") {
				continue
			}
			newName := n.Name + " DNS❌"
			if strings.TrimSpace(n.Name) == "" {
				newName = "DNS❌"
			}
			_ = database.DB.WithContext(ctx).Model(&model.Node{}).
				Where("id = ?", nid).Update("name", newName).Error
			stats["marked_nodes"]++
		}
	}

	total := stats["candidate_stale"] + stats["candidate_orphan_gone"] + stats["candidate_orphan_unres"]
	msg := fmt.Sprintf("🧹 DNS清理: 候选 %d(过期 %d/孤儿 %d+%d), CF删 %d, 本地删 %d",
		total, stats["candidate_stale"], stats["candidate_orphan_gone"], stats["candidate_orphan_unres"],
		stats["deleted_cf"], stats["deleted_db"])
	if stats["failed_cf"] > 0 {
		msg += fmt.Sprintf(", CF失败 %d", stats["failed_cf"])
	}
	if stats["skipped_no_zone"] > 0 {
		msg += fmt.Sprintf(", 无zone %d", stats["skipped_no_zone"])
	}
	if dryRun {
		msg += " [DRY-RUN]"
	}
	if logf != nil {
		logf("%s", msg)
	}
	if !dryRun && (stats["deleted_db"] > 0 || stats["failed_cf"] > 0) {
		notify.Info("DNS清理", msg)
	}
	return stats
}

func boolMap(m map[int32]int64) map[int32]bool {
	out := make(map[int32]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}
