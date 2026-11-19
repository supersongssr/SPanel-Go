// decision.go 节点决策台 — 逐行移植自 PHP:
//   - AdminController::nodeDecision (取数与组装: 矩阵/在线/7d/今日流量/组集合/
//     nodeRows 分层三数/verdicts/迁移候选/离线节点/图表序列)
//   - Analytics::buildInsights + DECISION_PRESETS (纯函数决策引擎 R1~R10/R14)
//
// 端点: GET /api/v2/admin/stats/node-decision (只读, 数值单位除 *_gb/*_pct 外均为 bytes)
// 方案: .plans/2026-10-24-节点决策台方案.md + 2026-11-11 v2 扩展
package adminv2

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/tools"
	"spanel-go/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// ════════════════════════ 决策阈值 (Analytics::DECISION_PRESETS, 单一位置可调) ════════════════════════

type decisionThresholds struct {
	onlinePerNodeWarn   int // 每台活跃真实节点承载在线用户告警阈值 (R2/R8 黄)
	onlinePerNodeDanger int // 同上红线 (R2 红)
	utilWarn            int // 组流量利用率告警 % (R3 黄)
	utilDanger          int // 组流量利用率红线 % (R3 红)
	utilIdle            int // G5 冗余判定: 利用率低于此值 % (R6)
	g5MinKeep           int // G5 至少保留真实节点数 (R6)
	minRealPerClass     int // L1~L5 每级最少活跃真实节点 (R4)
	minClonePerClass    int // L6~L10 每级最少 clone (R5)
	poolMarginPct       int // R9 贴线预警余量 %
	// ── v2 (.plans/2026-11-11-节点决策台v2-流量够用判定与扩容迁移裁决方案.md) ──
	overNodeWarnPct   int  // R10 组内超/将超日配额节点比例告警 %
	overNodeDangerPct int  // 同上红线 %
	projOverMargin    int  // 节点全天外推超额风险余量 %
	trimMaxPerNode    int  // R14 冗余判定: 人/台低于此值才可迁出
	minKeepDefault    int  // R14 非 G5 组迁出后至少保留的可用节点数
	trendDaysWarn     int  // R14 watch: 趋势外推距 95% 红线不足此天数
	perClass          int  // PHP Config node_sub_limit_per_class (Go 配置无此键, 取 config.default.php 默认 8)
	subBase           int  // PHP Config node_sub_limit_base (默认 10)
	groupTarget       int  // PHP Config node_decision_group_user_target (默认 1000)
}

var decisionPresets = decisionThresholds{
	onlinePerNodeWarn: 150, onlinePerNodeDanger: 250,
	utilWarn: 80, utilDanger: 95, utilIdle: 40,
	g5MinKeep: 3, minRealPerClass: 1, minClonePerClass: 1,
	poolMarginPct: 10,
	overNodeWarnPct: 30, overNodeDangerPct: 50, projOverMargin: 10,
	trimMaxPerNode: 80, minKeepDefault: 1, trendDaysWarn: 7,
	perClass: 8, subBase: 10, groupTarget: 1000,
}

// ════════════════════════ 取数中间结构 (与 Analytics 取数函数输出一一对应) ════════════════════════

// userMatrixRow getUserGroupClassMatrix 输出行: {g, c, maxc, users, live30, live24}
// (c 为矩阵列位夹 0~10, maxc 保留真实等级供 need 计算 —— class 11/99/111 不按 10 计)
type userMatrixRow struct {
	G      int   `json:"g"`
	C      int   `json:"c"`
	Maxc   int   `json:"maxc"`
	Users  int64 `json:"users"`
	Live30 int64 `json:"live30"`
	Live24 int64 `json:"live24"`
}

// nodeMatrixRow getNodeGroupClassMatrix 输出行 (订阅口径: type=1 AND custom_rss=1, 不过滤心跳)
type nodeMatrixRow struct {
	G          int   `json:"g"`
	C          int   `json:"c"`
	RealAll    int64 `json:"real_all"`
	RealAlive  int64 `json:"real_alive"`
	CloneAll   int64 `json:"clone_all"`
	CloneAlive int64 `json:"clone_alive"`
	Cost       int64 `json:"cost"`
}

// groupTrafficRow 组级流量输入: 供给日均/昨日用量/今日实时/7天日均/趋势外推距95%天数
type groupTrafficRow struct {
	SupplyDaily int64 `json:"supply_daily"`
	UsedLastday int64 `json:"used_lastday"`
	Today       int64 `json:"today"`
	Avg7d       int64 `json:"avg7d"`
	DaysTo95    *int  `json:"days_to_95"`
}

// seriesPoint getGroupTraffic7d 输出点 (按日期升序)
type seriesPoint struct {
	Ts          int64  `json:"ts"`
	Date        string `json:"date"`
	UsedLastday int64  `json:"used_lastday"`
}

// decisionNodeRow v2 节点级行 (心跳内真实节点, 分层三数):
// layer 3=超配额隐藏 4=冷启动(quota=0 且今日无用量) 5=可用
type decisionNodeRow struct {
	ID            int32 `json:"id"`
	G             int   `json:"g"`
	C             int   `json:"c"`
	Cost          int64 `json:"cost"`
	Layer         int   `json:"layer"`
	TodayUsed     int64 `json:"today_used"`
	Quota         int64 `json:"quota"`
	ProjToday     int64 `json:"proj_today"`
	Avg7          int64 `json:"avg7"`
	Left          int64 `json:"left"`
	DaysRemaining int   `json:"days_remaining"`
}

// nodeDisplayRow E 区展示行 (离线/迁移候选/隐藏/每节点表共用)
type nodeDisplayRow struct {
	ID           int32   `json:"id"`
	Name         string  `json:"name"`
	Group        int32   `json:"group"`
	Class        int32   `json:"class"`
	Cost         int32   `json:"cost"`
	Alive        int64   `json:"alive"`
	HeartbeatMin int64   `json:"heartbeat_min"` // 距今分钟; -1 = 从未心跳
	UsedLastday  int64   `json:"used_lastday"`
	UsedGB       float64 `json:"used_gb"`
	TodayGB      float64 `json:"today_gb"`
	QuotaGB      float64 `json:"quota_gb"`
	TodayPct     *int    `json:"today_pct"` // quota>0 才有, 否则 null
	Status       string  `json:"status,omitempty"`
	StatusLabel  string  `json:"status_label,omitempty"`
}

// disabledNodeRow 停用节点行 (type=0)
type disabledNodeRow struct {
	ID           int32   `json:"id"`
	Name         string  `json:"name"`
	Group        int32   `json:"group"`
	Class        int32   `json:"class"`
	HeartbeatMin int64   `json:"heartbeat_min"`
	UsedGB       float64 `json:"used_gb"`
	LimitGB      float64 `json:"limit_gb"`
}

// ════════════════════════ 取数函数 (SQL 语义照搬 Analytics.php) ════════════════════════

// fetchUserGroupClassMatrix 用户矩阵: 组×等级 全量聚合 (enable=1)。
// 今日流量不在此统计 (旧 user 口径 Σ(u+d-last_day_t) 因 dailyjob 只快照 last_day_t=d
// 会把历史累计 u 误入今日, 已废弃 —— 今日流量统一走节点游标口径)。
func fetchUserGroupClassMatrix(now int64) []userMatrixRow {
	var rows []struct {
		G      int32 `gorm:"column:g"`
		C      int32 `gorm:"column:c"`
		Users  int64 `gorm:"column:users"`
		Live30 int64 `gorm:"column:live30"`
		Live24 int64 `gorm:"column:live24"`
	}
	database.DB.Raw(
		"SELECT node_group AS g, class AS c, COUNT(*) AS users, "+
			"SUM(IF(t > ?, 1, 0)) AS live30, SUM(IF(t > ?, 1, 0)) AS live24 "+
			"FROM user WHERE enable = 1 GROUP BY node_group, class",
		now-1800, now-86400,
	).Scan(&rows)
	out := make([]userMatrixRow, 0, len(rows))
	for _, r := range rows {
		maxc := int(r.C)
		if maxc < 0 {
			maxc = 0
		}
		c := maxc
		if c > 10 {
			c = 10
		}
		out = append(out, userMatrixRow{
			G: int(r.G), C: c, Maxc: maxc,
			Users: r.Users, Live30: r.Live30, Live24: r.Live24,
		})
	}
	return out
}

// fetchNodeGroupClassMatrix 节点矩阵: 组×等级 全量聚合
// (与订阅口径一致: type=1 AND custom_rss=1, 不过滤心跳 —— 离线节点用户订阅仍能拿到,
// 池必须计入; 心跳仅作活跃/离线分层供 R2/R4/G5 候选使用)
func fetchNodeGroupClassMatrix(h7 int64) []nodeMatrixRow {
	var rows []struct {
		G          int32 `gorm:"column:g"`
		C          int32 `gorm:"column:c"`
		RealAll    int64 `gorm:"column:real_all"`
		RealAlive  int64 `gorm:"column:real_alive"`
		CloneAll   int64 `gorm:"column:clone_all"`
		CloneAlive int64 `gorm:"column:clone_alive"`
		Cost       int64 `gorm:"column:cost"`
	}
	database.DB.Raw(
		"SELECT node_group AS g, node_class AS c, "+
			"SUM(is_clone = 0) AS real_all, "+
			"SUM(is_clone = 0 AND node_heartbeat > ?) AS real_alive, "+
			"SUM(is_clone > 0) AS clone_all, "+
			"SUM(is_clone > 0 AND node_heartbeat > ?) AS clone_alive, "+
			"SUM(IF(is_clone = 0, node_cost, 0)) AS cost "+
			"FROM ss_node WHERE type = 1 AND custom_rss = 1 GROUP BY node_group, node_class",
		h7, h7,
	).Scan(&rows)
	out := make([]nodeMatrixRow, 0, len(rows))
	for _, r := range rows {
		c := int(r.C)
		if c < 0 {
			c = 0
		} else if c > 10 {
			c = 10
		}
		out = append(out, nodeMatrixRow{
			G: int(r.G), C: c,
			RealAll: r.RealAll, RealAlive: r.RealAlive,
			CloneAll: r.CloneAll, CloneAlive: r.CloneAlive, Cost: r.Cost,
		})
	}
	return out
}

// fetchAliveUserByNode 每节点实时在线人数 (alive_ip 120s 窗口, 按 userid 去重;
// IP 级口径, 同用户跨节点会重复计)
func fetchAliveUserByNode(now int64) map[int32]int64 {
	var rows []struct {
		NodeID int32 `gorm:"column:nodeid"`
		Online int64 `gorm:"column:online"`
	}
	database.DB.Raw(
		"SELECT nodeid, COUNT(DISTINCT userid) AS online FROM alive_ip WHERE datetime > ? GROUP BY nodeid",
		now-120,
	).Scan(&rows)
	m := make(map[int32]int64, len(rows))
	for _, r := range rows {
		m[r.NodeID] = r.Online
	}
	return m
}

// fetchGroupTodayTraffic 每分组「今日实时流量」(bytes), 仅真实节点 (is_clone=0)。
// 口径: 节点累计游标差 traffic_used - traffic_used_lastday; 计数器中途回退
// (used < lastday) 时取 used 本身 (与 TrafficRateCalculator 日增量钳制规则一致)。
func fetchGroupTodayTraffic() map[int]int64 {
	var rows []struct {
		G     int32 `gorm:"column:g"`
		Today int64 `gorm:"column:today"`
	}
	database.DB.Raw(
		"SELECT node_group AS g, SUM(CASE WHEN traffic_used >= traffic_used_lastday "+
			"THEN traffic_used - traffic_used_lastday ELSE traffic_used END) AS today "+
			"FROM ss_node WHERE is_clone = 0 GROUP BY node_group",
	).Scan(&rows)
	m := make(map[int]int64, len(rows))
	for _, r := range rows {
		m[int(r.G)] = r.Today
	}
	return m
}

// readTrafficDailyLastRun 读 Redis ssp:node_traffic_daily:last_run (缺失/异常返回 0)
func readTrafficDailyLastRun(ctx context.Context) int64 {
	rdb := database.Redis
	if rdb == nil {
		return 0
	}
	v, err := rdb.Get(ctx, "ssp:node_traffic_daily:last_run").Result()
	if err != nil {
		return 0
	}
	n, perr := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if perr != nil || n <= 0 {
		return 0
	}
	return n
}

// dailyJobBaseTs 最近一次 dailyjob (流量日快照) 执行时刻 —— v2 日内时间进度基准
// (traffic_used_lastday 在 dailyjob 每日 04:11 刷新, 游标差 today_used 是
// 「自该时刻以来的用量」, 外推全天须除以已经过的时间比例而非自然日比例)。
func dailyJobBaseTs(now time.Time) int64 {
	if lastRun := readTrafficDailyLastRun(context.Background()); lastRun > 0 {
		return lastRun
	}
	// 回退: 今天 04:11 已过则基准=今天 04:11, 否则=昨天 04:11 (与 7d 日期对齐同款启发式)
	today0411 := time.Date(now.Year(), now.Month(), now.Day(), 4, 11, 0, 0, now.Location())
	if now.Unix() >= today0411.Unix() {
		return today0411.Unix()
	}
	return today0411.Unix() - 86400
}

// traffic7dBaseTs 7d 序列日期对齐基准: 最近 dailyjob 所属日的零点
// (traffic_7d[0] = 最近一次跑出的差值 = 其前一日的用量)
func traffic7dBaseTs(now time.Time) int64 {
	if lastRun := readTrafficDailyLastRun(context.Background()); lastRun > 0 {
		lt := time.Unix(lastRun, 0).In(now.Location())
		return time.Date(lt.Year(), lt.Month(), lt.Day(), 0, 0, 0, 0, now.Location()).Unix()
	}
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	today0411 := time.Date(now.Year(), now.Month(), now.Day(), 4, 11, 0, 0, now.Location())
	if now.Unix() >= today0411.Unix() {
		return midnight.Unix()
	}
	return midnight.Unix() - 86400
}

// pipelineTraffic7d 批量读取节点 7 日窗口 ssp:node:{id}:traffic_7d (LPUSH 最新在前),
// 对齐 PHP getNodes7dAvg/getGroupTraffic7d 的 pipeline 取法; Redis 不可用返回空表。
func pipelineTraffic7d(ctx context.Context, ids []int32) map[int32][]int64 {
	out := make(map[int32][]int64, len(ids))
	rdb := database.Redis
	if rdb == nil || len(ids) == 0 {
		return out
	}
	pipe := rdb.Pipeline()
	cmds := make([]*redis.StringSliceCmd, len(ids))
	for i, id := range ids {
		cmds[i] = pipe.LRange(ctx, fmt.Sprintf("ssp:node:%d:traffic_7d", id), 0, 6)
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return out // 连接级失败: 全部按「从未上报」处理
	}
	for i, cmd := range cmds {
		vals, err := cmd.Result()
		if err != nil || len(vals) == 0 {
			continue
		}
		list := make([]int64, 0, len(vals))
		for _, v := range vals {
			n, perr := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if perr != nil {
				n = 0 // PHP intval 语义: 非数字记 0
			}
			list = append(list, n)
		}
		out[ids[i]] = list
	}
	return out
}

// nodes7dAvg 每台真实节点的 7 天日均 (bytes, 向下取整; 从未上报为 0)
func nodes7dAvg(windows map[int32][]int64, id int32) int64 {
	list := windows[id]
	if len(list) == 0 {
		return 0
	}
	var sum int64
	for _, v := range list {
		sum += v
	}
	return sum / int64(len(list)) // 非负值整数除法 = floor
}

// aggregateGroupUsableDaily aggregateGroup(g, 7*3600, usableOnly=true) 的 supply_daily:
// usableOnly —— 供给只算可用层(type=1+custom_rss=1+心跳 7h 内), 排除超配额隐藏/停用节点;
// 每节点日供给 = min(traffic_left_daily, traffic_max_day_value[为 0 回退 traffic_used_lastday])
func aggregateGroupUsableDaily(nodes []model.Node, group int32, hbCutoff int64) int64 {
	var supply int64
	for i := range nodes {
		n := &nodes[i]
		if n.NodeGroup != group || n.Type != 1 || n.CustomRSS != 1 || n.NodeHeartbeat <= hbCutoff {
			continue // is_clone=0 已在取数时过滤
		}
		maxDay := n.TrafficMaxDayValue
		if maxDay <= 0 {
			maxDay = n.TrafficUsedLastday // 冷启动/dailyjob 未填充时回退
		}
		sd := n.TrafficLeftDaily
		if maxDay < sd {
			sd = maxDay
		}
		supply += sd
	}
	return supply
}

// groupTraffic7dSeries getGroupTraffic7d(g, 7*3600): 本组活跃主节点 (is_clone=0+心跳过滤)
// 的 traffic_7d 按索引求和, 输出按日期升序 7 条 (最早→最近)
func groupTraffic7dSeries(nodes []model.Node, windows map[int32][]int64, group int32, hbCutoff int64, baseTs int64, loc *time.Location) []seriesPoint {
	var sums [7]int64
	for i := range nodes {
		n := &nodes[i]
		if n.NodeGroup != group || n.NodeHeartbeat <= hbCutoff {
			continue
		}
		for j, v := range windows[n.ID] {
			if j > 6 {
				break
			}
			sums[j] += v
		}
	}
	series := make([]seriesPoint, 0, 7)
	for i := 6; i >= 0; i-- {
		dayTs := baseTs - int64(i+1)*86400
		series = append(series, seriesPoint{
			Ts:          dayTs,
			Date:        time.Unix(dayTs, 0).In(loc).Format("01-02"), // PHP date('m-d')
			UsedLastday: sums[i],
		})
	}
	return series
}

// ════════════════════════ PHP 数值/格式化语义辅助 ════════════════════════

func round1(v float64) float64  { return math.Round(v*10) / 10 }  // PHP round(x,1)
func round2f(v float64) float64 { return math.Round(v*100) / 100 } // PHP round(x,2)

// phpFloatStr PHP 浮点转字符串语义 (无多余尾零: 87.5→"87.5", 95→"95")
func phpFloatStr(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// phpNumberFormat1 PHP number_format(x, 1): 千分位 + 恒 1 位小数
func phpNumberFormat1(v float64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	r := math.Round(v*10) / 10
	intPart := math.Floor(r)
	frac := int(math.Round((r - intPart) * 10))
	if frac == 10 { // 进位 (如 0.96→1.0)
		intPart++
		frac = 0
	}
	s := strconv.FormatInt(int64(intPart), 10)
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	if neg && (intPart > 0 || frac > 0) {
		return fmt.Sprintf("-%s.%d", b.String(), frac)
	}
	return fmt.Sprintf("%s.%d", b.String(), frac)
}

// gbFmt PHP gbFmt: number_format(bytes/2^30, 1) . 'G'
func gbFmt(bytes float64) string {
	return phpNumberFormat1(bytes/1073741824) + "G"
}

// pctOrNullF 浮点百分比字符串或 "—"
func pctOrNullF(v *float64) string {
	if v == nil {
		return "—"
	}
	return phpFloatStr(*v) + "%"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxI64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// orderedIntSet 保序去重 int 集合 (对齐 PHP 数组键插入序)
type orderedIntSet struct {
	m  map[int]bool
	sl []int
}

func newOrderedIntSet(items ...int) *orderedIntSet {
	s := &orderedIntSet{m: make(map[int]bool, len(items))}
	for _, v := range items {
		s.add(v)
	}
	return s
}

func (s *orderedIntSet) add(v int) {
	if !s.m[v] {
		s.m[v] = true
		s.sl = append(s.sl, v)
	}
}

func (s *orderedIntSet) has(v int) bool { return s.m[v] }
func (s *orderedIntSet) slice() []int   { return s.sl }

// ════════════════════════ 纯函数决策引擎 (Analytics::buildInsights) ════════════════════════

type insightItem struct {
	Sev  string `json:"sev"`  // red|yellow|green
	Rule string `json:"rule"` // R1~R10/R14
	Msg  string `json:"msg"`
}

type nodeRollT struct {
	usable, serving, cold, over, risk, cycle, ok int
	projDemand                                    int64
	costUsable                                    int64
}

type groupRowT struct {
	Users, Live30, Live24   int64
	TodayTraffic            int64
	RealAlive, RealAll      int64
	RealDead, CloneAll, Cost int64
	SupplyDaily, UsedLastday int64
	Util, PerNode            *float64
	Usable, Serving          int
	OverDen, OverCnt         int
	OverPct                  *float64
	CycleCnt, ColdCnt        int
	ProjDemand               int64
	ProjUtil                 *float64
	CovLive, CovTotal        *float64
	CostPerLive              *float64
}

type verdictT struct {
	Verdict string   `json:"verdict"` // broken|add|watch|trim|ok
	Gap     int      `json:"gap"`
	Spare   int      `json:"spare"`
	Reasons []string `json:"reasons"`
	Signals gin.H    `json:"signals"`
}

type insightsResult struct {
	Insights     []insightItem
	GroupIDs     []int          // 组序 (5..0 优先, 其余升序追加)
	Groups       map[string]gin.H
	Cells        map[string]map[string]gin.H
	Verdicts     map[string]verdictT
	Transfer     gin.H
	NodeStatus   map[int32]string
	CoverageBase gin.H
}

// buildInsightsInputs 引擎入参
type buildInsightsInputs struct {
	userMatrix   []userMatrixRow
	nodeMatrix   []nodeMatrixRow
	groupTraffic map[int]*groupTrafficRow
	th           decisionThresholds
	nodeRows     []decisionNodeRow
}

// buildInsights 决策规则引擎 (纯函数, 无 IO; R1~R9 + v2 R10/R14)
func buildInsights(in buildInsightsInputs) *insightsResult {
	th := in.th
	perClass := th.perClass
	target := th.groupTarget
	warnNode := th.onlinePerNodeWarn
	dangerNode := th.onlinePerNodeDanger
	utilWarn := th.utilWarn
	utilDanger := th.utilDanger
	utilIdle := th.utilIdle
	g5Keep := th.g5MinKeep
	minReal := th.minRealPerClass
	minClone := th.minClonePerClass
	margin := maxInt(0, th.poolMarginPct)
	overWarn := th.overNodeWarnPct
	overDanger := th.overNodeDangerPct
	projMargin := maxInt(0, th.projOverMargin)
	trimPerNode := th.trimMaxPerNode
	keepDefault := th.minKeepDefault
	trendDays := th.trendDaysWarn

	// ---- 索引化: U[g][c] / N[g][c] ----
	type uCell struct {
		maxc              int
		users, live30, live24 int64
	}
	type nCell struct {
		realAll, realAlive, cloneAll, cloneAlive, cost int64
	}
	U := map[int]map[int]*uCell{}
	for _, r := range in.userMatrix {
		if U[r.G] == nil {
			U[r.G] = map[int]*uCell{}
		}
		if cur, ok := U[r.G][r.C]; ok {
			// 防御: 列位合并后同格多行累加, maxc 取真实等级最大值
			cur.users += r.Users
			cur.live30 += r.Live30
			cur.live24 += r.Live24
			if r.Maxc > cur.maxc {
				cur.maxc = r.Maxc
			}
		} else {
			U[r.G][r.C] = &uCell{maxc: maxInt(r.C, r.Maxc), users: r.Users, live30: r.Live30, live24: r.Live24}
		}
	}
	N := map[int]map[int]*nCell{}
	for _, r := range in.nodeMatrix {
		if N[r.G] == nil {
			N[r.G] = map[int]*nCell{}
		}
		if cur, ok := N[r.G][r.C]; ok {
			cur.realAll += r.RealAll
			cur.realAlive += r.RealAlive
			cur.cloneAll += r.CloneAll
			cur.cloneAlive += r.CloneAlive
			cur.cost += r.Cost
		} else {
			N[r.G][r.C] = &nCell{
				realAll: r.RealAll, realAlive: r.RealAlive,
				cloneAll: r.CloneAll, cloneAlive: r.CloneAlive, cost: r.Cost,
			}
		}
	}

	// ---- 组集合: 固定 5..0 优先, 其余升序追加 ----
	set := newOrderedIntSet(5, 4, 3, 2, 1, 0)
	for g := range U {
		set.add(g)
	}
	for g := range N {
		set.add(g)
	}
	for g := range in.groupTraffic {
		set.add(g)
	}
	for i := range in.nodeRows { // v2: 节点行所在组也进组集合
		set.add(in.nodeRows[i].G)
	}
	var extras []int
	for _, g := range set.slice() {
		if g != 5 && g != 4 && g != 3 && g != 2 && g != 1 && g != 0 {
			extras = append(extras, g)
		}
	}
	sort.Ints(extras)
	groups := []int{5, 4, 3, 2, 1, 0}
	groups = append(groups, extras...)

	classes := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	// ---- v2 预处理: 节点级分层(③超额隐藏/4冷启动/5可用) + 日配额/周期判定 + 组内汇总 ----
	//   over  今日已用 > 日配额 (离 custom_rss=0 只差一次上报)
	//   risk  全天外推 > 配额×(1+余量%) (比系统隐藏早半天的预警)
	//   cycle 限额节点 left/avg7 < 周期剩余天数 (月配额撑不到重置日)
	//   ok    其余; layer4(冷启动) 不进 R10 分母, 单独计数
	nodeStatus := map[int32]string{}
	nodeRoll := map[int]*nodeRollT{}
	hasNodeRows := len(in.nodeRows) > 0
	for i := range in.nodeRows {
		nr := &in.nodeRows[i]
		roll := nodeRoll[nr.G]
		if roll == nil {
			roll = &nodeRollT{}
			nodeRoll[nr.G] = roll
		}
		if nr.Layer == 4 {
			// 冷启动: 在订阅池内服役(计可用/成本), 但三数未就绪不进 R10 分母
			roll.cold++
			roll.usable++
			roll.costUsable += nr.Cost
			nodeStatus[nr.ID] = "cold"
			continue
		}
		// layer 3(超配额隐藏, 存量连接仍在服役) / 5(可用): 进 R10 分母
		var status string
		switch {
		case nr.TodayUsed > nr.Quota:
			status = "over"
		case nr.Quota > 0 && float64(nr.ProjToday) > float64(nr.Quota)*(1+float64(projMargin)/100):
			status = "risk"
		case nr.Avg7 > 0 && nr.Left > 0 && float64(nr.Left)/float64(nr.Avg7) < float64(nr.DaysRemaining):
			status = "cycle"
		default:
			status = "ok"
		}
		roll.serving++
		if nr.Layer == 5 {
			roll.usable++
			roll.costUsable += nr.Cost
		}
		switch status {
		case "over":
			roll.over++
		case "risk":
			roll.risk++
		case "cycle":
			roll.cycle++
		default:
			roll.ok++
		}
		roll.projDemand += nr.ProjToday
		nodeStatus[nr.ID] = status
	}

	// ---- 每组聚合 + 前缀和(池/活跃真实) + 后缀和(用户/在线) ----
	groupRows := map[int]*groupRowT{}
	poolCum := map[int][11]int64{}       // [g][c] = Σ_{c'<=c} (real_all+clone_all) 与订阅可见池同口径
	realAliveCum := map[int][11]int64{}  // [g][c] = Σ_{c'<=c} real_alive
	usersAtOrAbove := map[int][11]int64{} // [g][c] = Σ_{c'>=c} users
	live30AtOrAbove := map[int][11]int64{}
	for _, g := range groups {
		sum := &groupRowT{}
		for _, c := range classes {
			if u := U[g][c]; u != nil {
				sum.Users += u.users
				sum.Live30 += u.live30
				sum.Live24 += u.live24
			}
			if n := N[g][c]; n != nil {
				sum.RealAll += n.realAll
				sum.RealAlive += n.realAlive
				sum.CloneAll += n.cloneAll
				sum.Cost += n.cost
			}
		}
		sum.RealDead = sum.RealAll - sum.RealAlive
		var supply, used int64
		if gt := in.groupTraffic[g]; gt != nil {
			supply = gt.SupplyDaily
			used = gt.UsedLastday
			// 今日实时流量: 节点游标口径 (仅 is_clone=0 真实节点, 见 getGroupTodayTraffic)
			sum.TodayTraffic = gt.Today
		}
		sum.SupplyDaily = supply
		sum.UsedLastday = used
		if supply > 0 {
			v := round1(float64(used) / float64(supply) * 100)
			sum.Util = &v
		}
		if sum.RealAlive > 0 {
			v := round1(float64(sum.Live30) / float64(sum.RealAlive))
			sum.PerNode = &v
		}
		// v2 字段: 可用层/超额比例/今日外推/每$覆盖 (nodeRows 为空时全为 0/null, 与 v1 同形)
		if roll := nodeRoll[g]; roll != nil {
			sum.Usable = roll.usable
			sum.Serving = roll.serving
			sum.OverDen = roll.serving
			sum.OverCnt = roll.over + roll.risk
			if roll.serving > 0 {
				v := round1(float64(roll.over+roll.risk) / float64(roll.serving) * 100)
				sum.OverPct = &v
			}
			sum.CycleCnt = roll.cycle
			sum.ColdCnt = roll.cold
			sum.ProjDemand = roll.projDemand
			if supply > 0 {
				v := round1(float64(roll.projDemand) / float64(supply) * 100)
				sum.ProjUtil = &v
			}
			costU := roll.costUsable
			if costU > 0 {
				v := round1(float64(sum.Live24) / float64(costU))
				sum.CovLive = &v
				v2 := round1(float64(sum.Users) / float64(costU))
				sum.CovTotal = &v2
				if sum.Live24 > 0 {
					v3 := round2f(float64(costU) / float64(sum.Live24))
					sum.CostPerLive = &v3
				}
			}
		}
		groupRows[g] = sum

		var pool, ra int64
		for _, c := range classes {
			if n := N[g][c]; n != nil {
				pool += n.realAll + n.cloneAll
				ra += n.realAlive
			}
			poolCum[g][c] = pool
			realAliveCum[g][c] = ra
		}
		var ua, la int64
		for i := 10; i >= 0; i-- {
			if u := U[g][i]; u != nil {
				ua += u.users
				la += u.live30
			}
			usersAtOrAbove[g][i] = ua
			live30AtOrAbove[g][i] = la
		}
	}

	// ---- 节点池矩阵 cells ----
	cells := map[string]map[string]gin.H{}
	for _, g := range groups {
		gs := strconv.Itoa(g)
		cells[gs] = map[string]gin.H{}
		for _, c := range classes {
			var users, live30 int64
			maxc := c
			if u := U[g][c]; u != nil {
				users = u.users
				live30 = u.live30
				maxc = maxInt(c, u.maxc)
			}
			pool := poolCum[g][c]
			// need 展示口径 = 等级×per_class (不含隐藏保底基数 base, 实际生效上限
			// 见 User::effectiveSubLimit = base + 等级×per_class); 等级取格内真实
			// 最高值 maxc (class 11/99/111 不按 10 计)
			var need *int
			if perClass > 0 {
				v := maxc * perClass
				need = &v
			}
			var needLim int64 = math.MaxInt64
			if need != nil {
				needLim = int64(*need)
			}
			sev := "green"
			switch {
			case users <= 0:
				sev = "none"
			case need != nil && *need <= 0:
				sev = "none" // 需0 (L0): 该级用户一个都不下发, 无暴露风险, 不判定
			case pool <= 0:
				sev = "red"
			case need != nil && pool <= needLim:
				sev = "red"
			case need != nil && pool <= int64(math.Ceil(float64(*need)*(1+float64(margin)/100))):
				sev = "yellow"
			}
			var realN, cloneN int64
			if n := N[g][c]; n != nil {
				realN = n.realAll
				cloneN = n.cloneAll
			}
			var needOut interface{}
			if need != nil {
				needOut = *need
			}
			cells[gs][strconv.Itoa(c)] = gin.H{
				"pool":   pool,
				"need":   needOut,
				"users":  users,
				"live30": live30,
				"real":   realN,
				"clone":  cloneN,
				"sev":    sev,
			}
		}
	}

	// ---- 建议列表 (sev + 规则号用于排序) ----
	insights := make([]insightItem, 0, 64)
	push := func(sev, rule, msg string) {
		insights = append(insights, insightItem{Sev: sev, Rule: rule, Msg: msg})
	}

	getCell := func(g, c int) gin.H { return cells[strconv.Itoa(g)][strconv.Itoa(c)] }
	i64 := func(v interface{}) int64 {
		switch x := v.(type) {
		case int64:
			return x
		case int:
			return int64(x)
		}
		return 0
	}

	// R9 节点池被拿满 (防墙红线, 最高优先)
	for _, g := range groups {
		if g == 0 {
			continue // 0组特殊/radius, 不参与判定
		}
		for _, c := range classes {
			cell := getCell(g, c)
			users := i64(cell["users"])
			if users <= 0 {
				continue
			}
			need, hasNeed := cell["need"].(int)
			pool := i64(cell["pool"])
			if hasNeed && need <= 0 {
				continue // 需0 (L0): 无暴露风险, 不判红
			}
			if pool <= 0 {
				push("red", "R9", fmt.Sprintf("G%d L%d 节点池为 0 —— 该级 %d 名用户无可下发节点", g, c, users))
			} else if hasNeed && pool <= int64(need) {
				push("red", "R9", fmt.Sprintf("G%d L%d 池 %d ≤ 需 %d —— 该级用户将拿满全池, 缺 %d 个节点(clone 即可补足)", g, c, pool, need, int64(need)-pool))
			} else if hasNeed && cell["sev"] == "yellow" {
				pct := 100
				if need > 0 {
					pct = int(math.Round((float64(pool) - float64(need)) / float64(need) * 100))
				}
				push("yellow", "R9", fmt.Sprintf("G%d L%d 池 %d 贴线(需 %d, 余量 %d%%) —— 建议补 clone", g, c, pool, need, pct))
			}
		}
	}

	// R4 L1~L5 缺真实节点 (该级+有用户)
	for _, g := range groups {
		if g == 0 {
			continue
		}
		for c := 1; c <= 5; c++ {
			var realAlive int64
			if n := N[g][c]; n != nil {
				realAlive = n.realAlive
			}
			ua := usersAtOrAbove[g][c]
			if ua > 0 && realAlive < int64(minReal) {
				push("red", "R4", fmt.Sprintf("G%d 缺 L%d 活跃真实节点 (L%d+ 用户 %d 人, 该级活跃真实节点 %d) —— 等级=月价, L1~L5 需真实节点支撑", g, c, c, ua, realAlive))
			}
		}
	}

	// R8 累计覆盖不足: Lc+ 在线 / Lc 累计活跃真实节点
	for _, g := range groups {
		if g == 0 {
			continue
		}
		for c := 1; c <= 5; c++ {
			la := live30AtOrAbove[g][c]
			rc := realAliveCum[g][c]
			if rc > 0 && float64(la)/float64(rc) > float64(warnNode) {
				ratio := round1(float64(la) / float64(rc))
				push("yellow", "R8", fmt.Sprintf("G%d L%d+ 在线 %d 人 / 累计活跃真实节点 %d 台 ≈ %s 人/台 —— 建议该组加节点或从 G5 迁入", g, c, la, rc, phpFloatStr(ratio)))
			}
		}
	}

	// R1/R2/R3/R7/R5/R6 组级
	for _, g := range groups {
		row := groupRows[g]
		if g == 0 {
			continue // 0组特殊, 只展示不判定
		}
		// R1 用户超编
		if target > 0 && row.Users > int64(target) {
			pct := int(math.Round(float64(row.Users) / float64(target) * 100))
			sev := "yellow"
			if float64(row.Users) > float64(target)*1.2 {
				sev = "red"
			}
			push(sev, "R1", fmt.Sprintf("G%d 用户 %d 超目标 %d (%d%%) —— 建议收紧降档阈值或扩容", g, row.Users, target, pct))
		}
		// R2 节点负载
		if row.RealAlive <= 0 && row.Live30 > 0 {
			push("red", "R2", fmt.Sprintf("G%d 在线 %d 人但无活跃真实节点", g, row.Live30))
		} else if row.PerNode != nil && *row.PerNode > float64(dangerNode) {
			push("red", "R2", fmt.Sprintf("G%d 在线 %d 人 / 活跃真实节点 %d 台 ≈ %s 人/台, 高于红线 %d", g, row.Live30, row.RealAlive, phpFloatStr(*row.PerNode), dangerNode))
		} else if row.PerNode != nil && *row.PerNode > float64(warnNode) {
			push("yellow", "R2", fmt.Sprintf("G%d 在线 %d 人 / 活跃真实节点 %d 台 ≈ %s 人/台, 高于阈值 %d", g, row.Live30, row.RealAlive, phpFloatStr(*row.PerNode), warnNode))
		}
		// R3 流量吃紧 (v2: 昨日 util + 今日外推 util 双信号, 外推只补位不重复报警)
		if row.Util != nil && *row.Util > float64(utilDanger) {
			push("red", "R3", fmt.Sprintf("G%d 昨日用量 %s / 供给日均 %s = %s%%, 超红线 %d%% —— 流量不够, 建议加节点或提限额", g, gbFmt(float64(row.UsedLastday)), gbFmt(float64(row.SupplyDaily)), phpFloatStr(*row.Util), utilDanger))
		} else if row.ProjUtil != nil && *row.ProjUtil > float64(utilDanger) {
			push("red", "R3", fmt.Sprintf("G%d 今日已用 %s 外推全天 %s / 供给日均 %s = %s%%, 预计今日超红线 %d%% —— 流量不够", g, gbFmt(float64(row.TodayTraffic)), gbFmt(float64(row.ProjDemand)), gbFmt(float64(row.SupplyDaily)), phpFloatStr(*row.ProjUtil), utilDanger))
		} else if row.Util != nil && *row.Util > float64(utilWarn) {
			push("yellow", "R3", fmt.Sprintf("G%d 昨日用量 %s / 供给日均 %s = %s%%, 超阈值 %d%%", g, gbFmt(float64(row.UsedLastday)), gbFmt(float64(row.SupplyDaily)), phpFloatStr(*row.Util), utilWarn))
		} else if row.ProjUtil != nil && *row.ProjUtil > float64(utilWarn) {
			push("yellow", "R3", fmt.Sprintf("G%d 今日外推 %s / 供给日均 %s = %s%%, 预计今日超阈值 %d%%", g, gbFmt(float64(row.ProjDemand)), gbFmt(float64(row.SupplyDaily)), phpFloatStr(*row.ProjUtil), utilWarn))
		} else if row.UsedLastday > 0 && row.SupplyDaily <= 0 {
			push("yellow", "R3", fmt.Sprintf("G%d 有昨日用量 %s 但供给日均统计为 0 (活跃节点不足或未上报)", g, gbFmt(float64(row.UsedLastday))))
		}
		// R7 离线节点
		if row.RealDead > 0 {
			push("yellow", "R7", fmt.Sprintf("G%d 有 %d 台真实节点离线超 7 小时 (已从供给/负载统计剔除)", g, row.RealDead))
		}
		// R5 L6~L10 缺 clone (该级+有用户)
		for c := 6; c <= 10; c++ {
			var clone int64
			if n := N[g][c]; n != nil {
				clone = n.cloneAll
			}
			if usersAtOrAbove[g][c] > 0 && clone < int64(minClone) {
				push("yellow", "R5", fmt.Sprintf("G%d 缺 L%d clone 节点 (L%d+ 用户 %d 人) —— L6~L10 靠 clone 充数", g, c, c, usersAtOrAbove[g][c]))
			}
		}
		// R6 G5 冗余池
		if g == 5 && row.Util != nil && *row.Util < float64(utilIdle) && row.RealAlive > int64(g5Keep) {
			push("green", "R6", fmt.Sprintf("G5 利用率 %s%% (低于 %d%%), 冗余 %d 台真实节点可迁出 (建议保留 ≥%d 台) —— 见下方迁移候选", phpFloatStr(*row.Util), utilIdle, row.RealAlive, g5Keep))
		}
	}

	// R10 v2: 组内超/将超日配额节点比例 —— 供给不够的直接证据
	// (分母=③+⑤层即心跳内服役节点, 故障离线/停用/冷启动均已排除; 分子=超额+风险)
	for _, g := range groups {
		if g == 0 {
			continue
		}
		row := groupRows[g]
		if row.OverDen > 0 && row.OverPct != nil {
			if *row.OverPct >= float64(overDanger) {
				push("red", "R10", fmt.Sprintf("G%d 有 %d/%d 台节点今日超/将超日配额 —— 组流量不够, 建议扩容或提限额", g, row.OverCnt, row.OverDen))
			} else if *row.OverPct >= float64(overWarn) {
				push("yellow", "R10", fmt.Sprintf("G%d 有 %d/%d 台节点今日超/将超日配额, 建议关注流量分配", g, row.OverCnt, row.OverDen))
			}
		}
	}

	// R14 v2: 分组扩容/迁移裁决 (每组唯一结论; 依据引用 R2/R3/R7/R10)
	//   P(单台日供给)=supply/usable;  need=max(昨日, 今日外推, 7天日均);
	//   gap=ceil((need-supply)/P);    spare=min(usable-keep, floor((supply-need/util_idle)/P))
	//   判定按序短路: broken(无可用) > add(红线信号) > watch(黄线/趋势) > trim(闲置) > ok
	verdicts := map[string]verdictT{}
	orderedVerdicts := make([]verdictT, 0, len(groups))
	totGap, totSpare := 0, 0
	addGroups, trimGroups := []string{}, []string{}
	if hasNodeRows {
		for _, g := range groups {
			if g == 0 {
				continue // 0组特殊, 不参与裁决
			}
			row := groupRows[g]
			supply := row.SupplyDaily
			usable := row.Usable
			var avg7d int64
			var daysTo95 *int
			if gt := in.groupTraffic[g]; gt != nil {
				avg7d = gt.Avg7d
				daysTo95 = gt.DaysTo95
			}
			P := 0.0
			if usable > 0 && supply > 0 {
				P = float64(supply) / float64(usable)
			}
			need := maxI64(row.UsedLastday, maxI64(row.ProjDemand, avg7d))
			gap := 0
			if need > supply {
				// P=0(冷启动/供给未统计): 只知缺, 至少缺 1 台
				if P > 0 {
					gap = int(math.Ceil((float64(need) - float64(supply)) / P))
				} else {
					gap = 1
				}
			}
			keep := keepDefault
			if g == 5 {
				keep = g5Keep
			}
			spare := 0
			if P > 0 && usable > keep {
				// 迁走 k 台后组 util 升至刚好 idle% 为上限 —— 只捐真正闲的供给
				kUtil := int(math.Floor((float64(supply) - float64(need)*100/float64(maxInt(1, utilIdle))) / P))
				spare = maxInt(0, minInt(usable-keep, kUtil))
			}

			reasons := []string{}
			r4Missing := []string{}
			var verdict string
			if row.Users > 0 && usable <= 0 {
				verdict = "broken"
				reasons = append(reasons, "全组 0 台心跳内真实节点")
				if row.RealDead > 0 {
					reasons = append(reasons, fmt.Sprintf("离线 %d 台(R7)", row.RealDead))
				}
				if row.Live30 > 0 {
					reasons = append(reasons, fmt.Sprintf("仍有 %d 人在线", row.Live30))
				}
			} else {
				addHit := []string{}
				watchHit := []string{}
				// R4: 该组 L1~L5 某级+ 有用户但该级无活跃真实节点 —— 付费级缺口,
				// 必须真实节点(加节点或调级), 与 R9(clone 可补) 不同类, 直接构成 add 依据
				for c := 1; c <= 5; c++ {
					var realAliveC int64
					if n := N[g][c]; n != nil {
						realAliveC = n.realAlive
					}
					if usersAtOrAbove[g][c] > 0 && realAliveC < int64(minReal) {
						r4Missing = append(r4Missing, fmt.Sprintf("L%d", c))
					}
				}
				if len(r4Missing) > 0 {
					addHit = append(addHit, "缺 "+strings.Join(r4Missing, "/")+" 真实节点(R4)")
				}
				if row.Util != nil && *row.Util > float64(utilDanger) {
					addHit = append(addHit, fmt.Sprintf("昨日 util %s%%(R3)", phpFloatStr(*row.Util)))
				}
				if row.ProjUtil != nil && *row.ProjUtil > float64(utilDanger) {
					addHit = append(addHit, fmt.Sprintf("今日外推 %s%%(R3)", phpFloatStr(*row.ProjUtil)))
				}
				if row.OverPct != nil && *row.OverPct >= float64(overDanger) {
					addHit = append(addHit, fmt.Sprintf("超额 %d/%d 台(R10)", row.OverCnt, row.OverDen))
				}
				if row.PerNode != nil && *row.PerNode > float64(dangerNode) {
					addHit = append(addHit, fmt.Sprintf("人/台 %s(R2)", phpFloatStr(*row.PerNode)))
				}
				if row.Util != nil && *row.Util > float64(utilWarn) && *row.Util <= float64(utilDanger) {
					watchHit = append(watchHit, fmt.Sprintf("昨日 util %s%%(R3)", phpFloatStr(*row.Util)))
				}
				if row.ProjUtil != nil && *row.ProjUtil > float64(utilWarn) && *row.ProjUtil <= float64(utilDanger) {
					watchHit = append(watchHit, fmt.Sprintf("今日外推 %s%%", phpFloatStr(*row.ProjUtil)))
				}
				if row.OverPct != nil && *row.OverPct >= float64(overWarn) && *row.OverPct < float64(overDanger) {
					watchHit = append(watchHit, fmt.Sprintf("超额 %d/%d 台(R10)", row.OverCnt, row.OverDen))
				}
				if row.PerNode != nil && *row.PerNode > float64(warnNode) && *row.PerNode <= float64(dangerNode) {
					watchHit = append(watchHit, fmt.Sprintf("人/台 %s(R2)", phpFloatStr(*row.PerNode)))
				}
				if daysTo95 != nil && *daysTo95 >= 0 && *daysTo95 < trendDays {
					watchHit = append(watchHit, fmt.Sprintf("趋势约 %d 天后 util 到 %d%%", *daysTo95, utilDanger))
				}
				if len(addHit) > 0 {
					// 红线信号即缺节点: 流量缺口按 P 估算, 纯负载缺口至少 1 台;
					// R4 付费级缺口按缺一级补一台计
					verdict = "add"
					if len(r4Missing) > 0 {
						gap = maxInt(gap, len(r4Missing))
					} else {
						gap = maxInt(gap, 1)
					}
					reasons = addHit
					if P > 0 {
						reasons = append(reasons, fmt.Sprintf("单台日供给 %s", gbFmt(P)))
					} else if supply <= 0 {
						reasons = append(reasons, "供给待预热(冷启动)")
					}
				} else if len(watchHit) > 0 {
					verdict = "watch"
					reasons = watchHit
				} else if (row.Util == nil || *row.Util < float64(utilIdle)) &&
					(row.PerNode == nil || *row.PerNode < float64(trimPerNode)) &&
					spare > 0 {
					verdict = "trim"
					if row.Util != nil {
						reasons = append(reasons, fmt.Sprintf("util %s%% < %d%%", phpFloatStr(*row.Util), utilIdle))
					} else {
						reasons = append(reasons, fmt.Sprintf("util 无用量 < %d%%", utilIdle))
					}
					if row.PerNode != nil {
						reasons = append(reasons, fmt.Sprintf("人/台 %s", phpFloatStr(*row.PerNode)))
					}
					reasons = append(reasons, fmt.Sprintf("迁出后 util 仍 ≤%d%%", utilIdle))
				} else {
					verdict = "ok"
					if row.ColdCnt > 0 {
						reasons = append(reasons, fmt.Sprintf("%d 台冷启动待预热(dailyjob 未跑)", row.ColdCnt))
					}
				}
			}
			overSig := "—"
			if row.OverDen > 0 {
				overSig = fmt.Sprintf("%d/%d", row.OverCnt, row.OverDen)
			}
			v := verdictT{
				Verdict: verdict,
				Gap:     gap,
				Spare:   spare,
				Reasons: reasons,
				Signals: gin.H{
					"usable":    usable,
					"serving":   row.Serving,
					"over":      overSig,
					"util":      row.Util,
					"proj_util": row.ProjUtil,
					"per_node":  row.PerNode,
					"cov_live":  row.CovLive,
					"supply":    supply,
					"need":      need,
					"r4":        len(r4Missing),
				},
			}
			verdicts[strconv.Itoa(g)] = v
			orderedVerdicts = append(orderedVerdicts, v)
			if verdict == "add" {
				totGap += gap
				addGroups = append(addGroups, fmt.Sprintf("G%d 缺%d台", g, gap))
			} else if verdict == "trim" {
				totSpare += spare
				trimGroups = append(trimGroups, fmt.Sprintf("G%d 可迁%d台", g, spare))
			}
		}
	}

	// R14 裁决条目 (排在 R9 防墙红线之后, 见下方 ruleRank)
	for i, v := range orderedVerdicts {
		g := groups[i+1] // orderedVerdicts 与 groups 中 g != 0 的子序列一一对应
		why := strings.Join(v.Reasons, " · ")
		switch v.Verdict {
		case "broken":
			push("red", "R14", fmt.Sprintf("G%d 无可用真实节点 —— %s (修复故障或补节点)", g, why))
		case "add":
			push("red", "R14", fmt.Sprintf("G%d 缺 %d 台 ← %s", g, v.Gap, why))
		case "watch":
			push("yellow", "R14", fmt.Sprintf("G%d 观察期 ← %s", g, why))
		case "trim":
			push("green", "R14", fmt.Sprintf("G%d 冗余, 可迁出 %d 台 ← %s", g, v.Spare, why))
		}
	}

	// 全站调配总纲 (F 区表尾)
	var transferMsg interface{}
	if totGap > 0 && totSpare >= totGap {
		transferMsg = fmt.Sprintf("全站缺口 %d 台 ≤ 冗余 %d 台, 可全内部解决: %s → %s",
			totGap, totSpare, strings.Join(trimGroups, "、"), strings.Join(addGroups, "、"))
	} else if totGap > 0 {
		if totSpare > 0 {
			transferMsg = fmt.Sprintf("全站缺口 %d 台 > 冗余 %d 台: 优先从冗余组迁 %d 台, 再采购 %d 台 (%s → %s)",
				totGap, totSpare, totSpare, totGap-totSpare,
				strings.Join(trimGroups, "、"), strings.Join(addGroups, "、"))
		} else {
			transferMsg = fmt.Sprintf("全站缺口 %d 台且无冗余组可迁: 需采购 %d 台真实节点 (→ %s)",
				totGap, totGap, strings.Join(addGroups, "、"))
		}
	} else if totSpare > 0 {
		transferMsg = fmt.Sprintf("全站无缺口, 冗余 %d 台: %s (候选沉放 G5)",
			totSpare, strings.Join(trimGroups, "、"))
	}
	transfer := gin.H{"total_gap": totGap, "total_spare": totSpare, "msg": transferMsg}

	// 全站每$覆盖基准 (仅可用层成本; live24 含无成本组)
	var covLive, covPerLive *float64
	var tc, tl int64
	if hasNodeRows {
		for _, g := range groups {
			tl += groupRows[g].Live24
			if r := nodeRoll[g]; r != nil {
				tc += r.costUsable
			}
		}
		if tc > 0 && tl > 0 {
			v := round1(float64(tl) / float64(tc))
			covLive = &v
			v2 := round2f(float64(tc) / float64(tl))
			covPerLive = &v2
		}
	}
	coverageBase := gin.H{"cov_live": covLive, "cost_per_live": covPerLive, "cost": tc, "live24": tl}

	// ---- 排序: red(0) < yellow(1) < green(2); 同级按规则优先级 R9<R14<R10<R4<R2<R3<R1<R8<R7<R5<R6 ----
	sevRank := map[string]int{"red": 0, "yellow": 1, "green": 2}
	ruleRank := map[string]int{"R9": 0, "R14": 1, "R10": 2, "R4": 3, "R2": 4, "R3": 5, "R1": 6, "R8": 7, "R7": 8, "R5": 9, "R6": 10}
	sort.SliceStable(insights, func(a, b int) bool {
		rank := func(it insightItem) int {
			rr := 50
			if v, ok := ruleRank[it.Rule]; ok {
				rr = v
			}
			return sevRank[it.Sev]*100 + rr
		}
		return rank(insights[a]) < rank(insights[b])
	})

	// groups 原始行输出 (v2 另含 usable/over_pct/proj_util/cov_live 等)
	groupsOut := make(map[string]gin.H, len(groups))
	for _, g := range groups {
		row := groupRows[g]
		groupsOut[strconv.Itoa(g)] = gin.H{
			"users":         row.Users,
			"live30":        row.Live30,
			"live24":        row.Live24,
			"today_traffic": row.TodayTraffic,
			"real_alive":    row.RealAlive,
			"real_all":      row.RealAll,
			"real_dead":     row.RealDead,
			"clone_all":     row.CloneAll,
			"cost":          row.Cost,
			"supply_daily":  row.SupplyDaily,
			"used_lastday":  row.UsedLastday,
			"util":          row.Util,
			"per_node":      row.PerNode,
			"usable":        row.Usable,
			"serving":       row.Serving,
			"over_den":      row.OverDen,
			"over_cnt":      row.OverCnt,
			"over_pct":      row.OverPct,
			"cycle_cnt":     row.CycleCnt,
			"cold_cnt":      row.ColdCnt,
			"proj_demand":   row.ProjDemand,
			"proj_util":     row.ProjUtil,
			"cov_live":      row.CovLive,
			"cov_total":     row.CovTotal,
			"cost_per_live": row.CostPerLive,
		}
	}

	return &insightsResult{
		Insights:     insights,
		GroupIDs:     groups,
		Groups:       groupsOut,
		Cells:        cells,
		Verdicts:     verdicts,
		Transfer:     transfer,
		NodeStatus:   nodeStatus,
		CoverageBase: coverageBase,
	}
}

// ════════════════════════ HTTP Handler ════════════════════════

// NodeDecision 节点决策台 — GET /api/v2/admin/stats/node-decision
// 分组×等级 节点池/流量/用户 决策数据: R9 防墙红线置顶, v2 节点级流量够用判定(R10)、
// 每$覆盖、分组扩容/迁移裁决(R14) 与全站调配总纲; 纯只读。
func (h *Handler) NodeDecision(c *gin.Context) {
	ctx := c.Request.Context()
	now := time.Now() // 本地时区 (与 PHP date()/time() 同口径)
	nowTs := now.Unix()
	loc := now.Location()
	h7 := nowTs - 7*3600

	// 取数: 两条矩阵 + alive_ip 实时在线 + 今日流量 (SQL 语义照搬 Analytics)
	userMatrix := fetchUserGroupClassMatrix(nowTs)
	nodeMatrix := fetchNodeGroupClassMatrix(h7)
	aliveMap := fetchAliveUserByNode(nowTs)
	todayMap := fetchGroupTodayTraffic()

	// v2: 节点级三数的两个基准 —— dailyjob 执行时刻(日内时间进度) 与 每节点 7 天日均
	dayProgBase := dailyJobBaseTs(now)
	dayProg := float64(nowTs-dayProgBase) / 86400
	if dayProg < 0.05 {
		dayProg = 0.05
	}
	if dayProg > 1.0 {
		dayProg = 1.0
	}

	// 全量真实节点 (is_clone=0) 一次取出, 供 组聚合/7d 序列/7d 日均/分层三数/停用表 共用
	var allReal []model.Node
	database.DB.Where("is_clone = 0").Order("id ASC").Find(&allReal)
	ids := make([]int32, 0, len(allReal))
	for i := range allReal {
		ids = append(ids, allReal[i].ID)
	}
	windows := pipelineTraffic7d(ctx, ids)
	baseTs := traffic7dBaseTs(now)

	// 组集合: 固定 5..0 优先, 矩阵出现的组追加 (与 buildInsights 组序一致)
	groupSet := newOrderedIntSet(5, 4, 3, 2, 1, 0)
	for _, r := range userMatrix {
		groupSet.add(r.G)
	}
	for _, r := range nodeMatrix {
		groupSet.add(r.G)
	}

	th := decisionPresets
	utilDangerTh := th.utilDanger

	traffic := map[int]*groupTrafficRow{}
	series7d := map[int][]seriesPoint{}
	for _, g := range groupSet.slice() {
		gi := int32(g)
		// v2: usableOnly —— 供给只算可用层(type=1+custom_rss=1+心跳), 排除超配额隐藏/停用节点
		supplyDaily := aggregateGroupUsableDaily(allReal, gi, h7)
		series := groupTraffic7dSeries(allReal, windows, gi, h7, baseTs, loc)
		var lastUsed int64
		if len(series) > 0 {
			lastUsed = series[len(series)-1].UsedLastday
		}
		// v2: 7天日均(活跃日均值, 全零日不拖累) + 趋势外推距 95% 红线天数 (OLS 斜率)
		ys := make([]float64, len(series))
		for i, pt := range series {
			ys[i] = float64(pt.UsedLastday)
		}
		var sumNz float64
		cntNz := 0
		for _, y := range ys {
			if y != 0 {
				sumNz += y
				cntNz++
			}
		}
		var avg7d int64
		if cntNz > 0 {
			avg7d = int64(math.Round(sumNz / float64(cntNz)))
		}
		var daysTo95 *int
		n := len(ys)
		if supplyDaily > 0 && n >= 2 {
			xB := float64(n-1) / 2
			var sumY float64
			for _, y := range ys {
				sumY += y
			}
			yB := sumY / float64(n)
			var num, den float64
			for i, y := range ys {
				num += (float64(i) - xB) * (y - yB)
				den += (float64(i) - xB) * (float64(i) - xB)
			}
			slope := 0.0
			if den > 0 {
				slope = num / den
			}
			if slope > 0 {
				days := (float64(supplyDaily)*float64(utilDangerTh)/100 - ys[n-1]) / slope
				d := int(math.Ceil(days))
				if days < 0 {
					d = 0
				}
				daysTo95 = &d
			}
		}
		var today int64
		if v, ok := todayMap[g]; ok {
			today = v
		}
		traffic[g] = &groupTrafficRow{
			SupplyDaily: supplyDaily,
			UsedLastday: lastUsed,
			Today:       today,
			Avg7d:       avg7d,
			DaysTo95:    daysTo95,
		}
		series7d[g] = series
	}

	// E 区取数 + v2 节点级行: 一次查询分区
	// (离线 / G5 迁移候选 / 超配额隐藏 / 配置异常 / 每节点实时在线)
	// 分层: ①离线(心跳>7h) 2停用(type=0, 不在此列) ③超配额隐藏(custom_rss=0 且日耗>日配)
	//       4冷启动(quota=0 且今日无用量) 5可用(其余) —— 4 不进 R10 分母
	realNodes := make([]*model.Node, 0, len(allReal))
	for i := range allReal {
		if allReal[i].Type == 1 {
			realNodes = append(realNodes, &allReal[i])
		}
	}
	// ORDER BY node_group DESC, node_class ASC, id ASC
	sort.SliceStable(realNodes, func(a, b int) bool {
		x, y := realNodes[a], realNodes[b]
		if x.NodeGroup != y.NodeGroup {
			return x.NodeGroup > y.NodeGroup
		}
		if x.NodeClass != y.NodeClass {
			return x.NodeClass < y.NodeClass
		}
		return x.ID < y.ID
	})

	var offlineNodes, nodeAliveRows, g5Candidates []*nodeDisplayRow
	var overquotaHidden, configHidden []*nodeDisplayRow
	var nodeRows []decisionNodeRow
	for _, n := range realNodes {
		used, lastday := n.TrafficUsed, n.TrafficUsedLastday
		// 今日已用: 日游标差 (重置/回退时钉住 used, 与 getGroupTodayTraffic 同款钳制)
		var todayUsed int64
		if used >= lastday {
			todayUsed = used - lastday
		} else {
			todayUsed = used
		}
		quota := n.TrafficLeftDaily
		var projToday int64
		if dayProg > 0 {
			projToday = int64(float64(todayUsed) / dayProg)
		} else {
			projToday = todayUsed
		}
		heartbeatMin := int64(-1)
		if n.NodeHeartbeat > 0 {
			heartbeatMin = int64(math.Round(float64(nowTs-n.NodeHeartbeat) / 60))
		}
		var todayPct *int
		if quota > 0 {
			v := int(math.Min(999, math.Round(float64(todayUsed)/float64(quota)*100)))
			todayPct = &v
		}
		row := &nodeDisplayRow{
			ID: n.ID, Name: n.Name, Group: n.NodeGroup, Class: n.NodeClass, Cost: n.NodeCost,
			Alive:        aliveMap[n.ID],
			HeartbeatMin: heartbeatMin,
			UsedLastday:  n.TrafficUsedLastday,
			UsedGB:       round1(float64(n.TrafficUsedLastday) / 1073741824),
			TodayGB:      round1(float64(todayUsed) / 1073741824),
			QuotaGB:      round1(float64(quota) / 1073741824),
			TodayPct:     todayPct,
		}
		if n.NodeHeartbeat <= h7 {
			offlineNodes = append(offlineNodes, row) // ① 故障/离线: 排除一切统计, 只进 R7/离线表
			if n.CustomRSS == 0 {
				configHidden = append(configHidden, row) // 离线且订阅不可见 → 归配置异常 (流量隐藏判定需心跳上报)
			}
			continue
		}
		nodeAliveRows = append(nodeAliveRows, row)
		if n.NodeGroup == 5 {
			g5Candidates = append(g5Candidates, row)
		}
		layer := 5
		if n.CustomRSS == 0 {
			// ③ 超配额隐藏 vs 配置异常: reportStatus 只在 limit>0 且 used_daily>left_daily 时隐藏
			if n.TrafficLimit > 0 && n.TrafficUsedDaily > n.TrafficLeftDaily {
				overquotaHidden = append(overquotaHidden, row)
			} else {
				configHidden = append(configHidden, row)
			}
			layer = 3
		} else if quota <= 0 && todayUsed <= 0 {
			layer = 4 // 冷启动: applyDerived/dailyjob 未跑, 供给待预热
		}
		var left int64
		if n.TrafficLimit > 0 {
			left = n.TrafficLimit - used
			if left < 0 {
				left = 0
			}
		}
		nodeRows = append(nodeRows, decisionNodeRow{
			ID: n.ID, G: int(n.NodeGroup), C: int(n.NodeClass), Cost: int64(n.NodeCost), Layer: layer,
			TodayUsed: todayUsed, Quota: quota, ProjToday: projToday,
			Avg7:          nodes7dAvg(windows, n.ID),
			Left:          left,
			DaysRemaining: service.DaysRemaining(int(n.TrafficResetDay)), // 月度重置剩余天数
		})
	}
	// G5 迁移候选: 昨日用量升序 (利用率最低优先); 节点实时在线: 在线人数降序
	sort.SliceStable(g5Candidates, func(a, b int) bool {
		return g5Candidates[a].UsedLastday < g5Candidates[b].UsedLastday
	})
	if len(g5Candidates) > 8 {
		g5Candidates = g5Candidates[:8]
	}
	sort.SliceStable(nodeAliveRows, func(a, b int) bool {
		return nodeAliveRows[a].Alive > nodeAliveRows[b].Alive
	})

	// 2 停用层: type=0 真实节点 (周期耗尽自动停用/手动隐藏), 供 E 区下钻
	disabledRaw := make([]*model.Node, 0)
	for i := range allReal {
		if allReal[i].Type == 0 {
			disabledRaw = append(disabledRaw, &allReal[i])
		}
	}
	sort.SliceStable(disabledRaw, func(a, b int) bool {
		return disabledRaw[a].NodeHeartbeat > disabledRaw[b].NodeHeartbeat
	})
	disabledNodes := make([]disabledNodeRow, 0, len(disabledRaw))
	for _, n := range disabledRaw {
		heartbeatMin := int64(-1)
		if n.NodeHeartbeat > 0 {
			heartbeatMin = int64(math.Round(float64(nowTs-n.NodeHeartbeat) / 60))
		}
		disabledNodes = append(disabledNodes, disabledNodeRow{
			ID: n.ID, Name: n.Name, Group: n.NodeGroup, Class: n.NodeClass,
			HeartbeatMin: heartbeatMin,
			UsedGB:       round1(float64(n.TrafficUsed) / 1073741824),
			LimitGB:      round1(float64(n.TrafficLimit) / 1073741824),
		})
	}

	// 决策判定 (纯函数, 单一来源)
	result := buildInsights(buildInsightsInputs{
		userMatrix:   userMatrix,
		nodeMatrix:   nodeMatrix,
		groupTraffic: traffic,
		th:           th,
		nodeRows:     nodeRows,
	})

	// E 区节点表附加状态徽章 (节点级判定结果来自纯函数, 单一来源)
	statusLabels := map[string]string{
		"over": "超额", "risk": "将超额", "cycle": "周期见底", "ok": "正常", "cold": "冷启动",
	}
	for _, r := range nodeAliveRows {
		st := result.NodeStatus[r.ID]
		r.Status = st
		r.StatusLabel = "—"
		if l, ok := statusLabels[st]; ok {
			r.StatusLabel = l
		}
	}

	// B 区展示行: 原始数值 → 展示字符串 (流量人性化/百分比/人台比/每$覆盖)
	groupDisplay := map[string]gin.H{}
	for _, g := range result.GroupIDs {
		row := result.Groups[strconv.Itoa(g)]
		usersPct := "—"
		if th.groupTarget > 0 {
			usersPct = fmt.Sprintf("%d%%", int64(math.Round(toF(row["users"])/float64(th.groupTarget)*100)))
		}
		overDisp := "—"
		if toI(row["over_den"]) > 0 {
			overDisp = fmt.Sprintf("%d/%d", toI(row["over_cnt"]), toI(row["over_den"]))
		}
		groupDisplay[strconv.Itoa(g)] = gin.H{
			"users":         row["users"],
			"users_pct":     usersPct,
			"live30":        row["live30"],
			"live24":        row["live24"],
			"today_traffic": tools.FlowAutoShow(math.Max(0, toF(row["today_traffic"]))),
			"used_lastday":  tools.FlowAutoShow(toF(row["used_lastday"])),
			"supply_daily":  tools.FlowAutoShow(toF(row["supply_daily"])),
			"util":          pctOrNullF(toFP(row["util"])),
			"proj_util":     pctOrNullF(toFP(row["proj_util"])),
			"over":          overDisp,
			"real_alive":    row["real_alive"],
			"real_dead":     row["real_dead"],
			"clone":         row["clone_all"],
			"per_node":      nullOrF(row["per_node"]),
			"cov_live":      nullOrF(row["cov_live"]),
			"cost_per_live": dollarOrNullF(row["cost_per_live"]),
			"cost":          row["cost"],
			"zero_group":    g == 0,
		}
	}

	// F 区展示行: 每组裁决 → 徽章/依据/建议动作 (R14, 单一结论)
	verdictLabels := map[string]string{
		"broken": "🔴 无可用", "add": "➕ 缺节点", "watch": "👀 观察", "trim": "➖ 冗余", "ok": "✅ 正常",
	}
	verdictDisplay := map[string]gin.H{}
	for _, g := range result.GroupIDs {
		v, ok := result.Verdicts[strconv.Itoa(g)]
		if !ok {
			continue
		}
		label := verdictLabels[v.Verdict]
		count := "—"
		if v.Verdict == "add" {
			count = fmt.Sprintf("缺 %d 台", v.Gap)
		} else if v.Verdict == "trim" {
			count = fmt.Sprintf("可迁 %d 台", v.Spare)
		}
		sg := v.Signals
		verdictDisplay[strconv.Itoa(g)] = gin.H{
			"verdict":   v.Verdict,
			"label":     label,
			"count":     count,
			"reasons":   strings.Join(v.Reasons, " · "),
			"usable":    sg["usable"],
			"over":      sg["over"],
			"util":      pctOrNullF(toFP(sg["util"])),
			"proj_util": pctOrNullF(toFP(sg["proj_util"])),
			"per_node":  nullOrF(sg["per_node"]),
			"cov_live":  nullOrF(sg["cov_live"]),
			"supply":    tools.FlowAutoShow(toF(sg["supply"])),
			"need":      tools.FlowAutoShow(toF(sg["need"])),
		}
	}

	// 全站每$覆盖基准展示
	cbCov := "—"
	if v, ok := result.CoverageBase["cov_live"].(*float64); ok && v != nil {
		cbCov = phpFloatStr(*v)
	}
	cbCpl := "—"
	if v, ok := result.CoverageBase["cost_per_live"].(*float64); ok && v != nil {
		cbCpl = "$" + phpFloatStr(*v)
	}
	coverageBaseDisplay := gin.H{"cov_live": cbCov, "cost_per_live": cbCpl}

	// 图表: 7 天每日用量 GB 曲线 (每组一条) + v2 供给日均水平线 (D 区虚线)
	chartData := gin.H{}
	supplyLines := gin.H{}
	for _, g := range result.GroupIDs {
		series, ok := series7d[g]
		if !ok {
			continue
		}
		points := make([]gin.H, 0, len(series))
		for _, pt := range series {
			points = append(points, gin.H{"label": pt.Date, "y": int64(math.Round(float64(pt.UsedLastday) / 1073741824))})
		}
		key := fmt.Sprintf("G%d", g)
		chartData[key] = points
		if t := traffic[g]; t != nil && t.SupplyDaily > 0 {
			supplyLines[key] = int64(math.Round(float64(t.SupplyDaily) / 1073741824))
		}
	}

	// 矩阵行头: L1($1)~L5($5) 真实等级, L6~L10 clone 区
	classLabels := []string{"L0"}
	for c := 1; c <= 5; c++ {
		classLabels = append(classLabels, fmt.Sprintf("L%d($%d)", c, c))
	}
	for c := 6; c <= 10; c++ {
		classLabels = append(classLabels, fmt.Sprintf("L%d(clone)", c))
	}
	classIDs := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	// node_status: {nodeid: over|risk|cycle|ok|cold}
	nodeStatusOut := make(map[string]string, len(result.NodeStatus))
	for id, st := range result.NodeStatus {
		nodeStatusOut[strconv.Itoa(int(id))] = st
	}

	subLimitDesc := "封顶已关闭"
	if th.perClass > 0 {
		subLimitDesc = fmt.Sprintf("需 = 等级×%d", th.perClass)
	} else if th.subBase > 0 {
		subLimitDesc = "需 = 保底基数"
	}

	RespondData(c, "获取成功", gin.H{
		"insights":              result.Insights,
		"groups":                result.Groups,
		"group_ids":             result.GroupIDs,
		"group_display":         groupDisplay,
		"class_ids":             classIDs,
		"class_labels":          classLabels,
		"cells":                 result.Cells,
		"verdicts":              result.Verdicts,
		"verdict_display":       verdictDisplay,
		"transfer":              result.Transfer,
		"node_status":           nodeStatusOut,
		"coverage_base":         result.CoverageBase,
		"coverage_base_display": coverageBaseDisplay,
		"node_alive_rows":       nodeAliveRows,
		"g5_candidates":         g5Candidates,
		"offline_nodes":         offlineNodes,
		"disabled_nodes":        disabledNodes,
		"overquota_hidden":      overquotaHidden,
		"config_hidden":         configHidden,
		"chart_data":            chartData,
		"supply_lines":          supplyLines,
		"generated_at":          now.Format("2006-01-02 15:04:05"),
		"group_target":          th.groupTarget,
		"sub_limit_desc":        subLimitDesc,
	})
}

// ---- gin.H 数值取回辅助 (buildInsights 输出经 map 传递) ----

func toI(v interface{}) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	}
	return 0
}

func toF(v interface{}) float64 {
	switch x := v.(type) {
	case int64:
		return float64(x)
	case int:
		return float64(x)
	}
	return 0
}

func toFP(v interface{}) *float64 {
	if x, ok := v.(*float64); ok {
		return x
	}
	return nil
}

// nullOrF 浮点或 "—"
func nullOrF(v interface{}) string {
	if x, ok := v.(*float64); ok && x != nil {
		return phpFloatStr(*x)
	}
	return "—"
}

// dollarOrNullF "$浮点" 或 "—"
func dollarOrNullF(v interface{}) string {
	if x, ok := v.(*float64); ok && x != nil {
		return "$" + phpFloatStr(*x)
	}
	return "—"
}
