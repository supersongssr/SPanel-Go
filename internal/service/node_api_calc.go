// 节点 API 配套计算器 —— 对齐 PHP:
//   - app/Services/TrafficRate/TrafficRateCalculator.php (register 期派生 + 默认值)
//   - app/Services/NodeSort/NodeSortCalculator.php (resetState 窗口清理)
//   - app/Services/Dns/AddressPolicy.php (register 期 add= 地址策略)
//   - app/Utils/DomainPool.php pickDomain/pickRotating (records_limit 感知选域)
//   - app/Services/NodeMonthlyTraffic.php (reportStatus 用到的记账公式;
//     日历公式 DaysElapsed/DaysRemaining/DailyLeftCap 已在 node_monthly.go, 此处复用)
//
// 仅移植 registerNode / reportStatus / updateUnlock / editNode / NodeDefaultsReset
// 用到的部分; DailyJob 侧入口 (calculate/snapshotDailyPeaks/recalculateAll/
// NodeSortCalculator::calculate 等) 不在本文件范围。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/notify"
	"spanel-go/internal/pkg/timex"
)

// ─────────────────────────────────────────────────────────────────────────────
// TrafficRateCalculator (register 期)
// ─────────────────────────────────────────────────────────────────────────────

const (
	// trafficRateDefault 对齐 PHP TrafficRateCalculator::DEFAULT_RATE
	trafficRateDefault = 1.0
)

// trafficRateDefaultValue 对齐 PHP TrafficRateCalculator::defaultValue (新建/重置/兜底)。
func trafficRateDefaultValue() float64 { return trafficRateDefault }

// trafficRateDeriveFromRegistration 对齐 PHP TrafficRateCalculator::deriveFromRegistration。
// 优先级: 上报 node_traffic_rate → 上报 node_cost 派生 (>0 ? cost : 1.0) → 未上报 (ok=false,
// 调用方保持节点既有 traffic_rate)。数值解析失败按 PHP (float) 弱转语义回 0。
func trafficRateDeriveFromRegistration(params url.Values, nodeCost float64) (rate float64, ok bool) {
	if raw, present := params["node_traffic_rate"]; present {
		if len(raw) > 0 {
			return phpFloat(raw[0]), true
		}
		return 0, true
	}
	if _, present := params["node_cost"]; present {
		if nodeCost > 0 {
			return nodeCost, true
		}
		return trafficRateDefault, true
	}
	return 0, false
}

// ─────────────────────────────────────────────────────────────────────────────
// NodeSortCalculator::resetState (register/provisioning 期窗口清理)
// ─────────────────────────────────────────────────────────────────────────────

// nodeSortResetState 对齐 PHP NodeSortCalculator::resetState:
// 重装/回收时清除节点级 Redis 流量窗口 (traffic_7d / traffic_snapshot),
// 防新节点 node_sort 健康分被前任流量污染。Redis 故障不阻断主流程。
func nodeSortResetState(nodeID int64) {
	rdb := database.Redis
	if rdb == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	keys := []string{
		fmt.Sprintf("ssp:node:%d:traffic_7d", nodeID),
		fmt.Sprintf("ssp:node:%d:traffic_snapshot", nodeID),
	}
	if err := rdb.Del(ctx, keys...).Err(); err != nil {
		log.Printf("[NodeApi] NodeSortCalculator resetState fail node=#%d: %v", nodeID, err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AddressPolicy (register 期 add= 地址策略)
// ─────────────────────────────────────────────────────────────────────────────

// addrPolicy 对齐 PHP AddressPolicy::resolveForMain/resolveForClone 的返回结构
// {add:string, extra:[]string, mode:string}
type addrPolicy struct {
	add   string
	extra []string
	mode  string
}

// addressPolicyResolveForMain 对齐 PHP AddressPolicy::resolveForMain:
//
//	CDN      → 优选域名池随机域名 (池空回退真实 IP, legacy cdnip 模式) + extra [cdn=1]
//	非 CDN   → 真实 IP (ipv4 优先, 否则 ipv6)
func addressPolicyResolveForMain(nodeID int64, isCdn bool, rootDomain, ipv4, ipv6 string) addrPolicy {
	fallback := addressPolicyFallbackIP(ipv4, ipv6)
	if isCdn {
		return addressPolicyResolveCDN(nodeID, fallback)
	}
	return addrPolicy{add: fallback, extra: []string{}, mode: "ip"}
}

// addressPolicyResolveForClone 对齐 PHP AddressPolicy::resolveForClone:
//
//	CDN + hy2         → 绕过 CDN 直连真实 IP (CF 不代理 UDP)
//	CDN + 其它         → 同 main 的 CDN 策略 (每 clone 独立随机/消费)
//	非 CDN + IPv6 半区  → 真实 IPv6
//	非 CDN + IPv4 半区  → 域名池轮换取域名 (方案C 分散根域名防封锁), 池空回退 IPv4
func addressPolicyResolveForClone(cloneID int64, isCdn bool, isIPv6Node bool, ipv4, ipv6 string, rotationOffset int64, proto string) addrPolicy {
	if isCdn && proto == "hy2" {
		add := ipv4
		if isIPv6Node {
			add = ipv6
		}
		return addrPolicy{add: add, extra: []string{}, mode: "ip"}
	}
	if isCdn {
		return addressPolicyResolveCDN(cloneID, addressPolicyFallbackIP(ipv4, ipv6))
	}
	if isIPv6Node {
		return addrPolicy{add: ipv6, extra: []string{}, mode: "ip"}
	}

	offset := rotationOffset
	if offset < 0 {
		offset = -offset
	}
	entry, ok := poolPickRotating(DomainPoolNormal(), offset)
	if ok && entry.Domain != "" {
		return addrPolicy{
			add:   "s" + strconv.FormatInt(cloneID, 10) + "." + entry.Domain,
			extra: []string{},
			mode:  "domain",
		}
	}
	log.Printf("[NodeApi] [AddressPolicy] clone_id=%d normal domain pool empty, fallback to IPv4", cloneID)
	return addrPolicy{add: ipv4, extra: []string{}, mode: "ip"}
}

// addressPolicyResolveCDN 对齐 PHP AddressPolicy::resolveCdn:
// 优选域名池非空 → 随机选取 + cdn=1; 池空 → legacy cdnip 模式
// (Go 侧 CdnIpPool 未移植 → CSV 恒视为耗尽, 等价 PHP 池缺失时回退真实 IP 分支,
// 仍携带 cdn=1 + cdnip=<add> 标记)。
func addressPolicyResolveCDN(nodeID int64, fallbackIP string) addrPolicy {
	domains := addressPolicyPreferredDomains()
	if len(domains) > 0 {
		return addrPolicy{
			add:   domains[rand.Intn(len(domains))],
			extra: []string{"cdn=1"},
			mode:  "cdn-domain",
		}
	}
	log.Printf("[NodeApi] [AddressPolicy] node_id=%d CDN preferred domains empty, "+
		"cdnip mode but preferred IP pool empty, fallback to real IP '%s'", nodeID, fallbackIP)
	return addrPolicy{
		add:   fallbackIP,
		extra: []string{"cdn=1", "cdnip=" + fallbackIP},
		mode:  "cdn",
	}
}

// addressPolicyPreferredDomains 对齐 PHP AddressPolicy::getPreferredDomains:
// 读取并清洗优选域名池 (cloudflare_cdn_preferred_domains):
// trim → 去 http(s):// 前缀 → 去路径 → 小写 → 去空 → 去重 (保持出现顺序)。
var addressPolicyDomainChars = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

func addressPolicyPreferredDomains() []string {
	cfg := config.GlobalConfig
	if cfg == nil || len(cfg.Node.CloudflareCDNPreferredDomains) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var clean []string
	for _, raw := range cfg.Node.CloudflareCDNPreferredDomains {
		d := strings.ToLower(strings.TrimSpace(raw))
		d = strings.TrimPrefix(strings.TrimPrefix(d, "https://"), "http://")
		if i := strings.IndexByte(d, '/'); i >= 0 {
			d = d[:i]
		}
		if d == "" || seen[d] || !addressPolicyDomainChars.MatchString(d) {
			continue
		}
		seen[d] = true
		clean = append(clean, d)
	}
	return clean
}

// addressPolicyFallbackIP 对齐 PHP AddressPolicy::fallbackIp (ipv4 优先)。
func addressPolicyFallbackIP(ipv4, ipv6 string) string {
	if ipv4 != "" {
		return ipv4
	}
	return ipv6
}

// ─────────────────────────────────────────────────────────────────────────────
// DomainPool pickDomain / pickRotating (records_limit 感知选域)
// ─────────────────────────────────────────────────────────────────────────────

// dnsRecordsCountForDomain 统计某根域名下 dns_records 行数 (选域限流用; 失败返回 0)。
func dnsRecordsCountForDomain(domain string) int64 {
	if database.DB == nil {
		return 0
	}
	var n int64
	if err := database.DB.Model(&model.DnsRecord{}).
		Where("root_domain = ?", domain).Count(&n).Error; err != nil {
		log.Printf("[NodeApi] count dns_records for domain=%s failed: %v", domain, err)
		return 0
	}
	return n
}

// poolDomainAvailable 域名是否未满 (records_limit=0 = 不限)。
func poolDomainAvailable(e DomainEntry) bool {
	if e.RecordsLimit <= 0 {
		return true
	}
	return dnsRecordsCountForDomain(e.Domain) < int64(e.RecordsLimit)
}

// poolPickDomain 对齐 PHP DomainPool::pickDomain: 依序选取第一个未满域名。
// PHP 顺序 = 配置文件书写顺序; Go 域名池来自 YAML map (无序), 顺序为解析序 —
// 语义偏差仅限多域名时的选择倾向, records_limit 限流语义完整保留。
func poolPickDomain(entries []DomainEntry) (DomainEntry, bool) {
	for _, e := range entries {
		if e.Domain == "" {
			continue
		}
		if poolDomainAvailable(e) {
			return e, true
		}
	}
	return DomainEntry{}, false
}

// poolPickRotating 对齐 PHP DomainPool::pickRotating: 从 offset 位置起轮询,
// 跳过已满域名; 全满则 best-effort 返回 offset 处域名 (允许超限, 不让 clone 无域名可用)。
func poolPickRotating(entries []DomainEntry, offset int64) (DomainEntry, bool) {
	if len(entries) == 0 {
		return DomainEntry{}, false
	}
	if offset < 0 {
		offset = -offset
	}
	n := int64(len(entries))
	for try := int64(0); try < n; try++ {
		e := entries[(offset+try)%n]
		if e.Domain == "" {
			continue
		}
		if poolDomainAvailable(e) {
			return e, true
		}
	}
	return entries[offset%n], true
}

// randIntn 包一层 math/rand (与 PHP rand()/array_rand() 同源的非安全随机)。
func randIntn(n int) int {
	if n <= 0 {
		return 0
	}
	return rand.Intn(n)
}

// ─────────────────────────────────────────────────────────────────────────────
// NodeMonthlyTraffic 私有移植 (reportStatus 记账红线)
// ─────────────────────────────────────────────────────────────────────────────

// monthlyBillRawTotal 对齐 PHP NodeMonthlyTraffic::billRawTotal:
// 按 node_rxtx 计费模式把 NIC 原始 rx/tx 读数折算为单一累计值 (rxtx=均值)。
func monthlyBillRawTotal(rawTx, rawRx float64, billingMode string) float64 {
	switch billingMode {
	case "rx":
		return rawRx
	case "rxtx":
		return (rawTx + rawRx) / 2
	default: // "tx"
		return rawTx
	}
}

// monthlyAddIncrement 对齐 PHP NodeMonthlyTraffic::addIncrement (基线保护四分支,
// 与原 reportStatus 逐字一致, 勿改):
//
//	rawTotal<=0      非法/缺失   → 增量 0, 不刷基线
//	lastRaw<=0       首次/重置后 → 增量 0, 刷基线
//	rawTotal<lastRaw NIC 回退    → 增量=rawTotal, 刷基线
//	正常                         → 增量=差值, 刷基线
//
// 就地改 node.TrafficUsed / node.TrafficRawTotal, 由调用方统一落库。
// 返回本轮增量 (byte)。
func monthlyAddIncrement(node *model.Node, rawTotal float64) int64 {
	lastRaw := node.TrafficRawTotal
	var increment int64
	updateRawTotal := false

	switch {
	case rawTotal <= 0:
		// 非法/缺失读数: 不计增量, 也不更新基线 (保留上次有效值)
		increment = 0
	case lastRaw <= 0:
		// 首次上报 / 基线为 0: 用本次读数建基线, 本轮不计增量
		increment = 0
		updateRawTotal = true
	case rawTotal < float64(lastRaw):
		// NIC 计数器回退 (节点重启/计数器归零): 当前读数视为本次增量 + 新基线
		increment = int64(rawTotal)
		updateRawTotal = true
	default:
		// 正常相减
		increment = int64(rawTotal - float64(lastRaw))
		updateRawTotal = true
	}

	node.TrafficUsed += increment
	if updateRawTotal {
		node.TrafficRawTotal = int64(rawTotal)
	}
	return increment
}

// monthlyComputeLeft 对齐 PHP NodeMonthlyTraffic::computeLeft:
// 本周期剩余 = limit>0 ? max(0, limit-used) : 0 (0=无限, 双义)。
func monthlyComputeLeft(node *model.Node) int64 {
	if node.TrafficLimit > 0 {
		left := node.TrafficLimit - node.TrafficUsed
		if left < 0 {
			return 0
		}
		return left
	}
	return 0
}

// monthlyComputeDailyUsed 对齐 PHP NodeMonthlyTraffic::computeDailyUsed:
// 已用周期日均 = used / daysElapsed。
func monthlyComputeDailyUsed(node *model.Node) int64 {
	return node.TrafficUsed / int64(DaysElapsed(int(node.TrafficResetDay)))
}

// monthlyComputeDailyLeft 对齐 PHP NodeMonthlyTraffic::computeDailyLeft:
//
//	有限额: left / daysRemaining (自然有界, 不二次封顶)
//	无限制: min(1 TiB, traffic_max_day_value) (冷启动 max_day=0 回退 1 TiB)
func monthlyComputeDailyLeft(node *model.Node) int64 {
	if node.TrafficLimit > 0 {
		raw := monthlyComputeLeft(node) / int64(DaysRemaining(int(node.TrafficResetDay)))
		if raw < 0 {
			return 0
		}
		return raw
	}
	return DailyLeftCap(node.TrafficMaxDayValue)
}

// monthlyIsExhausted 对齐 PHP NodeMonthlyTraffic::isExhausted:
// 有限额节点 used >= limit - reserve (无限制恒 false)。
func monthlyIsExhausted(node *model.Node, reserveBytes int64) bool {
	if node.TrafficLimit <= 0 {
		return false
	}
	return node.TrafficUsed >= node.TrafficLimit-reserveBytes
}

// monthlyApplyDerived 对齐 PHP NodeMonthlyTraffic::applyDerived:
// 一次性重算 left + used_daily + left_daily (幂等), 并触发 reset_day 缺失配置告警。
func monthlyApplyDerived(node *model.Node) {
	monthlyWarnIfNoResetDay(node)
	node.TrafficLeft = monthlyComputeLeft(node)
	node.TrafficUsedDaily = monthlyComputeDailyUsed(node)
	node.TrafficLeftDaily = monthlyComputeDailyLeft(node)
}

// monthlyEnsureBaseline 对齐 PHP NodeMonthlyTraffic::ensureBaseline:
// 仅当无基准 (=0) 时盖 traffic_last_reset_time = now (不覆盖更近期值), best-effort。
func monthlyEnsureBaseline(nodeID int64) {
	if database.DB == nil {
		return
	}
	err := database.DB.Model(&model.Node{}).
		Where("id = ? AND traffic_last_reset_time = 0", nodeID).
		Update("traffic_last_reset_time", timex.Unix()).Error
	if err != nil {
		log.Printf("[NodeApi] ensureBaseline failed node_id=%d: %v", nodeID, err)
	}
}

// ── reset_day 缺失告警 (两级节流: Redis 按天去重 + 60s 合并窗口) ──

const (
	// monthlyResetDayWarnTTL 对齐 PHP NodeMonthlyTraffic::RESET_DAY_WARN_TTL
	monthlyResetDayWarnTTL = 86400
	// monthlyResetDayWarnCoalesceSecs 对齐 PHP RESET_DAY_WARN_COALESCE_SECS
	monthlyResetDayWarnCoalesceSecs = 60
)

// monthlyWarnIfNoResetDay 对齐 PHP NodeMonthlyTraffic::warnIfNoResetDay:
// 有限额节点却未配置 traffic_reset_day → 配置缺失告警。
//  1. Redis 按天去重 (每节点每天最多一条; Redis 故障跳过去重, 不阻断);
//  2. 60 秒合并窗口: 攒满窗口内触发的所有节点合并成一条通知发送
//     (PHP 用文件+flock 跨进程合并; Go 单进程内存态等价实现)。
func monthlyWarnIfNoResetDay(node *model.Node) {
	// 仅有限额节点 + 未配置重置日才视为配置问题
	if node.TrafficLimit <= 0 || node.TrafficResetDay > 0 {
		return
	}

	// 1) 按天去重
	key := fmt.Sprintf("ssp:node:%d:reset_day_warn_%s", node.ID, timex.Ymd())
	rdb := database.Redis
	if rdb != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		ok, err := rdb.SetNX(ctx, key, "1", time.Duration(monthlyResetDayWarnTTL)*time.Second).Result()
		cancel()
		if err == nil && !ok {
			return // 今天已通知过
		}
		if err != nil {
			log.Printf("[NodeApi] reset_day warn dedup failed: %v", err)
		}
	}

	// 2) 60 秒合并窗口 (内存态; flushPendingResetDayWarn 的周期兜底归 HourlyJob, 未移植)
	name := node.Name
	if name == "" {
		name = "#" + strconv.FormatInt(int64(node.ID), 10)
	}
	if flush := monthlyCoalesceResetDayWarn(int64(node.ID), name); len(flush) > 0 {
		monthlySendResetDayWarn(flush)
	}
}

// resetDayWarnCoalesce 合并窗口状态 (PHP 为 .db/reset_day_warn_coalesce.json 文件)
var resetDayWarnCoalesce = struct {
	sync.Mutex
	lastSend int64
	pending  map[int64]string // node_id => name (按 id 排序输出)
}{pending: map[int64]string{}}

// monthlyCoalesceResetDayWarn 并入当前节点; 距上次发送满 60s 则取出全部 pending 合并发送。
func monthlyCoalesceResetDayWarn(nodeID int64, nodeName string) map[int64]string {
	now := timex.Unix()
	resetDayWarnCoalesce.Lock()
	defer resetDayWarnCoalesce.Unlock()

	resetDayWarnCoalesce.pending[nodeID] = nodeName
	if now-resetDayWarnCoalesce.lastSend < monthlyResetDayWarnCoalesceSecs {
		return nil
	}
	flush := resetDayWarnCoalesce.pending
	resetDayWarnCoalesce.pending = map[int64]string{}
	resetDayWarnCoalesce.lastSend = now
	return flush
}

// monthlySendResetDayWarn 对齐 PHP NodeMonthlyTraffic::sendResetDayWarn (锁外发送)。
func monthlySendResetDayWarn(flushNodes map[int64]string) {
	ids := make([]int64, 0, len(flushNodes))
	for id := range flushNodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var lines []string
	for _, id := range ids {
		lines = append(lines, fmt.Sprintf("  - #%d %s", id, flushNodes[id]))
	}
	body := "以下限额节点未设置 traffic_reset_day (=0), 无法按月重置周期流量, " +
		"流量将持续累积直至撞限额:\n" + strings.Join(lines, "\n") + "\n请检查节点配置。"
	notify.Warning("节点流量周期配置缺失", body)
}

// ─────────────────────────────────────────────────────────────────────────────
// 配置读取 (PHP Config::get 对应物)
// ─────────────────────────────────────────────────────────────────────────────

// nodeConfigRegisterGroup 对齐 PHP Config::get('node_group') (config.default.php 默认 '4'):
// Go 配置 0 视为未配置, 回退 4 (node_group=0 具有「全员可见」语义, 不能做默认)。
func nodeConfigRegisterGroup() int {
	if cfg := config.GlobalConfig; cfg != nil && cfg.Node.NodeGroup > 0 {
		return cfg.Node.NodeGroup
	}
	return 4
}

// nodeConfigCloneClassMax 对齐 PHP Config::getWithDefault('node_clone_class_max', 10)。
func nodeConfigCloneClassMax() int {
	if cfg := config.GlobalConfig; cfg != nil && cfg.Node.NodeCloneClassMax > 0 {
		return cfg.Node.NodeCloneClassMax
	}
	return 10
}

// nodeMinAllocatableID 对齐 PHP NodeApiService::MIN_ALLOCATABLE_ID = 100:
// < 此值的节点为保留区 (测试/公告/系统节点), 自动分配/自动删除均不碰。
const nodeMinAllocatableID = 100

// ─────────────────────────────────────────────────────────────────────────────
// PHP 弱类型数值语义
// ─────────────────────────────────────────────────────────────────────────────

// phpFloat 复刻 PHP (float) 字符串强转: 允许前导空白 + 可选符号 + 数字串
// (可含小数点/指数), 取最长合法前缀; 无数字返回 0。
var phpFloatRe = regexp.MustCompile(`^[ \t\n\r\v\f]*[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?`)

func phpFloat(s string) float64 {
	m := phpFloatRe.FindString(s)
	if m == "" {
		return 0
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(m), 64)
	if err != nil {
		return 0
	}
	return v
}

// phpNotEmpty 复刻 PHP !empty() 语义: "" 与 "0" 均为空。
func phpNotEmpty(s string) bool {
	return s != "" && s != "0"
}

// phpIsNumeric 复刻 PHP is_numeric(): 整数/浮点数字符串或数值 (bool 非 numeric)。
func phpIsNumeric(v any) bool {
	switch t := v.(type) {
	case float64:
		return true
	case float32:
		return true
	case int, int64, int32:
		return true
	case json.Number:
		_, err := t.Float64()
		return err == nil
	case string:
		_, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return err == nil
	default:
		return false
	}
}

// phpStringValue 复刻 PHP (string) 强转 (JSON 值域: 数值按 PHP 精度规则, bool → "1"/"")。
func phpStringValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "1"
		}
		return ""
	case float64:
		return phpFloatToString(t)
	case float32:
		return phpFloatToString(float64(t))
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	case json.Number:
		return t.String()
	case nil:
		return ""
	default:
		return ""
	}
}

// phpFloatToString 复刻 PHP float→string (默认精度, 去尾零: 2048.0 → "2048")。
func phpFloatToString(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// isValidIPv4 / isValidIPv6 对齐 PHP FILTER_VALIDATE_IP + FLAG 白名单。
func isValidIPv4(raw string) bool {
	ip := net.ParseIP(raw)
	return ip != nil && ip.To4() != nil && !strings.Contains(raw, ":")
}

func isValidIPv6(raw string) bool {
	ip := net.ParseIP(raw)
	return ip != nil && strings.Contains(raw, ":")
}

// errNodeAPINilDB 统一的 DB 未初始化错误
var errNodeAPINilDB = errors.New("database not initialized")
