// 节点默认值重置模块 —— 对齐 PHP app/Services/NodeDefaultsReset.php 的忠实移植。
//
// 职责: 把一个 Node 置为「全字段默认值」的干净态 (provisioning / 字段归零)。
// 与 register 的分工: apply_id (allocateNodeId) = provisioning (调本模块);
// register = recording (只记录上报字段, 不归零未上报字段; 唯一例外是
// resetOperationalMarks 的运维标记局部清零, 归零动作仍归本模块)。
//
// 关键约束 (勿破):
//  1. 写方法只改内存字段, 不落库 —— 调用方统一 save, 避免双保存;
//  2. traffic_last_reset_time 必须随流量字段一并清零, 防止回收节点旧基准被
//     ForceResetStaleNodes 误判为「漏跑月度重置」而清空 register 继承的 traffic_used;
//  3. node_ip 语义: nodeIP != "" 才覆盖 (PHP null = 不触碰, Go 用空串表示;
//     allocateNodeId 传 "ip=&ipv6=" 形态的串恒非空, 语义等价)。
package service

import (
	"crypto/rand"
	"encoding/base64"
	"log"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/timex"
)

// ResetNodeToDefaults 对齐 PHP NodeDefaultsReset::resetToDefaults:
// 把节点重置为数据库默认值 (provisioning 干净态), 就地全字段归零 (不落库)。
//
// traffic_used 归零审计: 对齐 PHP isDirty('traffic_used') 时写
// Logger::get('node')->warning('[traffic_used_audit] ...') —— 与 register/editNode/
// resetCycle 的审计链同源, 保证「谁把 traffic_used 改成 0」可追踪。
func ResetNodeToDefaults(node *model.Node, nodeIP string) {
	oldTrafficUsed := node.TrafficUsed

	node.Name = ""
	node.Type = 0
	node.Server = ""
	node.Method = "aes-256-cfb"
	node.Info = ""
	node.Status = ""
	// 解锁信息归零: 回收死节点不携带前任节点的 node_unlock, 否则新节点首次下发
	// config.json 即注入前任的解锁出站/路由, 且订阅端显示错误的解锁标记
	node.NodeUnlock = ""
	// Fix #5: 干净态 = 配置指纹待重算 (防御性; 后续 register 也会再置脏)
	node.ConfigHash = ""
	node.NodeHeartbeat = timex.Unix()
	node.NodeClass = 0
	node.NodeGroup = 0
	node.NodeBandwidth = "0"
	node.BandwidthResetDay = 0 // 废弃字段, 一并纳入干净态
	// —— 流量字段 provisioning 归零 ——
	// 新建(INSERT)与回收死节点(UPDATE)两条路径都显式赋值: UPDATE 不设字段会保留
	// 前任节点旧值 (不会回落 DB 默认), 故必须显式抹零才叫"干净态"。
	// last_reset_time 一并清零, 防止回收节点旧基准被 ForceResetStaleNodes 误判。
	node.TrafficUsed = 0
	node.TrafficLeft = 0
	node.TrafficLimit = 0
	node.TrafficUsedDaily = 0
	node.TrafficLeftDaily = 0
	node.TrafficRawTotal = 0
	node.TrafficResetDay = 0
	node.TrafficMaxDayValue = 0
	node.TrafficLastResetTime = 0
	node.NodeRxTx = "tx"
	node.NodeSpeedlimit = "0"
	node.TrafficRate = float32(trafficRateDefaultValue())
	node.Sort = 0
	node.NodeOnline = 0
	node.CustomRSS = 0
	node.NodeSort = 0
	// 同步清除 Redis 流量窗口 (traffic_7d / traffic_snapshot): node_sort v2 健康分
	// 从 7 日窗口计算, 重装/回收后若残留前任窗口, 新节点的 base/strikes 会被前任
	// 流量污染。v1 时代无此依赖, v2 起必须清。
	nodeSortResetState(int64(node.ID))
	node.MuOnly = 0
	node.IsClone = 0
	node.CustomMethod = 0
	node.NodeConnector = 0
	node.NodeIDs = ""
	node.CountryCode = ""
	// node_cost 默认 0: 仅由节点上报/管理员设置才取别值。clone 节点恒为 0
	// (从不上报, 与主节点共享流量不计独立成本), 否则会被无 is_clone 过滤的
	// Analytics 查询误计入分组成本。
	node.NodeCost = 0

	// —— v2 面板 API 凭据 (provisioning 无条件签发, 与 NPanel 同口径) ——
	// 新建 = 首发, 回收死节点 = 轮换旧 token (旧机器可能仍持有, 轮换即吊销)。
	// PHP 未迁移面板 (无 api_token 列) 时为 no-op; Go 侧共享库已迁移, 恒签发。
	nodeAPITokenRotate(node)

	if nodeIP != "" {
		node.NodeIP = nodeIP
	}

	if oldTrafficUsed != node.TrafficUsed {
		log.Printf("[NodeApi] [traffic_used_audit] node=#%d traffic_used %d->%d src=node.provisioning.resetToDefaults",
			node.ID, oldTrafficUsed, node.TrafficUsed)
	}
}

// ResetOperationalMarks 对齐 PHP NodeDefaultsReset::resetOperationalMarks:
// 清除面板侧运维标记 (register 专用 · 局部清空, 与 resetToDefaults 全字段归零相区分)。
//
// node_sort 写入者只有 DailyJob 动态排序 (-100 扣分)、reportStatus IP 不匹配标记
// (-999 身份隔离)、管理员手设, 节点安装脚本从不上报 —— register 未收到上报时
// 应清零而非保持 (残留 -999 会让订阅端 isWarning 把重装好的节点继续标成维护)。
// 只改内存字段, 不落库 (调用方统一 save)。
func ResetOperationalMarks(node *model.Node) {
	node.NodeSort = 0
	// 重装路径 B (自带 ID, 不走 provisioning) 同样必须清窗口 —— 见 resetToDefaults 注释。
	// 治安标记归零 + 窗口清空后, 新周期从观察期 (node_sort=0) 干净起步。
	nodeSortResetState(int64(node.ID))
}

// ─────────────────────────────────────────────────────────────────────────────
// NodeApiToken (v2 面板 API token 签发) —— 对齐 PHP app/Services/NodeApiToken.php
// ─────────────────────────────────────────────────────────────────────────────

// nodeAPITokenGenerate 对齐 PHP NodeApiToken::generate:
// 32 字节随机数的 base64url (43 字符, 无填充) —— 契约规定格式。
func nodeAPITokenGenerate() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败极罕见; 全零兜底保证不 panic (登录面 token 校验会拒绝)
		for i := range b {
			b[i] = 0
		}
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// nodeAPITokenRotate 对齐 PHP NodeApiToken::rotate: 无条件签发新 token
// (provisioning 语义)。只改内存字段, 由调用方统一落库。
func nodeAPITokenRotate(node *model.Node) {
	node.APIToken = nodeAPITokenGenerate()
}

// nodeAPITokenIssueIfNeeded 对齐 PHP NodeApiToken::issueIfNeeded: 仅当 token 为空时
// 补发 (register 自愈路径: 存量节点重装时 api_token 可能为空 → /api/node/config 只会
// 下发旧式 ssrpanel 段; 补发后重拉配置即带 panelApi 段, 已有 token 不动)。
// 返回是否补发。只改内存字段, 由调用方统一落库。
func nodeAPITokenIssueIfNeeded(node *model.Node) bool {
	if node.APIToken != "" {
		return false
	}
	node.APIToken = nodeAPITokenGenerate()
	return true
}
