// 节点 API —— 对齐 PHP app/Services/NodeApiService.php 的 allocateNodeId /
// registerNode / createCloneNodes 忠实移植 (reportStatus/updateUnlock/editNode
// 见 node_api_status.go; deleteNodeDnsRecords 已有 node_dns.go, 不重复移植)。
//
// register 契约 (PHP registerNode 头部注释): 只记录节点上报的字段, 不触碰未上报
// 的字段 —— apply_id (AllocateNodeID) 是唯一 provisioning 归零入口, register 只做
// recording + 网络身份签发。DB 写用 Updates(map)/Exec 精确列 (对齐 Eloquent 只写
// dirty 属性的语义, 不整结构体覆盖并发写的列)。
package service

import (
	"fmt"
	"log"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/notify"
	"spanel-go/internal/pkg/timex"
)

// nodePortRe 对齐 PHP preg_replace('/\bport=443\b/'): server 串外部端口改写
var nodePortRe = regexp.MustCompile(`\bport=443\b`)

// unlockSplitRe 对齐 PHP preg_split('/[,&]/')
var unlockSplitRe = regexp.MustCompile(`[,&]`)

// AllocateNodeID 对齐 PHP allocateNodeId (apply_id 端点后端实现):
// 单一职责 = 返回一个「全字段默认值」的干净节点 ID。**恒新建, 永不复用**
// (max(id)+1, ≥MIN_ALLOCATABLE_ID=100 避开保留区), NodeDefaultsReset 全字段默认值
// (含 hb=now, 被装机失败遗弃的行因此有年龄可判, 由 GC 按死池口径回收)。
//
// [已知并发窗口 · 站长确认不视为 bug] max(id)+1 无锁: 两台节点同时 apply_id 会
// 读到同一 max → 后 INSERT 者主键冲突, 错误冒给 handler 返回 internal_error,
// 节点安装脚本重试即自愈。不为此引入锁/重试复杂度。
func AllocateNodeID(nodeIP, nodeIPv6 string) (int64, error) {
	if database.DB == nil {
		return 0, errNodeAPINilDB
	}
	// IP 白名单校验 (FILTER_VALIDATE_IP, 与 register 同口径): 非法值按未上报处理,
	// 不入库不进渲染面 (node_ip 查询串在 admin/node/edit.tpl 未转义渲染)
	cleanIP := buildNodeIpString(
		normalizeReportedIP(nodeIP, false),
		normalizeReportedIP(nodeIPv6, true),
	)

	newID, err := nodeNextID()
	if err != nil {
		return 0, err
	}

	// DNS GC 衔接: 清掉任何仍绑定该 id 的孤儿 DNS 记录 (DB + Cloudflare)。
	// 恒新建语义下 id 首次使用, 正常无记录; 此处兜底手工删行/重置自增残留。
	if deleted := DeleteNodeDNSRecords(newID); deleted > 0 {
		log.Printf("[NodeApi] allocateNodeId: cleaned %d orphan dns_records for new node_id=%d", deleted, newID)
	}

	// 新建行: 显式列 INSERT (对齐 Eloquent 只插 set 属性; 不走整结构体 Create,
	// 避免模型零值串写入 decimal 列)
	node := model.Node{ID: int32(newID)}
	ResetNodeToDefaults(&node, cleanIP)
	cols := nodeResetColumns(&node)
	cols["id"] = newID
	if err := nodeInsertRow(cols); err != nil {
		return 0, err
	}
	return newID, nil
}

// nodeNextID max(id)+1 (≥MIN_ALLOCATABLE_ID; 空表 → 100)
func nodeNextID() (int64, error) {
	var maxID int64
	if err := database.DB.Raw("SELECT COALESCE(MAX(id), 0) FROM ss_node").Scan(&maxID).Error; err != nil {
		return 0, fmt.Errorf("allocateNodeId max(id) failed: %w", err)
	}
	newID := maxID + 1
	if newID < nodeMinAllocatableID {
		newID = nodeMinAllocatableID
	}
	if newID > math.MaxInt32 {
		return 0, fmt.Errorf("allocateNodeId exhausted: next id %d exceeds int32", newID)
	}
	return newID, nil
}

// nodeInsertRow 按显式列清单 INSERT ss_node (列序排序保证确定性)。
func nodeInsertRow(cols map[string]any) error {
	if len(cols) == 0 {
		return nil
	}
	keys := make([]string, 0, len(cols))
	for k := range cols {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	placeholders := make([]string, len(keys))
	vals := make([]any, len(keys))
	for i, k := range keys {
		placeholders[i] = "?"
		vals[i] = cols[k]
	}
	sql := "INSERT INTO ss_node (" + strings.Join(keys, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ")"
	if err := database.DB.Exec(sql, vals...).Error; err != nil {
		return err
	}
	return nil
}

// nodeResetColumns ResetNodeToDefaults 拥有的全部列 (+条件 node_ip) 的落库快照。
// node_ids/country_code 对齐 PHP null 赋值 (SQL NULL)。
func nodeResetColumns(n *model.Node) map[string]any {
	cols := map[string]any{
		"name":                    n.Name,
		"type":                    n.Type,
		"server":                  n.Server,
		"method":                  n.Method,
		"info":                    n.Info,
		"status":                  n.Status,
		"node_unlock":             n.NodeUnlock,
		"config_hash":             n.ConfigHash,
		"node_heartbeat":          n.NodeHeartbeat,
		"node_class":              n.NodeClass,
		"node_group":              n.NodeGroup,
		"node_bandwidth":          n.NodeBandwidth,
		"bandwidthlimit_resetday": n.BandwidthResetDay,
		"traffic_used":            n.TrafficUsed,
		"traffic_left":            n.TrafficLeft,
		"traffic_limit":           n.TrafficLimit,
		"traffic_used_daily":      n.TrafficUsedDaily,
		"traffic_left_daily":      n.TrafficLeftDaily,
		"traffic_raw_total":       n.TrafficRawTotal,
		"traffic_reset_day":       n.TrafficResetDay,
		"traffic_max_day_value":   n.TrafficMaxDayValue,
		"traffic_last_reset_time": n.TrafficLastResetTime,
		"node_rxtx":               n.NodeRxTx,
		"node_speedlimit":         n.NodeSpeedlimit,
		"traffic_rate":            n.TrafficRate,
		"sort":                    n.Sort,
		"node_online":             n.NodeOnline,
		"custom_rss":              n.CustomRSS,
		"node_sort":               n.NodeSort,
		"mu_only":                 n.MuOnly,
		"is_clone":                n.IsClone,
		"custom_method":           n.CustomMethod,
		"node_connector":          n.NodeConnector,
		"node_ids":                nil,
		"country_code":            nil,
		"node_cost":               n.NodeCost,
		"api_token":               n.APIToken,
	}
	if n.NodeIP != "" {
		cols["node_ip"] = n.NodeIP
	}
	return cols
}

// nodeBumpUserSyncStamp 对齐 PHP Capsule::update('UPDATE `user` SET `status_changed_at`
// = UNIX_TIMESTAMP()'): 重注册/编辑导致等级/分组实际变化时全表打戳, 各节点当轮
// 退回全量重算资格 (方案 2026-11-08 §3.5)。
func nodeBumpUserSyncStamp() error {
	return database.DB.Exec("UPDATE `user` SET `status_changed_at` = UNIX_TIMESTAMP()").Error
}

// RegisterNode 对齐 PHP registerNode (register 端点核心契约):
// 返回完整响应 map (status/node_id/clone_node_ids/... 直接 JSON 输出);
// err 仅表示内部异常 (handler 记日志返回 internal_error), 业务错误编进 map。
func RegisterNode(nodeID int64, params url.Values) (map[string]any, error) {
	if database.DB == nil {
		return nil, errNodeAPINilDB
	}
	var node model.Node
	if err := database.DB.First(&node, nodeID).Error; err != nil {
		return map[string]any{"status": "error", "message": "node_not_found"}, nil
	}

	// 增量同步 bump 前置快照: 存量节点重注册可能改写 node_class/node_group
	oldNodeClass := node.NodeClass
	oldNodeGroup := node.NodeGroup
	oldTrafficUsed := node.TrafficUsed
	oldResetDay := node.TrafficResetDay

	// dirty 列集 (对齐 Eloquent「只写动过的属性」; 同时同步内存态供后续派生计算)
	dirty := map[string]any{}
	set := func(col string, v any) { dirty[col] = v }

	// v2 面板 API 自愈 (无条件补发, 与 NPanel 同口径): 存量节点重装自带 ID 上报、
	// 不走 apply_id, api_token 可能为空 → 补发后重拉配置即带 panelApi 段;
	// 已有 token 不动。随下方 save 落库。
	if nodeAPITokenIssueIfNeeded(&node) {
		set("api_token", node.APIToken)
	}

	// ── 1. 业务属性: 上报则写, 未上报则保持既有值 (不归零) ──

	// 展示型文本一律过 sanitizeDisplayText 白名单净化 (防存储型 XSS)
	cityName := ""
	if phpNotEmpty(params.Get("node_city")) {
		cityName = sanitizeDisplayText(params.Get("node_city"), 64)
	}

	// name / country_code (派生自 node_country_code + node_city);
	// [净化后再判空] 上报非空但全是被过滤字符时视为垃圾上报, 整体跳过更新
	countryCode := ""
	if phpNotEmpty(params.Get("node_country_code")) {
		countryCode = strings.ToUpper(sanitizeDisplayText(params.Get("node_country_code"), 32))
	} else if phpNotEmpty(params.Get("node_country")) {
		countryCode = "UN"
	}
	if countryCode != "" {
		displayName := countryCode
		if cityName != "" {
			displayName += "-" + cityName
		}
		node.Name = displayName
		set("name", node.Name)
		node.CountryCode = countryCode
		set("country_code", node.CountryCode)
	}

	if v := params.Get("node_info"); v != "" { // isset && !== ""
		node.Info = sanitizeDisplayText(v, 128)
		set("info", node.Info)
	}

	// node_bandwidth (Mbps) — decimal 列
	if v := params.Get("node_bandwidth"); v != "" {
		node.NodeBandwidth = phpFloatToString(phpFloat(v))
		set("node_bandwidth", node.NodeBandwidth)
	}

	// node_cost — 上报则写 (后续 traffic_rate / node_class 派生复用; 未上报不读旧值)
	nodeCost := 0.0
	if raw, ok := params["node_cost"]; ok && len(raw) > 0 {
		nodeCost = math.Max(0, phpFloat(raw[0]))
		node.NodeCost = int32(math.Round(nodeCost)) // ss_node.node_cost 为 int 列
		set("node_cost", node.NodeCost)
	}

	// traffic_rate 派生: 上报 rate 优先, 否则按 cost, 都未上报保持原值
	if rate, ok := trafficRateDeriveFromRegistration(params, nodeCost); ok {
		node.TrafficRate = float32(rate)
		set("traffic_rate", node.TrafficRate)
	}

	// node_group — 上报则写, 未上报回退 config 默认 (不能落到 0: 全员可见语义)
	if phpNotEmpty(params.Get("node_group")) {
		node.NodeGroup = int32(phpInt(params.Get("node_group")))
	} else {
		node.NodeGroup = int32(nodeConfigRegisterGroup())
	}
	set("node_group", node.NodeGroup)
	nodeGroup := node.NodeGroup

	// traffic_limit (上报单位 GB → 字节)
	if v := params.Get("node_traffic_limit"); v != "" {
		node.TrafficLimit = phpInt(v) * 1024 * 1024 * 1024
		set("traffic_limit", node.TrafficLimit)
	}

	// traffic_reset_day — 0 = 「未配置」哨兵值 (触发 warnIfNoResetDay 配置告警)
	if phpNotEmpty(params.Get("node_traffic_resetday")) {
		node.TrafficResetDay = int32(phpInt(params.Get("node_traffic_resetday")))
		set("traffic_reset_day", node.TrafficResetDay)
		// NodeResetDayAuditLogger::intend($node, 'api.node.register') → 落库后补审计
	} else {
		monthlyWarnIfNoResetDay(&node)
	}

	// node_rxtx — 合法 (tx/rx/rxtx, 大小写不敏感) 则写; 否则回退 "rxtx";
	// 非法值发 TG 告警 (warnIfInvalidRxtx)
	rawRxtx := params.Get("node_rxtx")
	reportedRxtx := strings.ToLower(strings.TrimSpace(rawRxtx))
	if reportedRxtx == "tx" || reportedRxtx == "rx" || reportedRxtx == "rxtx" {
		node.NodeRxTx = reportedRxtx
	} else {
		if reportedRxtx != "" {
			warnIfInvalidRxtx(&node, rawRxtx)
		}
		node.NodeRxTx = "rxtx"
	}
	set("node_rxtx", node.NodeRxTx)
	nodeRxtx := node.NodeRxTx
	if nodeRxtx == "" {
		nodeRxtx = "tx"
	}

	// 流量继承 (traffic_used) — 仅当上报了有效值才覆盖 (复用节点不丢失既有用量)
	if raw, ok := params["traffic_used"]; ok && len(raw) > 0 {
		if trafficUsed := phpInt(raw[0]); trafficUsed > 0 {
			node.TrafficUsed = trafficUsed
			set("traffic_used", node.TrafficUsed)
			if node.TrafficUsed != oldTrafficUsed {
				log.Printf("[NodeApi] [traffic_used_audit] node=#%d traffic_used %d->%d src=api.node.register",
					node.ID, oldTrafficUsed, node.TrafficUsed)
			}
		}
	}

	// 历史日峰值继承 (traffic_max_day_value, 只增不减, 仅有效值才覆盖)
	if raw, ok := params["traffic_max_day_value"]; ok && len(raw) > 0 {
		if m := phpInt(raw[0]); m > 0 {
			node.TrafficMaxDayValue = m
			set("traffic_max_day_value", node.TrafficMaxDayValue)
		}
	}

	// traffic_raw_total — 仅当上报了 raw_rx/raw_tx 才重算基线 (避免重注册把基线写 0)
	if hasParam(params, "raw_tx") || hasParam(params, "raw_rx") {
		rawTx, rawRx := 0.0, 0.0
		if raw, ok := params["raw_tx"]; ok && len(raw) > 0 {
			rawTx = phpFloat(raw[0])
		}
		if raw, ok := params["raw_rx"]; ok && len(raw) > 0 {
			rawRx = phpFloat(raw[0])
		}
		node.TrafficRawTotal = int64(monthlyBillRawTotal(rawTx, rawRx, nodeRxtx))
		set("traffic_raw_total", node.TrafficRawTotal)
	}

	// 周期派生字段 (left + daily): 始终按当前值重算 (幂等, 安全)
	monthlyApplyDerived(&node)
	set("traffic_left", node.TrafficLeft)
	set("traffic_used_daily", node.TrafficUsedDaily)
	set("traffic_left_daily", node.TrafficLeftDaily)

	// node_sort — 上报则写; 未上报交 resetOperationalMarks 清零 (运维标记不跨重装残留)
	if raw, ok := params["node_sort"]; ok && len(raw) > 0 {
		node.NodeSort = int32(phpInt(raw[0]))
	} else {
		ResetOperationalMarks(&node)
	}
	set("node_sort", node.NodeSort)

	// node_unlock — 上报则归一化为存储格式 (Key=Yes&...), 空结果保持既有
	if phpNotEmpty(params.Get("node_unlock")) {
		if normalized := normalizeUnlockServices(params.Get("node_unlock")); normalized != "" {
			node.NodeUnlock = normalized
			set("node_unlock", node.NodeUnlock)
		}
	}

	// ── 2. 网络身份 / provisioning: register 的核心契约 ──
	// IP 栈以本次上报为权威值; 缺失时回退节点既有 node_ip 解析值
	regIpv4 := normalizeReportedIP(params.Get("node_ip"), false)
	regIpv6 := normalizeReportedIP(params.Get("node_ipv6"), true)
	if regIpv4 == "" || regIpv6 == "" {
		existing := url.Values{}
		if node.NodeIP != "" {
			if q, err := url.ParseQuery(node.NodeIP); err == nil {
				existing = q
			}
		}
		if regIpv4 == "" && phpNotEmpty(existing.Get("ip")) {
			regIpv4 = existing.Get("ip")
		}
		if regIpv6 == "" && phpNotEmpty(existing.Get("ipv6")) {
			regIpv6 = existing.Get("ipv6")
		}
	}

	// node_ip 查询串 (硬件/位置信息)
	nodeIpParts := []string{"ip=" + regIpv4, "ipv6=" + regIpv6}
	if phpNotEmpty(params.Get("node_cpu")) {
		nodeIpParts = append(nodeIpParts, "node_cpu="+strconv.FormatInt(phpInt(params.Get("node_cpu")), 10))
	}
	if raw, ok := params["node_memory"]; ok && len(raw) > 0 {
		nodeIpParts = append(nodeIpParts, "node_memory="+phpFloatToString(phpFloat(raw[0])))
	}
	if raw, ok := params["node_disk"]; ok && len(raw) > 0 {
		nodeIpParts = append(nodeIpParts, "node_disk="+phpFloatToString(phpFloat(raw[0])))
	}
	if cityName != "" {
		nodeIpParts = append(nodeIpParts, "node_city="+cityName)
	}
	node.NodeIP = strings.Join(nodeIpParts, "&")
	set("node_ip", node.NodeIP)

	// ── 4. 动态协议分配 ──
	// v2_name: 上报且面板支持 → 用上报值; 上报但不支持 → 显式拒绝 (不静默降级,
	// 避免 CDN 节点退化成普通节点等部署不一致问题被掩盖); 未上报 → 复用节点沿用
	// server 串既有 v2_name (协议不变), 全新节点走配置默认
	reportedV2Name := ""
	if phpNotEmpty(params.Get("v2_name")) {
		reportedV2Name = params.Get("v2_name")
	}
	if reportedV2Name == "" {
		reportedV2Name = extractV2NameFromServer(node.Server)
	}
	var v2Name string
	if reportedV2Name != "" {
		v2Name = reportedV2Name
		if presetsGroup(v2Name) == nil {
			log.Printf("[NodeApi] registerNode rejected: node_id=%d v2_name='%s' not supported on this panel (supported: %s)",
				nodeID, v2Name, strings.Join(PresetsGroupNames(), ", "))
			return map[string]any{
				"status":    "error",
				"message":   "unsupported_v2_name",
				"v2_name":   v2Name,
				"supported": PresetsGroupNames(),
			}, nil
		}
	} else {
		v2Name = nodeConfigDefaultV2Name()
		if presetsGroup(v2Name) == nil {
			return map[string]any{"status": "error", "message": "unknown_v2_name"}, nil
		}
	}

	// PQ (后量子) v2_name 服务端回退: xhttp-pq / vision-reality-pq 要求
	// OpenSSL>=3.5 (X25519MLKEM768); 上报 openssl<3.5 或缺失 → 回退默认保证可用
	reportedOpenssl := ""
	if phpNotEmpty(params.Get("node_openssl")) {
		reportedOpenssl = params.Get("node_openssl")
	}
	if needsPqFallback(v2Name, reportedOpenssl) {
		originalV2Name := v2Name
		v2Name = nodeConfigDefaultV2Name()
		log.Printf("[NodeApi] PQ fallback: node_id=%d reported v2_name='%s' requires OpenSSL>=3.5 (X25519MLKEM768), reported node_openssl='%s' -> fell back to v2_name='%s'",
			nodeID, originalV2Name, reportedOpenssl, v2Name)
		if presetsGroup(v2Name) == nil {
			return map[string]any{"status": "error", "message": "unknown_v2_name"}, nil
		}
	}

	protocols := presetsGetOrderedProtocols(v2Name)
	mainProtocol := protocols[0]

	// CDN 模式 (xhttp-cdn): 独立 CF token + 独立域名池 + grey-cloud DNS host
	isCdn := presetsIsCdn(v2Name)

	// 随机 path: register 一次性生成, 主节点与所有 clone 共用
	paths := presetsGenerateRandomPaths(v2Name)

	// xhttp-verify: 启用组一次性生成随机 UUID, 主/clone 共用
	xhttpVerify := ""
	if presetsHasXhttpVerify(v2Name) {
		xhttpVerify = presetsGenerateXhttpVerify()
	}

	// REALITY: per-node X25519 密钥对 + shortId, 主/clone 共用
	realityPriv, realityPub, realitySid := "", "", ""
	if presetsIsReality(v2Name) {
		realityPriv, realityPub, realitySid = presetsGenerateRealityKeys()
	}

	// ── 8. Domain affinity: 域名池轮换防打爆; CDN 严格隔离 cdn=true 池 ──
	rootDomain := ""
	if phpNotEmpty(params.Get("root_domain")) {
		rootDomain = params.Get("root_domain")
	}
	pool := DomainPoolNormal()
	if isCdn {
		pool = DomainPoolCDN()
	}
	if rootDomain != "" && !poolContainsDomain(pool, rootDomain) {
		category := "普通"
		if isCdn {
			category = "CDN"
		}
		log.Printf("[NodeApi] registerNode: node_id=%d reported root_domain='%s' 不属于%s域名池 (或未配置 cdn 标志), 将从%s池重新分配",
			nodeID, rootDomain, category, category)
		rootDomain = ""
	}
	if rootDomain == "" {
		if pick, ok := poolPickDomain(pool); ok && pick.Domain != "" {
			rootDomain = pick.Domain
			log.Printf("[NodeApi] registerNode: node_id=%d auto-assigned root_domain='%s'", nodeID, rootDomain)
		}
	}
	if rootDomain == "" {
		poolLabel := "pool"
		if isCdn {
			poolLabel = "CDN pool"
		}
		log.Printf("[NodeApi] registerNode rejected: node_id=%d no domain available in %s", nodeID, poolLabel)
		return map[string]any{"status": "error", "message": "no_domain_available"}, nil
	}

	// host 前缀随机化 (uuid4 截 8 位, 防固定 s{nodeId} 形态被批量扫描)
	hostPrefix := presetsGenerateUUID4()[:8]
	nodeAddress := hostPrefix + "s" + strconv.FormatInt(nodeID, 10) + "." + rootDomain

	// xhttp-split: 独立下行域名 (dl_host), 同 rootDomain; dl_add 真实 IP (v4 优先)
	xhttpSplit := presetsIsXhttpSplit(v2Name)
	dlHost, dlAdd := "", ""
	if xhttpSplit {
		dlHost = presetsBuildDownloadHost(nodeID, rootDomain)
		dlAdd = regIpv4
		if dlAdd == "" {
			dlAdd = regIpv6
		}
	}

	// ECH: 仅 CDN 集群的 xhttp 协议 (hy2 不经过 CF 不用 ECH)
	ech := ""
	if isCdn && mainProtocol != "hy2" {
		ech = presetsBuildEchValue(rootDomain, &nodeID)
	}

	// node_port — 自定义外部端口 (默认 443)
	nodePort := int64(443)
	if raw, ok := params["node_port"]; ok && len(raw) > 0 {
		nodePort = phpInt(raw[0])
	}

	// server 串构建
	serverParts := []string{}
	mainServerStr := presetsBuildServerString(mainProtocol, false, v2Name, nodeAddress, paths)
	if nodePort != 443 {
		mainServerStr = nodePortRe.ReplaceAllString(mainServerStr, "port="+strconv.FormatInt(nodePort, 10))
	}
	serverParts = append(serverParts, mainServerStr)
	serverParts = append(serverParts, "v2_name="+v2Name)
	serverParts = append(serverParts, "node_rxtx="+nodeRxtx)
	serverParts = append(serverParts, "node_port="+strconv.FormatInt(nodePort, 10))
	serverParts = append(serverParts, presetsBuildPathsParam(paths)...)
	serverParts = append(serverParts, presetsBuildXhttpVerifyParam(xhttpVerify)...)
	serverParts = append(serverParts, presetsBuildRealityParam(realityPriv, realityPub, realitySid)...)
	serverParts = append(serverParts, presetsBuildXhttpSplitParam(dlHost, dlAdd)...)
	serverParts = append(serverParts, presetsBuildEchParam(ech)...)
	if isCdn {
		// padding 混淆键 (种子=节点 ID, 幂等): 服务端模板/订阅 extra/Clash 三端同源
		serverParts = append(serverParts, presetsBuildXobfsParam(map[string]string{
			"key":    presetsPickXobfsKey(nodeID),
			"header": presetsPickXobfsHeader(nodeID),
		})...)
	}
	// 地址策略统一交 AddressPolicy 决策 (CDN 优选域名 / 真实 IP)
	addrPolicyForMain := addressPolicyResolveForMain(nodeID, isCdn, rootDomain, regIpv4, regIpv6)
	serverParts = append(serverParts, "add="+addrPolicyForMain.add)
	serverParts = append(serverParts, addrPolicyForMain.extra...)
	node.Server = strings.Join(serverParts, "&")
	set("server", node.Server)

	// sort 由协议类型派生
	if protoCfg := presetsGetProtocolConfig(v2Name, mainProtocol); protoCfg != nil {
		node.Sort = int32(presetsGetSortForType(protoCfg.Type))
		set("sort", node.Sort)
	}

	node.Type = 1 // Enable
	set("type", node.Type)
	node.CustomRSS = 1 // Enable subscription
	set("custom_rss", node.CustomRSS)

	// node_class (阶梯等级引擎): 上报 node_level 优先 (显式 0 = 免费/全员档);
	// 否则上报了 cost 按 cost 派生; 均未上报回退默认 1 (fail-closed)
	hasNodeLevel := false
	if raw, ok := params["node_level"]; ok && len(raw) > 0 && raw[0] != "" {
		hasNodeLevel = true
		node.NodeClass = int32(phpInt(raw[0]))
	} else if hasParam(params, "node_cost") {
		node.NodeClass = int32(math.Max(1, math.Floor(nodeCost)))
	} else {
		node.NodeClass = 1
	}
	set("node_class", node.NodeClass)
	if !hasNodeLevel && !hasParam(params, "node_cost") {
		warnIfNoNodeClass(&node)
	}

	// ── 9. 心跳 ──
	node.NodeHeartbeat = timex.Unix()
	set("node_heartbeat", node.NodeHeartbeat)

	// node_ids — 接收节点上报的节点组 ID 列表 (末尾统一 seal 覆盖)
	if phpNotEmpty(params.Get("node_ids")) {
		node.NodeIDs = params.Get("node_ids")
		set("node_ids", node.NodeIDs)
	}

	// IP 身份校验只在 reportStatus 做; register 只负责注册, 不污染 node_sort/info
	if err := database.DB.Model(&model.Node{}).Where("id = ?", nodeID).Updates(dirty).Error; err != nil {
		return nil, fmt.Errorf("registerNode save failed: %w", err)
	}

	// reset_day 变更审计 (NodeResetDayAuditLogger intend 'api.node.register' 的落库点)
	if node.TrafficResetDay != oldResetDay {
		nodeRecordResetDayAudit(nodeID, node.Name, int64(oldResetDay), int64(node.TrafficResetDay), "api.node.register")
	}

	// 增量同步 bump: 等级/分组实际变化时全表打戳
	if node.NodeClass != oldNodeClass || node.NodeGroup != oldNodeGroup {
		if err := nodeBumpUserSyncStamp(); err != nil {
			return nil, fmt.Errorf("registerNode bump user stamp failed: %w", err)
		}
	}

	// ── 10. Fission matrix — clone 节点裂变 ──
	isDualStack := regIpv4 != "" && regIpv6 != ""
	cloneNodeIds, err := createCloneNodes(cloneNodeArgs{
		mainNodeID:  nodeID,
		v2Name:      v2Name,
		protocols:   protocols,
		nodeRxtx:    nodeRxtx,
		rootDomain:  rootDomain,
		ipv4:        regIpv4,
		ipv6:        regIpv6,
		mainClass:   node.NodeClass,
		nodeGroup:   nodeGroup,
		isDualStack: isDualStack,
		isCdn:       isCdn,
		nodeAddress: nodeAddress,
		nodePort:    nodePort,
		paths:       paths,
		xhttpVerify: xhttpVerify,
		realityPriv: realityPriv,
		realityPub:  realityPub,
		realitySid:  realitySid,
		xhttpSplit:  xhttpSplit,
		dlHost:      dlHost,
		ech:         ech,
		main:        &node,
	})
	if err != nil {
		return nil, err
	}

	// ── 11. Seal node_ids on main node ──
	allNodeIds := make([]string, 0, len(cloneNodeIds)+1)
	allNodeIds = append(allNodeIds, strconv.FormatInt(nodeID, 10))
	cloneIDStrs := make([]string, len(cloneNodeIds))
	for i, id := range cloneNodeIds {
		cloneIDStrs[i] = strconv.FormatInt(id, 10)
	}
	allNodeIds = append(allNodeIds, cloneIDStrs...)
	nodeIdsString := strings.Join(allNodeIds, ",")
	node.NodeIDs = nodeIdsString
	// Fix #5: register 重申身份/协议组/解锁, 配置指纹必然失效 → 置脏, 由心跳惰性重算
	node.ConfigHash = ""
	if err := database.DB.Model(&model.Node{}).Where("id = ?", nodeID).
		Updates(map[string]any{"node_ids": nodeIdsString, "config_hash": ""}).Error; err != nil {
		return nil, fmt.Errorf("registerNode seal node_ids failed: %w", err)
	}

	// 节点重置安全网: register 完成后仅当无基准 (=0) 时盖基准时间 (= 注册时刻);
	// 复用节点保留既有基准, 安全网窗口连续不中断
	monthlyEnsureBaseline(nodeID)

	// ── 12. Build response ──
	if cloneNodeIds == nil {
		cloneNodeIds = []int64{} // JSON 输出 [] 而非 null (对齐 PHP 空数组)
	}
	return map[string]any{
		"status":         "success",
		"node_id":        nodeID,
		"clone_node_ids": cloneNodeIds,
		"node_ids":       nodeIdsString,
		"root_domain":    rootDomain,
		"v2_name":        v2Name,
		"ip":             regIpv4,
		"ipv6":           regIpv6,
		"node_group":     nodeGroup,
	}, nil
}

// cloneNodeArgs createCloneNodes 入参 (对齐 PHP 私有方法签名)
type cloneNodeArgs struct {
	mainNodeID         int64
	v2Name             string
	protocols          []string
	nodeRxtx           string
	rootDomain         string
	ipv4, ipv6         string
	mainClass          int32
	nodeGroup          int32
	isDualStack, isCdn bool
	nodeAddress        string
	nodePort           int64
	paths              map[string]string
	xhttpVerify        string
	realityPriv        string
	realityPub         string
	realitySid         string
	xhttpSplit         bool
	dlHost             string
	ech                string
	main               *model.Node // 主节点内存态 (name/country_code/unlock/bandwidth 已更新)
}

// createCloneNodes 对齐 PHP createCloneNodes (fission matrix):
//
//	单栈: protocolCount 个节点 (含 main); 双栈: protocolCount*2 个 (含 main),
//	前半 IPv4 clone (add=域名池轮换), 后半 IPv6 clone (add=裸 ipv6);
//	所有 clone 的 node_ip 复制 main 双栈, host/sni 照搬 main host。
//
//	clone 复用 (同 main 的旧 clone, id≥100 且心跳>0, id ASC) —— 同一物理机重装
//	场景 clone 槽位稳定; 新建 max(id)+1 (≥100); 并发撞主键时跳过该 clone
//	(下次 register 补齐, 站长确认不视为 bug)。
//	 surplus 旧 clone: 先 DeleteNodeDNSRecords 再降级 (is_clone=0, type=0)。
func createCloneNodes(args cloneNodeArgs) ([]int64, error) {
	protocolCount := len(args.protocols)
	totalClones := protocolCount - 1
	if args.isDualStack {
		totalClones = protocolCount*2 - 1
	}

	created := []int64{}

	for i := 1; i <= totalClones; i++ {
		protoIndex := i % protocolCount
		cloneProto := args.protocols[protoIndex]

		// IP 栈: 双栈时后半为 IPv6 半区
		isIPv6Node := args.isDualStack && i >= protocolCount

		// clone 等级: rand(mainClass, max(mainClass, 配置上限)); main 高于上限时退化为自身
		cloneClassMax := int64(nodeConfigCloneClassMax())
		lo := int64(args.mainClass)
		hi := lo
		if cloneClassMax > lo {
			hi = cloneClassMax
		}
		cloneClass := lo + int64(randIntn(int(hi-lo+1)))

		// ① 复用同 main 的旧 clone (保留区 id<100 除外)
		var clone model.Node
		reused := false
		oldResetDay := int32(0)
		q := database.DB.Where("is_clone = ?", args.mainNodeID).
			Where("id >= ?", nodeMinAllocatableID).
			Where("node_heartbeat > ?", 0)
		if len(created) > 0 {
			q = q.Where("id NOT IN ?", created)
		}
		if err := q.Order("id asc").Limit(1).Find(&clone).Error; err != nil {
			return nil, fmt.Errorf("createCloneNodes reuse query failed: %w", err)
		}
		if clone.ID != 0 {
			reused = true
			oldResetDay = clone.TrafficResetDay
			ResetNodeToDefaults(&clone, "")
		}

		// ② 新建 (id >= 保留区上界; 跨机死行回收已删除 —— id 唯一性不受破坏)
		if !reused {
			newID, err := nodeNextID()
			if err != nil {
				return nil, err
			}
			clone = model.Node{ID: int32(newID)}
			ResetNodeToDefaults(&clone, "")
		}

		clone.Name = args.main.Name
		clone.Type = 1
		clone.IsClone = int32(args.mainNodeID)
		clone.CustomRSS = 1
		clone.NodeClass = int32(cloneClass)
		clone.NodeGroup = args.nodeGroup
		clone.CountryCode = args.main.CountryCode
		// node_unlock: clone = 同一物理节点的协议分身, 解锁能力与主节点相同
		clone.NodeUnlock = args.main.NodeUnlock
		clone.NodeBandwidth = phpFloatToString(phpFloat(args.main.NodeBandwidth) + float64(cloneClass))
		clone.NodeHeartbeat = timex.Unix()

		// clone server 串 — host/sni 完全照搬 main (s{main}.{rootDomain})
		cloneServerParts := []string{}
		cloneServerStr := presetsBuildServerString(cloneProto, false, args.v2Name, args.nodeAddress, args.paths)
		if args.nodePort != 443 {
			cloneServerStr = nodePortRe.ReplaceAllString(cloneServerStr, "port="+strconv.FormatInt(args.nodePort, 10))
		}
		cloneServerParts = append(cloneServerParts, cloneServerStr)
		cloneServerParts = append(cloneServerParts, "v2_name="+args.v2Name)
		cloneServerParts = append(cloneServerParts, "node_rxtx="+args.nodeRxtx)
		cloneServerParts = append(cloneServerParts, "node_port="+strconv.FormatInt(args.nodePort, 10))
		cloneServerParts = append(cloneServerParts, presetsBuildPathsParam(args.paths)...)
		cloneServerParts = append(cloneServerParts, presetsBuildXhttpVerifyParam(args.xhttpVerify)...)
		cloneServerParts = append(cloneServerParts, presetsBuildRealityParam(args.realityPriv, args.realityPub, args.realitySid)...)
		// xhttp-split: dl_host 照搬 main; dl_add 按 fission 半区重算 (IPv6 clone 用 ipv6)
		if args.xhttpSplit {
			cloneDlAdd := args.ipv4
			if cloneDlAdd == "" {
				cloneDlAdd = args.ipv6
			}
			if isIPv6Node {
				cloneDlAdd = args.ipv6
			}
			cloneServerParts = append(cloneServerParts, presetsBuildXhttpSplitParam(args.dlHost, cloneDlAdd)...)
		}
		// ECH: CDN 集群 xhttp clone 复用主节点 ECH; hy2 clone 不经 CF 不带
		if cloneProto != "hy2" {
			cloneServerParts = append(cloneServerParts, presetsBuildEchParam(args.ech)...)
		}
		// padding 混淆键: CDN 集群 xhttp clone 复用主节点键 (种子=主节点 ID, 幂等)
		if args.isCdn && cloneProto != "hy2" {
			cloneServerParts = append(cloneServerParts, presetsBuildXobfsParam(map[string]string{
				"key":    presetsPickXobfsKey(args.mainNodeID),
				"header": presetsPickXobfsHeader(args.mainNodeID),
			})...)
		}
		// 地址策略: CDN→橙云随机域名; 非CDN IPv4 clone→域名池轮换; IPv6 clone→裸 ipv6;
		// CDN 混合模式 hy2 特判绕过 CDN 直连 (CF 不代理 UDP)。cloneId 兼作轮换 offset。
		cloneAddr := addressPolicyResolveForClone(int64(clone.ID), args.isCdn, isIPv6Node,
			args.ipv4, args.ipv6, int64(clone.ID), cloneProto)
		cloneServerParts = append(cloneServerParts, "add="+cloneAddr.add)
		cloneServerParts = append(cloneServerParts, cloneAddr.extra...)
		clone.Server = strings.Join(cloneServerParts, "&")

		if protoCfg := presetsGetProtocolConfig(args.v2Name, cloneProto); protoCfg != nil {
			clone.Sort = int32(presetsGetSortForType(protoCfg.Type))
		}

		// clone node_ip — 完全复制 main 双栈 (DNS 层按需取其一)
		clone.NodeIP = "ip=" + args.ipv4 + "&ipv6=" + args.ipv6

		// 落库 (含并发撞主键容错: 跳过该 clone, 下次 register 补齐)
		cols := nodeResetColumns(&clone)
		cols["country_code"] = clone.CountryCode
		if reused {
			if err := database.DB.Model(&model.Node{}).Where("id = ?", clone.ID).Updates(cols).Error; err != nil {
				log.Printf("[NodeApi] createCloneNodes update clone failed (skip): main=%d clone=%d err=%v",
					args.mainNodeID, clone.ID, err)
				continue
			}
		} else {
			cols["id"] = int64(clone.ID)
			if err := nodeInsertRow(cols); err != nil {
				log.Printf("[NodeApi] createCloneNodes insert clone failed (skip): main=%d err=%v", args.mainNodeID, err)
				continue
			}
		}
		// reset_day provisioning 归零审计 (复用路径; 新建行无旧值不审计)
		if reused && oldResetDay != clone.TrafficResetDay {
			nodeRecordResetDayAudit(int64(clone.ID), clone.Name, int64(oldResetDay), int64(clone.TrafficResetDay),
				"node.provisioning.resetToDefaults")
		}
		created = append(created, int64(clone.ID))
	}

	// Downgrade surplus old clones (先清 DNS, 再降级 is_clone=0/type=0)
	surplusQuery := database.DB.Model(&model.Node{}).Where("is_clone = ?", args.mainNodeID)
	if len(created) > 0 {
		surplusQuery = surplusQuery.Where("id NOT IN ?", created)
	}
	var allCloneIds []int64
	if err := surplusQuery.Pluck("id", &allCloneIds).Error; err != nil {
		return nil, fmt.Errorf("createCloneNodes surplus query failed: %w", err)
	}
	if len(allCloneIds) > 0 {
		for _, oldCloneID := range allCloneIds {
			DeleteNodeDNSRecords(oldCloneID)
		}
		if err := database.DB.Model(&model.Node{}).Where("id IN ?", allCloneIds).
			Updates(map[string]any{"is_clone": 0, "type": 0}).Error; err != nil {
			return nil, fmt.Errorf("createCloneNodes downgrade failed: %w", err)
		}
	}

	return created, nil
}

// poolContainsDomain 域名是否在池内 (对齐 PHP isset($pool[$rootDomain]))
func poolContainsDomain(entries []DomainEntry, domain string) bool {
	for _, e := range entries {
		if e.Domain == domain {
			return true
		}
	}
	return false
}

// hasParam PHP isset($params[key]) 对应物 (区分「未上报」与「上报空串」)
func hasParam(params url.Values, key string) bool {
	_, ok := params[key]
	return ok
}

// ─────────────────────────────────────────────────────────────────────────────
// 私有辅助 (对齐 PHP NodeApiService 私有方法)
// ─────────────────────────────────────────────────────────────────────────────

// normalizeReportedIP 对齐 PHP normalizeReportedIp: FILTER_VALIDATE_IP 白名单,
// 合法 IP 原样返回 (已 trim); 非法/缺失返回 "" (按未上报处理)。
func normalizeReportedIP(raw string, ipv6 bool) string {
	ip := strings.TrimSpace(raw)
	if ip == "" {
		return ""
	}
	if ipv6 {
		if isValidIPv6(ip) {
			return ip
		}
		return ""
	}
	if isValidIPv4(ip) {
		return ip
	}
	return ""
}

// buildNodeIpString 对齐 PHP buildNodeIpString。
func buildNodeIpString(ipv4, ipv6 string) string {
	return "ip=" + ipv4 + "&ipv6=" + ipv6
}

// extractV2NameFromServer 对齐 PHP extractV2NameFromServer:
// 从 server 串提取 v2_name (复用节点未上报 v2_name 时沿用, 协议不变)。
func extractV2NameFromServer(serverStr string) string {
	if serverStr == "" {
		return ""
	}
	q, err := url.ParseQuery(serverStr)
	if err != nil {
		return ""
	}
	return q.Get("v2_name")
}

// needsPqFallback 对齐 PHP needsPqFallback: v2_name 属 PQ 组 (xhttp-pq /
// vision-reality-pq) 且 node_openssl < 3.5 / 缺失 / unknown → 回退默认。
func needsPqFallback(v2Name, reportedOpenssl string) bool {
	if v2Name != "xhttp-pq" && v2Name != "vision-reality-pq" {
		return false
	}
	if reportedOpenssl == "" || reportedOpenssl == "unknown" {
		return true
	}
	parts := strings.Split(reportedOpenssl, ".")
	major := phpInt(parts[0])
	minor := int64(0)
	if len(parts) > 1 {
		minor = phpInt(parts[1])
	}
	return !(major > 3 || (major == 3 && minor >= 5))
}

// sanitizeDisplayText 对齐 PHP sanitizeDisplayText: 展示型文本白名单净化
// (strip_tags → 剔除 < > " ' ` & → 按字符截断), 防存储型 XSS 与 node_ip 键注入。
func sanitizeDisplayText(raw string, maxLen int) string {
	text := stripTags(strings.TrimSpace(raw))
	text = strings.NewReplacer("<", "", ">", "", `"`, "", "'", "", "`", "", "&", "").Replace(text)
	r := []rune(text)
	if len(r) > maxLen {
		return string(r[:maxLen])
	}
	return text
}

// stripTags 复刻 PHP strip_tags: 删除 <..> 段, 未闭合 '<' 吞到串尾。
func stripTags(s string) string {
	for {
		i := strings.IndexByte(s, '<')
		if i < 0 {
			return s
		}
		j := strings.IndexByte(s[i:], '>')
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+1:]
	}
}

// normalizeUnlockServices 对齐 PHP normalizeUnlockServices: register 上报的
// node_unlock 归一化为存储格式 (Key=Yes&Key=No&...), 兼容 "openai,netflix" /
// "Netflix=Yes&OpenAI=No" 及混排; 未知服务名丢弃; 空串 = 无有效服务 (调用方保持既有值)。
func normalizeUnlockServices(raw string) string {
	tokens := unlockSplitRe.Split(strings.TrimSpace(raw), -1)
	parts := map[string]string{}
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		value := "Yes"
		if eq := strings.IndexByte(token, '='); eq >= 0 {
			value = strings.TrimSpace(token[eq+1:])
			token = strings.TrimSpace(token[:eq])
		}
		storageKey, ok := nodeUnlockAliasToStorageKey[strings.ToLower(token)]
		if !ok {
			continue // 白名单外服务名丢弃
		}
		parts[storageKey] = normalizeUnlockValue(value)
	}
	if len(parts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(parts))
	for k := range parts {
		keys = append(keys, k)
	}
	sort.Strings(keys) // 键序固定, 输出稳定 (PHP 为插入序, 等价可读)
	serialized := make([]string, len(keys))
	for i, k := range keys {
		serialized[i] = k + "=" + parts[k]
	}
	return strings.Join(serialized, "&")
}

// normalizeUnlockValue 对齐 PHP NodeUnlock::normalizeValue (写入端归一化):
// no/false/0/off 前缀 → No; failed/error/timeout/null/unknown 前缀 → Unknown;
// 其余非空值 → Yes (存储枚举恒为三个白名单词)。
func normalizeUnlockValue(value string) string {
	value = strings.TrimSpace(value)
	if nodeUnlockNoPrefix.MatchString(value) {
		return "No"
	}
	if nodeUnlockFailedPrefix.MatchString(value) {
		return "Unknown"
	}
	return "Yes"
}

// warnIfNoNodeClass 对齐 PHP warnIfNoNodeClass: register 未上报 node_level 且
// 未上报 node_cost → node_class 回退默认 1, 提示运维补配置。
// 节流: per-node-per-day Redis 去重; Redis 不可用放弃本次通知 (fail-safe)。
func warnIfNoNodeClass(node *model.Node) {
	key := fmt.Sprintf("ssp:node:%d:node_class_warn_%s", node.ID, timex.Ymd())
	notify.DedupNotify(key, 24*time.Hour, func() {
		name := node.Name
		if name == "" {
			name = "#" + strconv.FormatInt(int64(node.ID), 10)
		}
		notify.Warning("节点等级缺失",
			"节点 #"+strconv.FormatInt(int64(node.ID), 10)+" ("+name+") register 未上报 node_level 且未上报 node_cost, "+
				"node_class 已回退默认 1 (最低付费档, 免费用户不可见)。请补配置 node_level 或 node_cost "+
				"(若节点应为免费/全员档, 上报 node_level=0 即可)。")
	})
}

// warnIfInvalidRxtx 对齐 PHP warnIfInvalidRxtx: node_rxtx 非法值告警
// (已回退默认 rxtx 继续计费, 但节点端配置错误必须让运维看到)。
func warnIfInvalidRxtx(node *model.Node, rawValue string) {
	key := fmt.Sprintf("ssp:node:%d:node_rxtx_warn_%s", node.ID, timex.Ymd())
	notify.DedupNotify(key, 24*time.Hour, func() {
		shown := sanitizeDisplayText(rawValue, 32)
		notify.Warning("节点计费口径非法",
			"节点 #"+strconv.FormatInt(int64(node.ID), 10)+" register 上报 node_rxtx=\""+shown+"\" 不在 tx/rx/rxtx "+
				"枚举内, 已回退默认 rxtx (rx/tx 均值计费)。请检查节点端配置, 非法值会让流量统计"+
				"口径与预期不符。")
	})
}
