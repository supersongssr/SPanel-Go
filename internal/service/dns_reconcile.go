// 节点集群 DNS 对账服务 —— 对齐 PHP app/Services/DnsReconcileService.php 的忠实移植。
//
// DNS 解析策略 (address_mode 已删除, 只剩 ip 基础模式):
//
//	CDN 集群 (xhttp-cdn):
//	  - 只解析 main host → 一条记录 (优先 A/IPv4, 无 IPv4 才 AAAA/IPv6)
//	  - proxied=false (灰云); clone 共享 main host 无需独立 DNS
//
//	非 CDN 集群:
//	  - main 节点: add=真实IP, 不建 DNS
//	  - IPv4 clone (fission 前半区): add=域名 (方案C分散), 建 A 记录 → IPv4
//	  - IPv6 clone (fission 后半区): add=裸 ipv6, 不建 DNS (规避 ipv6 DNS 故障)
//
//	FQDN 来源: CDN→server.host (=main host); 非CDN clone→server.add (=轮换域名)
//
// 与 PHP 的结构性差异 (取舍):
//   - CF 驱动 (cloudflare.go) 未提供 PUT 更新接口, Scene D 漂移修正以
//     "删旧记录 + 建新记录" 等价实现 (对齐 PHP updateRecord 的语义效果,
//     记录 ID 会变化, 本地缓存 cf_record_id 同步改写);
//   - CFError 不携带 CF 业务错误码, 81058 (记录已存在) 以 HTTP 4xx +
//     message 文本 ("already exists" / "81058") 近似判定;
//   - TTL=120 (对齐 PHP createRecord/updateRecord 默认参数)。
package service

import (
	"errors"
	"log"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/timex"

	"gorm.io/gorm"
)

// dnsReconcileTimeout 对齐 PHP reconcileCluster 的 $timeout = 90 (秒),
// 超时后剩余节点记 skipped_timeout 跳过。
const dnsReconcileTimeout = 90

// dnsRecordTTL CF 记录 TTL, 对齐 PHP createRecord/updateRecord 的 $ttl = 120。
const dnsRecordTTL = 120

// ReconcileClusterDNS 对齐 DnsReconcileService::reconcileCluster(mainNodeId, force):
// 对齐整组 (main + clone) 的期望 A/AAAA 记录与 CF/DB 现状 (三方对账)。
// 返回完整响应 map (直接 JSON 输出给调用方), err 仅内部异常。
func ReconcileClusterDNS(nodeID int64, force bool) (map[string]any, error) {
	start := time.Now()

	if database.DB == nil {
		return nil, errors.New("[DnsReconcile] database not initialized")
	}

	// PHP: $mainNode = Node::find($mainNodeId); if (!$mainNode || empty($mainNode->node_ids))
	var mainNode model.Node
	if err := database.DB.First(&mainNode, nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return map[string]any{"status": "error", "message": "node_not_found"}, nil
		}
		return nil, err
	}
	if mainNode.NodeIDs == "" {
		return map[string]any{"status": "error", "message": "node_not_found"}, nil
	}

	// PHP: $nodeIds = explode(',', $mainNode->node_ids); array_map('intval', ...)
	parts := strings.Split(mainNode.NodeIDs, ",")
	nodeIds := make([]int64, 0, len(parts))
	for _, p := range parts {
		n, _ := strconv.Atoi(strings.TrimSpace(p)) // Atoi 失败→0, 对齐 intval 容错
		nodeIds = append(nodeIds, int64(n))
	}

	// 集群级 CDN 检测 (以 main v2_name 为准)
	// PHP: parse_str($mainNode->server) → v2_name → V2Presets::isCdn
	clusterV2Name := dnsParseQuery(mainNode.Server).Get("v2_name")
	isCdnCluster := presetsIsCdn(clusterV2Name)

	results := []map[string]any{}
	stats := map[string]int{
		"created": 0, "updated": 0, "deleted": 0,
		"skipped": 0, "failed": 0, "no_change": 0,
	}

	for _, nid := range nodeIds {
		// PHP: if ((microtime(true) - $startTime) > $timeout) → skipped_timeout
		if time.Since(start).Seconds() > dnsReconcileTimeout {
			results = append(results,
				dnsResult("skipped_timeout", false, nid, "A", "", false))
			stats["skipped"]++
			continue
		}

		// PHP: $node = Node::find($nid); if (!$node) continue;
		var node model.Node
		if err := database.DB.First(&node, nid).Error; err != nil {
			continue
		}

		for _, r := range dnsReconcileSingleNode(&node, isCdnCluster, force) {
			results = append(results, r)
			dnsUpdateStats(stats, r)
		}
	}

	// PHP: cleanOrphans($mainNodeId, $nodeIds) — 方法体仅执行一次 SELECT 后不做任何
	// 操作 (注释: "No action needed ... could be extended"), 为死代码, 不移植查询。

	elapsed := math.Round(time.Since(start).Seconds()*1000) / 1000 // PHP round(x, 3)
	message := "clone A records synchronized"
	if isCdnCluster {
		message = "CDN DNS synchronized (main host)"
	}
	return map[string]any{
		"status":    "success",
		"results":   results,
		"stats":     stats,
		"elapsed_s": elapsed,
		"message":   message,
	}, nil
}

// dnsReconcileSingleNode 对齐 PHP reconcileSingleNode:
// 为单个节点计算应建的 DNS 记录并调 reconcileRecord。
// 返回 0~2 条 result; nil = 该节点不需 DNS (跳过)。
func dnsReconcileSingleNode(node *model.Node, isCdnCluster, force bool) []map[string]any {
	serverParams := dnsParseQuery(node.Server)
	ipParams := dnsParseQuery(node.NodeIP)
	ipv4 := ipParams.Get("ip")
	ipv6 := ipParams.Get("ipv6")
	isMain := node.IsClone == 0
	nodeID := int64(node.ID)

	// ── CDN 模式: 只建 main host 的单条记录 (clone 共享, 不重复建) ──
	if isCdnCluster {
		if !isMain {
			return nil // CDN clone 共用 main host, 无需独立 DNS
		}
		host := serverParams.Get("host")
		if host == "" {
			return nil
		}
		return []map[string]any{
			dnsBuildSingleStackRecord(nodeID, host, ipv4, ipv6, false, force),
		}
	}

	// ── 非 CDN 模式: main 不建 DNS; clone 按半区处理 ──
	if isMain {
		return nil // main add=真实IP, 不建 DNS
	}

	// clone: 判断半区. fission 矩阵里 IPv6 clone 的 add= 是裸 ipv6; IPv4 clone 的 add= 是域名.
	add := serverParams.Get("add")
	if dnsIsIPv6(add) {
		return nil // IPv6 clone: 不建 DNS (规避 ipv6 DNS 故障)
	}

	// IPv4 clone: add=域名 → 建 A 记录 (解析到节点 ipv4)
	if add == "" || ipv4 == "" {
		return nil // 无域名/无ipv4, 无法建
	}
	subdomain, rootDomain, ok := dnsSplitHost(add)
	if !ok {
		return nil // 无根域
	}
	return []map[string]any{
		dnsReconcileRecord(nodeID, "A", subdomain, rootDomain, ipv4, false, force),
	}
}

// dnsBuildSingleStackRecord 对齐 PHP buildSingleStackRecord:
// CDN 模式为一个 host 建 [一条] 记录 — 优先 A/IPv4, 无 IPv4 才 AAAA/IPv6。
func dnsBuildSingleStackRecord(nodeID int64, host, ipv4, ipv6 string, proxied, force bool) map[string]any {
	subdomain, rootDomain, _ := dnsSplitHost(host) // 空 root 交由 reconcileRecord 判 skipped_no_zone (与 PHP 一致)

	if ipv4 != "" {
		return dnsReconcileRecord(nodeID, "A", subdomain, rootDomain, ipv4, proxied, force)
	}
	if ipv6 != "" { // 无 IPv4 → AAAA / IPv6
		return dnsReconcileRecord(nodeID, "AAAA", subdomain, rootDomain, ipv6, proxied, force)
	}
	return dnsResult("skipped_no_ip", false, nodeID, "A", host, false)
}

// dnsReconcileRecord 对齐 PHP reconcileRecord: 单条记录三方对账
// (Scene A 缓存精确命中 / B 域名变更: 删旧建新 / C 新建 / D IP或proxied漂移修正)。
func dnsReconcileRecord(nodeID int64, recType, subdomain, rootDomain, content string, proxied, force bool) map[string]any {
	fqdn := subdomain + "." + rootDomain

	// Get zone ID early (needed for fallback lookups)
	zoneID, zerr := ZoneIDForDomain(rootDomain)
	if zerr != nil {
		log.Printf("[DnsReconcile] zone resolve failed domain=%s: %v", rootDomain, zerr)
		zoneID = "" // PHP getZoneId 失败返回 null → skipped_no_zone
	}
	// 该域名对应 CF 账号的 token (CDN 域名 → 独立 CDN token; 空则回退全局)
	token := TokenForDomain(rootDomain)

	// Get local cache
	// PHP: DnsRecord::where('node_id')->where('record_type')->first()
	var local *model.DnsRecord
	var rec model.DnsRecord
	err := database.DB.Where("node_id = ?", nodeID).
		Where("record_type = ?", recType).
		First(&rec).Error
	switch {
	case err == nil:
		local = &rec
	case errors.Is(err, gorm.ErrRecordNotFound):
		// 无本地缓存 → Scene C
	default:
		log.Printf("[DnsReconcile] load local record failed node=%d type=%s: %v", nodeID, recType, err)
	}

	// Scene A: Cache hit — exact match, skip CF API
	if !force && local != nil &&
		local.Subdomain == subdomain &&
		local.RootDomain == rootDomain &&
		local.Content == content &&
		int(local.Proxied) == dnsBoolToInt(proxied) {
		return dnsResult("no_change", true, nodeID, recType, fqdn, proxied)
	}

	if zoneID == "" {
		return dnsResult("skipped_no_zone", false, nodeID, recType, fqdn, proxied)
	}

	// Scene B: Domain changed — delete old + create new
	if local != nil && (local.Subdomain != subdomain || local.RootDomain != rootDomain) {
		// Delete old record from CF — use OLD domain's zoneId + token
		if local.CFRecordID != "" {
			oldZoneID, ozerr := ZoneIDForDomain(local.RootDomain)
			if ozerr == nil && oldZoneID != "" {
				if derr := CFDeleteRecord(oldZoneID, local.CFRecordID, TokenForDomain(local.RootDomain)); derr != nil {
					log.Printf("[DnsReconcile] Scene B delete failed: %v", derr)
				}
			} else {
				log.Printf("[DnsReconcile] Scene B: no zone for old domain %s, skip CF delete", local.RootDomain)
			}
		}

		cfID, failOut := dnsCreateRecord(zoneID, token, nodeID, recType, fqdn, content, proxied)
		if failOut != nil {
			return failOut // create_failed
		}
		dnsUpsertRecord(local, nodeID, recType, subdomain, rootDomain, content, proxied, cfID)
		out := dnsResult("created", true, nodeID, recType, fqdn, proxied)
		out["cf_id"] = cfID
		return out
	}

	// Scene C: New record — create
	if local == nil {
		cfID, failOut := dnsCreateRecord(zoneID, token, nodeID, recType, fqdn, content, proxied)
		if failOut != nil {
			return failOut // create_failed
		}
		dnsUpsertRecord(nil, nodeID, recType, subdomain, rootDomain, content, proxied, cfID)
		out := dnsResult("created", true, nodeID, recType, fqdn, proxied)
		out["cf_id"] = cfID
		return out
	}

	// Scene D: IP or proxied changed — update
	needUpdate := local.Content != content || int(local.Proxied) != dnsBoolToInt(proxied)

	if needUpdate && local.CFRecordID != "" {
		newID, ok := dnsUpdateRecordViaRecreate(zoneID, token, local.CFRecordID, recType, fqdn, content, proxied)
		if !ok {
			return dnsResult("update_failed", false, nodeID, recType, fqdn, proxied)
		}
		// PHP: action = (content 变更) ? updated_ip : updated_proxied (upsert 前比较旧值)
		action := "updated_proxied"
		if local.Content != content {
			action = "updated_ip"
		}
		dnsUpsertRecord(local, nodeID, recType, subdomain, rootDomain, content, proxied, newID)
		out := dnsResult(action, true, nodeID, recType, fqdn, proxied)
		out["cf_id"] = newID
		return out
	}

	// No change needed (but cache was stale; force 重写亦走此分支)
	dnsUpsertRecord(local, nodeID, recType, subdomain, rootDomain, content, proxied, local.CFRecordID)
	return dnsResult("no_change", true, nodeID, recType, fqdn, proxied)
}

// dnsCreateRecord 对齐 PHP Scene B/C 的 createRecord 调用 (含 81058 回退):
// 返回 (cfID, nil) 成功; ("" , result) 时 result 为 create_failed 结果。
func dnsCreateRecord(zoneID, token string, nodeID int64, recType, fqdn, content string, proxied bool) (string, map[string]any) {
	cfID, err := CFCreateRecord(zoneID, token, CFRecord{
		Type:    recType,
		Name:    fqdn,
		Content: content,
		Proxied: proxied,
		TTL:     dnsRecordTTL,
	})
	if err != nil {
		id, ok := dnsHandleCreateException(err, zoneID, recType, fqdn, token)
		if !ok {
			log.Printf("[DnsReconcile] create failed for node %d %s: %v", nodeID, fqdn, err)
			return "", dnsResult("create_failed", false, nodeID, recType, fqdn, proxied)
		}
		cfID = id
	}
	if cfID == "" { // PHP: if ($cfId) — 空值视为失败
		return "", dnsResult("create_failed", false, nodeID, recType, fqdn, proxied)
	}
	return cfID, nil
}

// dnsUpdateRecordViaRecreate Scene D 漂移修正。
// 取舍: Go 版 CF 驱动 (cloudflare.go) 未提供 PUT 更新接口 (PHP updateRecord),
// 以 "删旧记录 (404 容忍=已被删) + 建新记录" 等价实现; 记录 ID 会变化,
// 调用方随后以新 ID 改写本地缓存。
func dnsUpdateRecordViaRecreate(zoneID, token, oldRecordID, recType, fqdn, content string, proxied bool) (string, bool) {
	if err := CFDeleteRecord(zoneID, oldRecordID, token); err != nil && !IsCFNotFound(err) {
		log.Printf("[DnsReconcile] Scene D delete failed %s: %v", fqdn, err)
		return "", false
	}
	newID, err := CFCreateRecord(zoneID, token, CFRecord{
		Type:    recType,
		Name:    fqdn,
		Content: content,
		Proxied: proxied,
		TTL:     dnsRecordTTL,
	})
	if err != nil {
		if id, ok := dnsHandleCreateException(err, zoneID, recType, fqdn, token); ok {
			return id, true
		}
		log.Printf("[DnsReconcile] Scene D recreate failed %s: %v", fqdn, err)
		return "", false
	}
	if newID == "" {
		return "", false
	}
	return newID, true
}

// dnsUpsertRecord 对齐 PHP upsertDnsRecord: 本地 dns_records 缓存行 upsert。
// 更新走 Updates(map), 新建走 Table().Create(map) (均不触碰 ttl 列, 对齐 PHP 不写 ttl)。
func dnsUpsertRecord(existing *model.DnsRecord, nodeID int64, recType, subdomain, rootDomain, content string, proxied bool, cfID string) {
	if database.DB == nil {
		return
	}
	now := int32(timex.Unix()) // PHP: time()
	proxiedInt := dnsBoolToInt(proxied)
	if existing != nil {
		if err := database.DB.Model(&model.DnsRecord{}).Where("id = ?", existing.ID).
			Updates(map[string]any{
				"record_type":  recType,
				"subdomain":    subdomain,
				"root_domain":  rootDomain,
				"content":      content,
				"proxied":      proxiedInt,
				"cf_record_id": cfID,
				"updated_at":   now,
			}).Error; err != nil {
			log.Printf("[DnsReconcile] upsert update failed node=%d rec=%d: %v", nodeID, existing.ID, err)
		}
		return
	}
	if err := database.DB.Table("dns_records").Create(map[string]any{
		"node_id":      nodeID,
		"record_type":  recType,
		"subdomain":    subdomain,
		"root_domain":  rootDomain,
		"content":      content,
		"proxied":      proxiedInt,
		"cf_record_id": cfID,
		"updated_at":   now,
	}).Error; err != nil {
		log.Printf("[DnsReconcile] upsert create failed node=%d: %v", nodeID, err)
	}
}

// dnsHandleCreateException 对齐 PHP handleCreateException:
// CF 81058 ("An identical record already exists") 时回退 getRecord (listRecords
// 按 type+name 过滤) 取已存在记录的 ID, 用于同步本地缓存。
// 返回 (cfID, true) 成功; ("", false) 不可恢复。
func dnsHandleCreateException(err error, zoneID, recType, fqdn, token string) (string, bool) {
	if !dnsIsDuplicateExistsErr(err) {
		return "", false
	}
	log.Printf("[DnsReconcile] Record already exists on CF (81058), falling back to getRecord for %s", fqdn)
	recs, lerr := CFListRecords(zoneID, token)
	if lerr != nil {
		log.Printf("[DnsReconcile] Fallback getRecord also failed for %s: %v", fqdn, lerr)
		return "", false
	}
	for _, r := range recs {
		if r.Type == recType && strings.EqualFold(r.Name, fqdn) && r.ID != "" {
			return r.ID, true
		}
	}
	return "", false
}

// dnsIsDuplicateExistsErr 近似判定 CF 81058 (记录已存在)。
// 取舍: Go 版 CFError 只保留 HTTP 状态 + message, 不携带 CF 业务错误码,
// 故以 4xx + message 文本 ("81058" / "already exists") 判定。
func dnsIsDuplicateExistsErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if !strings.Contains(msg, "81058") &&
		!strings.Contains(strings.ToLower(msg), "already exists") {
		return false
	}
	if cf, ok := err.(*CFError); ok {
		return cf.Status >= 400 && cf.Status < 500
	}
	return true
}

// dnsUpdateStats 对齐 PHP updateStats: action 命中 stats 键则自增;
// updated_ip / updated_proxied 归并进 updated; 其余 (create_failed /
// update_failed / skipped_* / skipped_no_zone 等) 不计数 (与 PHP 一致)。
func dnsUpdateStats(stats map[string]int, result map[string]any) {
	action, _ := result["action"].(string)
	if _, ok := stats[action]; ok {
		stats[action]++
		return
	}
	if action == "updated_ip" || action == "updated_proxied" {
		stats["updated"]++
	}
}

// dnsResult 构造单条 result (对齐 PHP 各分支返回的关联数组结构)。
func dnsResult(action string, success bool, nodeID int64, recType, fqdn string, proxied bool) map[string]any {
	return map[string]any{
		"action":  action,
		"success": success,
		"node_id": nodeID,
		"type":    recType,
		"fqdn":    fqdn,
		"proxied": proxied,
	}
}

// dnsParseQuery 对齐 PHP parse_str (容错解析, 失败保留已解析键值)。
func dnsParseQuery(s string) url.Values {
	v, _ := url.ParseQuery(s)
	return v
}

// dnsIsIPv6 对齐 PHP filter_var($add, FILTER_VALIDATE_IP, FILTER_FLAG_IPV6)。
func dnsIsIPv6(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() == nil
}

// dnsSplitHost 对齐 PHP explode('.', $host, 2): 返回 (子域, 根域, 根域非空)。
func dnsSplitHost(host string) (subdomain, rootDomain string, ok bool) {
	parts := strings.SplitN(host, ".", 2)
	if len(parts) > 0 {
		subdomain = parts[0]
	}
	if len(parts) > 1 {
		rootDomain = parts[1]
	}
	return subdomain, rootDomain, rootDomain != ""
}

// dnsBoolToInt 对齐 PHP (int)(bool) 强转 (proxied 比对/落库用)。
func dnsBoolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
