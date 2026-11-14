// list_stale_dns.go 陈旧节点 DNS 记录匹配报告 (对齐 PHP XCat::listStaleDNS, 只读)。
//
// 60 天无心跳节点的域名 vs CF zone A/AAAA 记录全等匹配, 输出 JSON 报告。
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/timex"
)

// ListStaleDNS 生成陈旧 DNS 匹配报告 (JSON)。
func ListStaleDNS(ctx context.Context) (string, error) {
	threshold := timex.Unix() - 60*86400

	// 1. stale 节点与其域名 (host 优先, 次 sni, 再裸 server 非 IPv4)
	var nodes []model.Node
	_ = database.DB.WithContext(ctx).
		Select("id", "server", "node_heartbeat").
		Where("node_heartbeat < ?", threshold).
		Find(&nodes).Error

	staleDomains := map[string][]int32{} // 域名 → 节点 id 集
	staleIDs := []int32{}
	for i := range nodes {
		n := &nodes[i]
		domain := extractNodeDomain(n.Server)
		if domain == "" {
			continue
		}
		staleIDs = append(staleIDs, n.ID)
		staleDomains[domain] = append(staleDomains[domain], n.ID)
	}

	// 2. 普通池 zone 的全部 A/AAAA 记录
	var errors []string
	type match struct {
		Domain string  `json:"domain"`
		Nodes  []int32 `json:"nodes"`
	}
	matches := map[string][]int32{}
	totalRecords := 0

	for _, entry := range DomainPoolParse() {
		if entry.CDN {
			continue // 普通池 (对齐 PHP DomainPool::parse)
		}
		zoneID, err := ZoneIDForDomain(entry.Domain)
		if err != nil || zoneID == "" {
			errors = append(errors, fmt.Sprintf("%s: zone lookup failed: %v", entry.Domain, err))
			continue
		}
		recs, err := CFListRecords(zoneID, TokenForDomain(entry.Domain))
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: listRecords failed: %v", entry.Domain, err))
			continue
		}
		for _, r := range recs {
			if r.Type != "A" && r.Type != "AAAA" {
				continue
			}
			totalRecords++
			name := strings.ToLower(r.Name)
			if ids, ok := staleDomains[name]; ok {
				matches[name] = append(matches[name], ids...)
			}
		}
	}

	// 3. 汇总输出 (对齐 PHP JSON 结构)
	matchList := []match{}
	for domain, ids := range matches {
		seen := map[int32]bool{}
		uniq := []int32{}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				uniq = append(uniq, id)
			}
		}
		sort.Slice(uniq, func(i, j int) bool { return uniq[i] < uniq[j] })
		matchList = append(matchList, match{Domain: domain, Nodes: uniq})
	}
	sort.Slice(matchList, func(i, j int) bool { return matchList[i].Domain < matchList[j].Domain })

	report := map[string]any{
		"generated_at":              time.Now().Format("2006-01-02 15:04:05"),
		"stale_threshold_days":      60,
		"total_stale_nodes":         len(staleIDs),
		"total_dns_records_scanned": totalRecords,
		"total_matches":             len(matchList),
		"errors":                    errors,
		"stale_nodes":               staleIDs,
		"match_results":             matchList,
	}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// extractNodeDomain 从 server 串提取节点域名 (host → sni → 裸串非 IPv4)。
func extractNodeDomain(server string) string {
	if v, err := url.ParseQuery(server); err == nil {
		if h := v.Get("host"); h != "" {
			return strings.ToLower(h)
		}
		if sni := v.Get("sni"); sni != "" {
			return strings.ToLower(sni)
		}
	}
	s := strings.TrimSpace(server)
	if s == "" || net.ParseIP(s) != nil {
		return ""
	}
	return strings.ToLower(s)
}
