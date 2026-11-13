// 节点 API 状态族 —— 对齐 PHP app/Services/NodeApiService.php 的 reportStatus /
// updateUnlock / editNode 忠实移植。
//
// reportStatus 是 ss_node.traffic_used 的唯一权威写入方 (NIC 口径
// traffic_raw_total 基线 + node_rxtx 计费口径 + 基线保护四分支 + 月度派生刷新 +
// 上报者 IP 比对强制拦截), 语义红线不可漂移。
package service

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/timex"
)

// monitorRe 对齐 PHP monitor 白名单校验 /^[\d.,\-\/]+$/
// (vnstat 形态 "v1,v2,...,v7,M-D"; 拒绝其他字符防 node.status 存储型 XSS)
var monitorRe = regexp.MustCompile(`^[\d.,\-/]+$`)

// ReportStatus 对齐 PHP reportStatus (POST /api/node/status 后端):
// 永远返回完整响应 map (业务错误也编进 map), 不返回 error。
//
// reporterIP 为服务端注入的上报者真实连接 IP (对齐 PHP 控制器
// $params['reporter_ip'] = $_SERVER['REMOTE_ADDR'] 的覆盖语义 —— 节点自报的
// reporter_ip 参数不可信, 一律以连接 IP 为准)。
func ReportStatus(nodeID int64, params url.Values, reporterIP string) map[string]any {
	if database.DB == nil {
		return map[string]any{"status": "error", "message": "internal_error"}
	}
	var node model.Node
	if err := database.DB.First(&node, nodeID).Error; err != nil {
		return map[string]any{"status": "error", "message": "node_not_found"}
	}

	// ── 安全闸: 上报者 IP 与记录的 node_ip 比对 ──
	// 高频 status 是流量写入的唯一入口, 必须校验, 否则同一 node_id 被多台机上报
	// 会让 traffic_raw_total 互相错位、traffic_used 计数爆炸。上报 IP 与记录的
	// ipv4/ipv6 均不一致 → node_sort=-999 (排序隔离) + info 记录异常 IP;
	// 默认 (enforce_node_reporter_ip_check=true) 直接丢弃本次上报 (仅持久化标记后
	// 返回, 不写流量/心跳/monitor), 从根源阻止灌爆。
	reporterIp := strings.TrimSpace(reporterIP)
	nodeIpParsed := url.Values{}
	if node.NodeIP != "" {
		if q, err := url.ParseQuery(node.NodeIP); err == nil {
			nodeIpParsed = q
		}
	}
	recordedIpv4 := nodeIpParsed.Get("ip")
	recordedIpv6 := nodeIpParsed.Get("ipv6")
	ipMismatch := false
	// 仅当存在记录 IP 可比对, 且上报 IP 与 ipv4/ipv6 都不一致时才触发
	if reporterIp != "" &&
		(recordedIpv4 != "" || recordedIpv6 != "") &&
		reporterIp != recordedIpv4 && reporterIp != recordedIpv6 {
		ipMismatch = true
		node.NodeSort = -999
		// info 字段 varchar(128), 按字节截断 (对齐 PHP substr) 防 ipv6 过长截断报错
		mismatchInfo := "IP异常: 上报者=" + reporterIp +
			" 记录ip=" + recordedIpv4 +
			" 记录ipv6=" + recordedIpv6
		if len(mismatchInfo) > 128 {
			mismatchInfo = mismatchInfo[:125] + "..."
		}
		node.Info = mismatchInfo
		log.Printf("[NodeApi] reportStatus IP_MISMATCH: node_id=%d reporter='%s' recorded ip='%s' ipv6='%s'",
			nodeID, reporterIp, recordedIpv4, recordedIpv6)
	}

	// 强制拦截: IP 不匹配时仅持久化 node_sort=-999 / info 标记后直接返回 ——
	// 不累加 traffic_used、不更新 traffic_raw_total、不刷新 node_heartbeat、
	// 不写入 monitor。冒充机既无法灌爆流量计数, 也无法借 monitor 注入载荷,
	// 更无法用心跳把节点保持在"在线"状态。traffic_raw_total 保留上次真实值。
	if ipMismatch && nodeEnforceReporterIPCheck() {
		if err := database.DB.Model(&model.Node{}).Where("id = ?", nodeID).
			Updates(map[string]any{"node_sort": node.NodeSort, "info": node.Info}).Error; err != nil {
			log.Printf("[NodeApi] reportStatus persist mismatch marks failed node_id=%d: %v", nodeID, err)
		}
		return map[string]any{
			"status":      "rejected",
			"message":     "reporter_ip_mismatch",
			"node_status": node.Type,
		}
	}

	// ── 1+2. NIC 原始读数 → 计费口径 → 基线保护增量累加 ──
	// (billRawTotal + addIncrement 四分支; enforce 拦截路径已早返回, 这里均为可信上报)
	billingMode := node.NodeRxTx
	if billingMode == "" {
		billingMode = "tx"
	}
	rawTx, rawRx := 0.0, 0.0
	if raw, ok := params["raw_tx"]; ok && len(raw) > 0 {
		rawTx = phpFloat(raw[0])
	}
	if raw, ok := params["raw_rx"]; ok && len(raw) > 0 {
		rawRx = phpFloat(raw[0])
	}
	rawTotal := monthlyBillRawTotal(rawTx, rawRx, billingMode)
	increment := monthlyAddIncrement(&node, rawTotal)

	oldTrafficUsed := node.TrafficUsed - increment // 审计基线 (addIncrement 前的值)

	dirty := map[string]any{}
	set := func(col string, v any) { dirty[col] = v }
	set("traffic_used", node.TrafficUsed)
	if node.TrafficUsed != oldTrafficUsed {
		// debug 级审计 (高频热路径): 与 PHP Logger debug 一致, 排障时打开
		log.Printf("[NodeApi] [traffic_used_audit] node=#%d traffic_used %d->%d src=node.reportStatus.addIncrement inc=%d",
			node.ID, oldTrafficUsed, node.TrafficUsed, increment)
	}
	set("traffic_raw_total", node.TrafficRawTotal)

	// 接收 node_bandwidth（Mbps，后端上报参数名就是 node_bandwidth）
	// 注意: 这里是 isset 语义 (空串也写 0), 与 register 的 isset&&!=="" 口径不同
	if raw, ok := params["node_bandwidth"]; ok && len(raw) > 0 {
		node.NodeBandwidth = phpFloatToString(phpFloat(raw[0]))
		set("node_bandwidth", node.NodeBandwidth)
	}

	// 接收 server_uptime (isset 语义)
	if raw, ok := params["server_uptime"]; ok && len(raw) > 0 {
		node.ServerUptime = int32(phpInt(raw[0]))
		set("server_uptime", node.ServerUptime)
	}

	// monitor (nodeMonitor.json 实时快照 → node.status): 格式白名单校验 +
	// 按字符 (非字节) 截断 128, 防切断多字节 UTF-8
	if raw, ok := params["monitor"]; ok && len(raw) > 0 {
		monitor := raw[0]
		if monitorRe.MatchString(monitor) {
			if r := []rune(monitor); len(r) > 128 {
				monitor = string(r[:128])
			}
			node.Status = monitor
			set("status", node.Status)
		}
	}

	// 重算周期派生字段 (left + used_daily + left_daily; reset_day==0 触发配置告警)
	monthlyApplyDerived(&node)
	set("traffic_left", node.TrafficLeft)
	set("traffic_used_daily", node.TrafficUsedDaily)
	set("traffic_left_daily", node.TrafficLeftDaily)

	// ── 5. custom_rss 判定 (上报时计算) ──
	if node.TrafficLimit > 0 && node.TrafficUsedDaily > node.TrafficLeftDaily {
		node.CustomRSS = 0
	} else if node.Type == 1 {
		node.CustomRSS = 1
	}
	set("custom_rss", node.CustomRSS)

	// ── 6. type 判定: 剩余流量不足 64G 则禁用, 否则启用 ──
	if monthlyIsExhausted(&node, 64*1024*1024*1024) {
		node.Type = 0
	} else {
		node.Type = 1
	}
	set("type", node.Type)

	node.NodeHeartbeat = timex.Unix()
	set("node_heartbeat", node.NodeHeartbeat)

	// 非 enforce 模式的 IP 不匹配标记随本次 save 一并持久化 (仅标记 + 继续处理上报)
	if ipMismatch {
		set("node_sort", node.NodeSort)
		set("info", node.Info)
	}

	if err := database.DB.Model(&model.Node{}).Where("id = ?", nodeID).Updates(dirty).Error; err != nil {
		log.Printf("[NodeApi] reportStatus save failed node_id=%d: %v", nodeID, err)
		return map[string]any{"status": "error", "message": "internal_error"}
	}

	// Sync heartbeat, type, custom_rss, node_bandwidth to clone nodes
	if err := database.DB.Model(&model.Node{}).Where("is_clone = ?", nodeID).Updates(map[string]any{
		"node_heartbeat": node.NodeHeartbeat,
		"type":           node.Type,
		"custom_rss":     node.CustomRSS,
		"node_bandwidth": node.NodeBandwidth,
	}).Error; err != nil {
		log.Printf("[NodeApi] reportStatus sync clones failed node_id=%d: %v", nodeID, err)
	}

	// Write traffic log (best-effort; 列名对齐 PHP TrafficLog 模型 → user_traffic_log)
	if err := database.DB.Exec(
		"INSERT INTO user_traffic_log (user_id, u, d, node_id, rate, traffic, log_time) VALUES (0, 0, ?, ?, 0, ?, ?)",
		increment, nodeID, strconv.FormatInt(node.TrafficUsed, 10), timex.Unix(),
	).Error; err != nil {
		log.Printf("[NodeApi] reportStatus traffic log failed node_id=%d: %v", nodeID, err)
	}

	// Write online log (best-effort)
	if err := database.DB.Exec(
		"INSERT INTO ss_node_online_log (node_id, online_user, log_time) VALUES (?, 0, ?)",
		nodeID, timex.Unix(),
	).Error; err != nil {
		log.Printf("[NodeApi] reportStatus online log failed node_id=%d: %v", nodeID, err)
	}

	// ── Fix #5: 配置下发闭环 ──
	// 心跳响应携带面板侧当前 xray 配置指纹; 空缓存列惰性重算一次并回填,
	// 高频心跳不全量重渲染。
	configHash := node.ConfigHash
	if configHash == "" {
		configHash = NodeComputeConfigHash(nodeID)
		if configHash != "" {
			if err := database.DB.Model(&model.Node{}).Where("id = ?", nodeID).
				Update("config_hash", configHash).Error; err != nil {
				log.Printf("[NodeApi] reportStatus cache config_hash failed node_id=%d: %v", nodeID, err)
			}
		}
	}
	reportedHash := strings.TrimSpace(params.Get("config_hash"))

	result := map[string]any{
		"node_status":           node.Type,
		"traffic_used":          node.TrafficUsed,
		"traffic_left":          node.TrafficLeft,
		"traffic_used_daily":    node.TrafficUsedDaily,
		"traffic_left_daily":    node.TrafficLeftDaily,
		"traffic_max_day_value": node.TrafficMaxDayValue,
		"config_hash":           configHash,
	}
	// 仅当两侧指纹均非空才给出 stale 判定 (面板生成失败时不误导节点首刷)
	if reportedHash != "" && configHash != "" {
		result["config_stale"] = reportedHash != configHash
	}
	return result
}

// nodeEnforceReporterIPCheck 对齐 PHP enforceReporterIpCheck:
// config['enforce_node_reporter_ip_check'] 默认 true (拦截模式); 置 false 退回
// "仅标记"模式 (合法换 IP/换 NAT 节点误伤时的逃生口)。
func nodeEnforceReporterIPCheck() bool {
	if config.GlobalConfig == nil {
		return true
	}
	return config.GlobalConfig.Node.EnforceIPCheck()
}

// ─────────────────────────────────────────────────────────────────────────────
// updateUnlock (POST /api/node/unlock_check)
// ─────────────────────────────────────────────────────────────────────────────

// unlockParamToStorageKey 对齐 PHP updateUnlock 的参数映射表
var unlockParamToStorageKey = map[string]string{
	"unlock_netflix":        "Netflix",
	"unlock_chatgpt":        "OpenAI",
	"unlock_disney":         "Disney",
	"unlock_bing":           "Bing",
	"unlock_claude":         "Claude",
	"unlock_gemini":         "Gemini",
	"unlock_bahamut":        "Bahamut",
	"unlock_mewatch":        "MeWatch",
	"unlock_tiktok":         "TikTok",
	"unlock_bilibili":       "Bilibili",
	"unlock_iqiyi":          "iQIYI",
	"unlock_google_scholar": "Google_Scholar",
	"unlock_notebooklm":     "NotebookLM",
}

// UpdateUnlock 对齐 PHP updateUnlock (独立于 register 的解锁检测上报):
// 写入是「合并」而非「替换」(unlockCheck.sh 只上报非空结果, 整串覆盖会把某服务
// 既有值静默抹掉 → 远程解锁路由不再注入)。返回完整响应 map; err 仅内部异常。
func UpdateUnlock(nodeID int64, params url.Values) (map[string]any, error) {
	if database.DB == nil {
		return nil, errNodeAPINilDB
	}
	var node model.Node
	if err := database.DB.First(&node, nodeID).Error; err != nil {
		return map[string]any{"status": "error", "message": "node_not_found"}, nil
	}

	// 既有值作兑底基线, 本次上报覆盖同名服务 (基线恒为 Key=Yes/No/Unknown)
	merged := map[string]string{}
	if node.NodeUnlock != "" {
		if q, err := url.ParseQuery(node.NodeUnlock); err == nil {
			for k, vs := range q {
				v := ""
				if len(vs) > 0 {
					v = vs[0]
				}
				merged[k] = v
			}
		}
	}
	hasNew := false
	updatedParams := []string{}
	// 参数表固定顺序遍历 (对齐 PHP foreach $unlockParamToStorageKey)
	for _, param := range unlockParamOrderedParams {
		storageKey := unlockParamToStorageKey[param]
		if raw := params.Get(param); raw != "" { // isset && !== ""
			updatedParams = append(updatedParams, param)
			// 归一化前先判空: 纯空格与字面空串同口径跳过, 不落入 Yes 兜底分支
			value := strings.TrimSpace(raw)
			if value == "" {
				continue
			}
			// 值归一化为 Yes/No/Unknown 白名单 (第三方检测脚本输出, 防存储型 XSS)
			merged[storageKey] = normalizeUnlockValue(value)
			hasNew = true
		}
	}

	if !hasNew && len(merged) == 0 {
		return map[string]any{"status": "success", "message": "no_data"}, nil
	}
	if hasNew {
		// ksort: 键序固定, 输出稳定
		keys := make([]string, 0, len(merged))
		for k := range merged {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		serialized := make([]string, len(keys))
		for i, k := range keys {
			serialized[i] = k + "=" + merged[k]
		}
		node.NodeUnlock = strings.Join(serialized, "&")
		// Fix #5: 解锁数据变更 → 配置指纹失效, 置脏待心跳重算
		if err := database.DB.Model(&model.Node{}).Where("id = ?", nodeID).
			Updates(map[string]any{"node_unlock": node.NodeUnlock, "config_hash": ""}).Error; err != nil {
			return nil, fmt.Errorf("updateUnlock save failed: %w", err)
		}
	}

	// 主节点上报 → 同步所有 clone (同一物理节点的协议分身, 解锁能力相同);
	// clone 自身上报 (is_clone>0) 不传播
	if node.IsClone == 0 {
		if err := database.DB.Model(&model.Node{}).Where("is_clone = ?", node.ID).Updates(map[string]any{
			"node_unlock": node.NodeUnlock,
			"config_hash": "", // Fix #5: clone 配置同样受解锁数据影响, 同步置脏
		}).Error; err != nil {
			return nil, fmt.Errorf("updateUnlock sync clones failed: %w", err)
		}
	}

	return map[string]any{
		"status":  "success",
		"updated": updatedParams,
	}, nil
}

// unlockParamOrderedParams updateUnlock 参数表固定遍历顺序 (PHP 数组序)
var unlockParamOrderedParams = []string{
	"unlock_netflix",
	"unlock_chatgpt",
	"unlock_disney",
	"unlock_bing",
	"unlock_claude",
	"unlock_gemini",
	"unlock_bahamut",
	"unlock_mewatch",
	"unlock_tiktok",
	"unlock_bilibili",
	"unlock_iqiyi",
	"unlock_google_scholar",
	"unlock_notebooklm",
}

// ─────────────────────────────────────────────────────────────────────────────
// editNode (POST /api/node/edit, JSON 信封 {"id","params"})
// ─────────────────────────────────────────────────────────────────────────────

// nodeEditableField 对齐 PHP EDITABLE_FIELDS 条目
// (type: int|gb|float|enum|string + min/max/maxlen/values/db/derived/audit/sanitize)
type nodeEditableField struct {
	typeName string   // int | gb | float | enum | string
	min      *float64 // 数值下限 (越界 → ignored, reason=out_of_range)
	max      *float64 // 数值上限
	maxLen   int      // 字符串最大长度 (按字符计, 超长 → too_long)
	values   []string // enum 允许值
	db       string   // 实际列名 (缺省 = 字段名; node_info→info)
	derived  bool     // 动了需重算 applyDerived (left/daily)
	audit    string   // 动了需打 reset_day 审计来源标签
	sanitize bool     // 展示型文本, 写库前过 sanitizeDisplayText 白名单净化
}

func f64ptr(v float64) *float64 { return &v }

// nodeEditableFields 对齐 PHP NodeApiService::EDITABLE_FIELDS (向前兼容契约,
// 一旦发布即冻结; 单位即协议: traffic_limit=GB, traffic_used=GB,
// node_bandwidth=Mbps, node_cost=元)
var nodeEditableFields = map[string]nodeEditableField{
	"traffic_reset_day": {typeName: "int", min: f64ptr(0), max: f64ptr(31), derived: true, audit: "api.node.edit"},
	"traffic_limit":     {typeName: "gb", min: f64ptr(0), derived: true},
	// traffic_used: 绝对值校准 (GB→字节)。不动 NIC 基线 traffic_raw_total,
	// 下次 status 按真实增量继续累加, 不破坏状态机。
	"traffic_used":   {typeName: "gb", min: f64ptr(0), derived: true},
	"node_bandwidth": {typeName: "float", min: f64ptr(0)},
	"node_cost":      {typeName: "float", min: f64ptr(0)},
	"node_group":     {typeName: "int", min: f64ptr(0)},
	"node_class":     {typeName: "int", min: f64ptr(0)},
	"node_sort":      {typeName: "int"},
	"node_rxtx":      {typeName: "enum", values: []string{"tx", "rx", "rxtx"}},
	"node_info":      {typeName: "string", maxLen: 128, db: "info", sanitize: true},
}

// EditNode 对齐 PHP editNode + 控制器校验: 单/多字段编辑节点配置。
// 入参为 JSON 信封里的 params 对象; 返回完整响应 map:
// 成功 {"status":"success","node_id":N,"updated":[...],"ignored":[{field,reason}...]},
// 错误 {"status":"error","message":"no_valid_fields|node_not_found"...,"ignored":[...]}。
// 仅作用于该 id 节点本身, 不传播 clone (clone 由 register 统一管理)。
func EditNode(nodeID int64, params map[string]any) map[string]any {
	if database.DB == nil {
		return map[string]any{"status": "error", "message": "internal_error"}
	}
	var node model.Node
	if err := database.DB.First(&node, nodeID).Error; err != nil {
		return map[string]any{"status": "error", "message": "node_not_found"}
	}

	// 增量同步 bump 前置快照: node_class/node_group 变化影响全体用户资格
	oldNodeClass := node.NodeClass
	oldNodeGroup := node.NodeGroup
	oldResetDay := node.TrafficResetDay
	oldTrafficUsed := node.TrafficUsed

	updated := []string{}
	ignored := []map[string]any{}
	needDerived := false
	dirty := map[string]any{}

	for field, value := range params {
		spec, ok := nodeEditableFields[field]
		if !ok {
			ignored = append(ignored, map[string]any{"field": field, "reason": "unknown_field"})
			continue
		}

		dbField := spec.db
		if dbField == "" {
			dbField = field
		}

		casted, reason := nodeCastEditValue(spec, value)
		if reason != "" {
			ignored = append(ignored, map[string]any{"field": field, "reason": reason})
			continue
		}

		// 展示型文本 (sanitize 标记, 目前 node_info) 写库前过白名单净化
		if spec.sanitize {
			if s, isStr := casted.(string); isStr {
				maxLen := spec.maxLen
				if maxLen <= 0 {
					maxLen = 128
				}
				casted = sanitizeDisplayText(s, maxLen)
			}
		}

		// 赋值 (内存态 + 精确列; dbField 缺省 = 字段名, node_info→info)
		switch dbField {
		case "traffic_reset_day":
			node.TrafficResetDay = int32(casted.(int64))
			dirty[dbField] = casted
		case "traffic_limit":
			node.TrafficLimit = casted.(int64)
			dirty[dbField] = casted
		case "traffic_used":
			node.TrafficUsed = casted.(int64)
			dirty[dbField] = casted
			// traffic_used 校准审计 (与 admin traffic_used_calibrate 同口径)
			if node.TrafficUsed != oldTrafficUsed {
				log.Printf("[NodeApi] [traffic_used_audit] node=#%d traffic_used %d->%d src=api.node.edit",
					node.ID, oldTrafficUsed, node.TrafficUsed)
			}
		case "node_bandwidth":
			node.NodeBandwidth = phpFloatToString(casted.(float64)) // decimal 列
			dirty[dbField] = node.NodeBandwidth
		case "node_cost":
			node.NodeCost = int32(math.Round(casted.(float64))) // int 列
			dirty[dbField] = node.NodeCost
		case "node_group":
			node.NodeGroup = int32(casted.(int64))
			dirty[dbField] = casted
		case "node_class":
			node.NodeClass = int32(casted.(int64))
			dirty[dbField] = casted
		case "node_sort":
			node.NodeSort = int32(casted.(int64))
			dirty[dbField] = casted
		case "node_rxtx":
			node.NodeRxTx = casted.(string)
			dirty[dbField] = casted
		case "info":
			node.Info = casted.(string)
			dirty[dbField] = casted
		default:
			ignored = append(ignored, map[string]any{"field": field, "reason": "unknown_field"})
			continue
		}

		updated = append(updated, field)

		// reset_day 变更审计 (NodeResetDayAuditLogger::intend 的落库点)
		if spec.audit != "" && dbField == "traffic_reset_day" && node.TrafficResetDay != oldResetDay {
			nodeRecordResetDayAudit(nodeID, node.Name, int64(oldResetDay), int64(node.TrafficResetDay), spec.audit)
		}
		if spec.derived {
			needDerived = true
		}
	}

	if len(updated) == 0 {
		return map[string]any{
			"status":  "error",
			"message": "no_valid_fields",
			"ignored": ignored,
		}
	}

	// 仅当动了流量周期相关字段才重算派生 (left/daily), 避免无关编辑触发配置告警刷屏
	if needDerived {
		monthlyApplyDerived(&node)
		dirty["traffic_left"] = node.TrafficLeft
		dirty["traffic_used_daily"] = node.TrafficUsedDaily
		dirty["traffic_left_daily"] = node.TrafficLeftDaily
	}

	if err := database.DB.Model(&model.Node{}).Where("id = ?", nodeID).Updates(dirty).Error; err != nil {
		log.Printf("[NodeApi] editNode save failed node_id=%d: %v", nodeID, err)
		return map[string]any{"status": "error", "message": "internal_error"}
	}

	// 增量同步 bump: 等级/分组实际变化时全表打戳
	if node.NodeClass != oldNodeClass || node.NodeGroup != oldNodeGroup {
		if err := nodeBumpUserSyncStamp(); err != nil {
			log.Printf("[NodeApi] editNode bump user stamp failed: %v", err)
		}
	}

	if ignored == nil {
		ignored = []map[string]any{}
	}
	return map[string]any{
		"status":  "success",
		"node_id": nodeID,
		"updated": updated,
		"ignored": ignored,
	}
}

// nodeCastEditValue 对齐 PHP castEditValue: 按字段 schema 把上报值转为存储值并校验。
// 返回转换后的值; 失败时返回 (nil, reason), reason ∈ invalid_value/out_of_range/too_long。
func nodeCastEditValue(spec nodeEditableField, value any) (any, string) {
	switch spec.typeName {
	case "int":
		if !phpIsNumeric(value) {
			return nil, "invalid_value"
		}
		// PHP (int) 强转: 截断取整
		v := math.Trunc(phpFloat(phpStringValue(value)))
		if spec.min != nil && v < *spec.min {
			return nil, "out_of_range"
		}
		if spec.max != nil && v > *spec.max {
			return nil, "out_of_range"
		}
		return int64(v), ""
	case "gb": // GB → 字节
		if !phpIsNumeric(value) {
			return nil, "invalid_value"
		}
		v := phpFloat(phpStringValue(value))
		if v < 0 {
			return nil, "out_of_range"
		}
		return int64(math.Round(v * 1024 * 1024 * 1024)), ""
	case "float":
		if !phpIsNumeric(value) {
			return nil, "invalid_value"
		}
		v := phpFloat(phpStringValue(value))
		if spec.min != nil && v < *spec.min {
			return nil, "out_of_range"
		}
		if spec.max != nil && v > *spec.max {
			return nil, "out_of_range"
		}
		return v, ""
	case "enum":
		v := ""
		switch value.(type) {
		case string:
			v = value.(string)
		case float64, float32, int, int64, json.Number:
			v = phpStringValue(value)
		}
		found := false
		for _, allowed := range spec.values {
			if v == allowed {
				found = true
				break
			}
		}
		if !found {
			return nil, "invalid_value"
		}
		return v, ""
	case "string":
		var v string
		switch t := value.(type) {
		case string:
			v = t
		case float64, float32, int, int64, json.Number:
			v = phpStringValue(value)
		default:
			return nil, "invalid_value"
		}
		if spec.maxLen > 0 {
			if l := len([]rune(v)); l > spec.maxLen {
				return nil, "too_long"
			}
		}
		return v, ""
	default:
		return nil, "invalid_value"
	}
}

// nodeRecordResetDayAudit 对齐 PHP NodeResetDayAuditLogger::intend + Node::save
// 捕获点: traffic_reset_day 实际变更时落 node_reset_day_audit + 文件日志镜像。
// best-effort: 任何失败只记日志, 永不阻断主流程。
func nodeRecordResetDayAudit(nodeID int64, nodeName string, oldValue, newValue int64, source string) {
	if database.DB == nil {
		return
	}
	caller := "NodeApiService"
	err := database.DB.Exec(
		"INSERT INTO node_reset_day_audit (node_id, node_name, old_value, new_value, source, caller, context, backtrace, created_at) "+
			"VALUES (?, ?, ?, ?, ?, ?, 'api', '', ?)",
		nodeID, nodeName, oldValue, newValue, source, caller, timex.DateStrNow(),
	).Error
	if err != nil {
		log.Printf("[NodeApi] reset_day audit insert failed node_id=%d: %v", nodeID, err)
	}
	log.Printf("[NodeApi] [node_reset_day_audit] node=#%d reset_day %d->%d src=%s",
		nodeID, oldValue, newValue, source)
}
