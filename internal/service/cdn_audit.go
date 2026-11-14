// cdn_audit.go CDN zone 卫生审计 (对齐 PHP Job::AuditCdnZones, 2026-11-05 防封号方案 P0-4)。
//
// 扫描 CDN 域名 CF zone 内全部 DNS 记录, 告警非面板管理的橙云 (proxied) 记录与
// 泛解析 (*) 记录 (一封封一片的高危元凶)。只告警不自动删。
package service

import (
	"context"
	"fmt"
	"strings"

	"spanel-go/internal/pkg/notify"
)

// AuditCdnZones 审计 CDN zone (dailyjob + 手动 cdnzoneaudit)。
func AuditCdnZones(ctx context.Context, dryRun bool, logf func(string, ...any)) map[string]int64 {
	stats := map[string]int64{"zones": 0, "records": 0, "orange": 0, "wildcard": 0, "errors": 0}
	cdnDomains := DomainPoolCDN()
	stats["zones"] = int64(len(cdnDomains))
	if len(cdnDomains) == 0 {
		if logf != nil {
			logf("[CdnZoneAudit] CDN 域名池为空, 跳过")
		}
		return stats
	}

	var findings []string
	for _, entry := range cdnDomains {
		zoneID, err := ZoneIDForDomain(entry.Domain)
		if err != nil || zoneID == "" {
			stats["errors"]++
			if logf != nil {
				logf("[CdnZoneAudit] zone %s 查询失败: %v", entry.Domain, err)
			}
			continue
		}
		recs, err := CFListRecords(zoneID, TokenForDomain(entry.Domain))
		if err != nil {
			stats["errors"]++
			if logf != nil {
				logf("[CdnZoneAudit] zone %s listRecords 失败: %v", entry.Domain, err)
			}
			continue
		}
		for _, rec := range recs {
			stats["records"]++
			name := rec.Name
			switch {
			case strings.Contains(name, "*"): // 泛解析优先
				stats["wildcard"]++
				findings = append(findings, fmt.Sprintf("%s: 泛解析记录 %s (橙云=%s) — 一封封一片, 高危, 建议立即删除",
					entry.Domain, name, ternary(rec.Proxied, "是", "否")))
			case rec.Proxied:
				stats["orange"]++
				findings = append(findings, fmt.Sprintf("%s: 橙云记录 %s → %s", entry.Domain, name, rec.Content))
			}
		}
	}

	for _, f := range findings {
		if logf != nil {
			logf("[CdnZoneAudit] ⚠ %s", f)
		}
	}
	if !dryRun && len(findings) > 0 {
		shown := findings
		if len(shown) > 10 {
			shown = shown[:10]
		}
		notify.Send(fmt.Sprintf("⚠️CDN zone 审计发现 %d 条风险记录 (橙云/泛解析):\n%s",
			len(findings), strings.Join(shown, "\n")))
	}
	return stats
}
