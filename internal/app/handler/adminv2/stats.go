package adminv2

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

/* 订阅请求记录 (rss_request_log; PHP RssHeaderCapture MySQL 化) + 统计 (充值/组流量) */

var rssSortable = map[string]string{"id": "id", "user_id": "user_id", "app": "app", "created_at": "created_at"}

// ListRssHeaders 订阅抓取列表 (user_id 过滤 + 模糊搜索; enabled 状态一并返回)
func (h *Handler) ListRssHeaders(c *gin.Context) {
	enabled := config.GlobalConfig != nil && config.GlobalConfig.RssHeaderCapture.Enabled
	if !enabled {
		RespondList(c, []interface{}{}, 0)
		return
	}
	q := ParseListQuery(c, rssSortable, "id", "desc")
	base := func() *gorm.DB {
		db := database.DB.Model(&model.RssRequestLog{})
		if v := c.Query("user_id"); v != "" {
			db = db.Where("user_id = ?", v)
		}
		if s := q.Search; s != "" {
			like := "%" + s + "%"
			db = db.Where("email LIKE ? OR user_agent LIKE ? OR uri LIKE ? OR app LIKE ? OR token LIKE ?", like, like, like, like, like)
		}
		return db
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	var logs []model.RssRequestLog
	dbq := base().Order(q.OrderBy + " " + q.OrderDir)
	if q.Limit >= 0 {
		dbq = dbq.Offset(q.Offset).Limit(q.Limit)
	}
	if err := dbq.Find(&logs).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	type item struct {
		ID        int64  `json:"id"`
		UserID    int64  `json:"user_id"`
		Email     string `json:"email"`
		Token     string `json:"token"`
		App       string `json:"app"`
		IP        string `json:"ip"`
		URI       string `json:"uri"`
		UserAgent string `json:"user_agent"`
		CreatedAt string `json:"created_at"`
	}
	items := make([]item, 0, len(logs))
	for _, l := range logs {
		items = append(items, item{
			ID: l.ID, UserID: l.UserID, Email: l.Email, Token: l.Token, App: l.App,
			IP: l.IP, URI: l.URI, UserAgent: l.UserAgent,
			CreatedAt: l.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	RespondList(c, items, total)
}

// GetRssHeader 详情 (全量 headers/query JSON)
func (h *Handler) GetRssHeader(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "记录ID无效")
		return
	}
	var l model.RssRequestLog
	if err := database.DB.First(&l, id).Error; err != nil {
		RespondErr(c, "记录不存在")
		return
	}
	var headers, query interface{}
	_ = json.Unmarshal([]byte(l.Headers), &headers)
	_ = json.Unmarshal([]byte(l.Query), &query)
	RespondData(c, "获取成功", gin.H{
		"id": l.ID, "user_id": l.UserID, "email": l.Email, "token": l.Token,
		"app": l.App, "ip": l.IP, "method": l.Method, "uri": l.URI,
		"query": query, "user_agent": l.UserAgent, "headers": headers,
		"created_at": l.CreatedAt.Format("2006-01-02 15:04:05"),
	})
}

// ClearRssHeaders 清空抓取记录
func (h *Handler) ClearRssHeaders(c *gin.Context) {
	if err := database.DB.Exec("DELETE FROM rss_request_log").Error; err != nil {
		RespondErr(c, "清空失败")
		return
	}
	RespondOK(c, "已清空")
}

// ==================== 充值统计 (rechargeStats) ====================

// RechargeStats 充值统计 (窗口 7/14/21/30/60/90 × 分组去重用户数 + 占比 + 金额)
func (h *Handler) RechargeStats(c *gin.Context) {
	groups := []int{5, 4, 3, 2, 1, 0}
	daysList := []int{7, 14, 21, 30, 60, 90}

	// 各分组用户总数
	groupTotals := map[int]int64{}
	{
		var rows []struct {
			G   int32 `gorm:"column:g"`
			Cnt int64 `gorm:"column:cnt"`
		}
		database.DB.Model(&model.User{}).
			Select("node_group AS g, COUNT(*) AS cnt").
			Group("node_group").Scan(&rows)
		for _, r := range rows {
			groupTotals[int(r.G)] = r.Cnt
		}
	}

	windows := make([]gin.H, 0, len(daysList))
	for _, d := range daysList {
		threshold := time.Now().AddDate(0, 0, -d).Format("2006-01-02 15:04:05")
		// 去重充值用户数 (按分组)
		stats := map[int]int64{}
		{
			var rows []struct {
				G   int32 `gorm:"column:g"`
				Cnt int64 `gorm:"column:cnt"`
			}
			database.DB.Table("code").
				Select("user.node_group AS g, COUNT(DISTINCT code.userid) AS cnt").
				Joins("JOIN user ON user.id = code.userid").
				Where("code.type = -1 AND code.isused = 1 AND code.usedatetime >= ?", threshold).
				Group("user.node_group").Scan(&rows)
			for _, r := range rows {
				stats[int(r.G)] = r.Cnt
			}
		}
		// 合计口径对齐 PHP array_sum($stats): 覆盖数据中出现的全部分组 (含 >5 的自定义组)
		var sum int64
		for _, cnt := range stats {
			sum += cnt
		}
		pct := gin.H{}
		for _, g := range groups {
			total := groupTotals[g]
			cnt := stats[g]
			if total > 0 {
				pct[strconv.Itoa(g)] = round1(float64(cnt) / float64(total) * 100)
			} else {
				pct[strconv.Itoa(g)] = 0.0
			}
		}
		// 充值金额合计
		var amount string
		database.DB.Model(&model.Code{}).
			Select("COALESCE(SUM(number),0)").
			Where("type = -1 AND isused = 1 AND usedatetime >= ?", threshold).
			Scan(&amount)

		windows = append(windows, gin.H{
			"days": d, "stats": stats, "pct": pct, "sum": sum, "amount": amount,
		})
	}

	// 最近一笔充值时间
	var latest string
	database.DB.Model(&model.Code{}).Where("type = -1 AND isused = 1").
		Select("MAX(usedatetime)").Scan(&latest)
	latestTs := int64(0)
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", strings.ReplaceAll(strings.ReplaceAll(latest, "T", " "), "Z", ""), time.Local); err == nil {
		latestTs = t.Unix()
	}

	var sumTotal int64
	for _, v := range groupTotals {
		sumTotal += v
	}

	RespondData(c, "获取成功", gin.H{
		"groups": groups, "windows": windows,
		"group_totals": groupTotals, "sum_total": sumTotal,
		"latest_recharge": latestTs,
	})
}

func round1(f float64) float64 {
	return float64(int64(f*10+0.5)) / 10
}

// ==================== 分组流量统计 (nodeGroupTraffic) ====================

// aggregateGroup 对齐 PHP Analytics::aggregateGroup (is_clone=0 + 心跳过滤 + usable 口径)
func aggregateGroup(group int32, heartbeatWithin int64, usableOnly bool) gin.H {
	var nodes []model.Node
	q := database.DB.Where("is_clone = 0 AND node_group = ?", group)
	if usableOnly {
		q = q.Where("type = 1 AND custom_rss = 1")
	}
	q.Order("id ASC").Find(&nodes)

	now := time.Now().Unix()
	cutoff := int64(0)
	if heartbeatWithin > 0 {
		cutoff = now - heartbeatWithin
	}
	agg := gin.H{
		"group": group, "used_total": int64(0), "supply_daily": int64(0),
		"node_count": 0, "unlimited_count": 0, "online_count": 0,
	}
	var usedTotal, supplyDaily int64
	var nodeCount, unlimited, online int
	for i := range nodes {
		n := &nodes[i]
		if cutoff > 0 && n.NodeHeartbeat <= cutoff {
			continue
		}
		maxDay := n.TrafficMaxDayValue
		if maxDay <= 0 {
			maxDay = n.TrafficUsedLastday
		}
		supply := n.TrafficLeftDaily
		if maxDay < supply {
			supply = maxDay
		}
		supplyDaily += supply
		usedTotal += n.TrafficUsed
		nodeCount++
		if n.TrafficLimit == 0 {
			unlimited++
		}
		if n.NodeHeartbeat > now-90 {
			online++
		}
	}
	agg["used_total"] = usedTotal
	agg["supply_daily"] = supplyDaily
	agg["node_count"] = nodeCount
	agg["unlimited_count"] = unlimited
	agg["online_count"] = online
	return agg
}

// groupTraffic7d 对齐 PHP getGroupTraffic7d: 组内活跃节点 traffic_7d (Redis) 按日求和
func groupTraffic7d(group int32, heartbeatWithin int64) []gin.H {
	var nodes []model.Node
	database.DB.Where("is_clone = 0 AND node_group = ?", group).Order("id ASC").Find(&nodes)
	now := time.Now().Unix()
	cutoff := now - heartbeatWithin

	// 基准日 (最近 dailyjob), Redis 缺失回退启发式
	var baseDateTs int64
	if database.Redis != nil {
		if v, err := database.Redis.Get(context.Background(), "ssp:node_traffic_daily:last_run").Result(); err == nil {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				t := time.Unix(n, 0)
				baseDateTs = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).Unix()
			}
		}
	}
	if baseDateTs == 0 {
		nowT := time.Now()
		today0411 := time.Date(nowT.Year(), nowT.Month(), nowT.Day(), 4, 11, 0, 0, nowT.Location())
		if nowT.After(today0411) {
			baseDateTs = time.Date(nowT.Year(), nowT.Month(), nowT.Day(), 0, 0, 0, 0, nowT.Location()).Unix()
		} else {
			baseDateTs = time.Date(nowT.Year(), nowT.Month(), nowT.Day(), 0, 0, 0, 0, nowT.Location()).AddDate(0, 0, -1).Unix()
		}
	}

	// 7 天序列 (traffic_7d[0]=最新=前一日用量), 输出升序
	sums := make([]int64, 7)
	counts := make([]int, 7)
	for i := range nodes {
		n := &nodes[i]
		if cutoff > 0 && n.NodeHeartbeat <= cutoff {
			continue
		}
		w := service.NodeDailyWindow7d(context.Background(), int64(n.ID))
		for j, v := range w {
			if j < 7 {
				sums[j] += v
				counts[j]++
			}
		}
	}
	series := make([]gin.H, 0, 7)
	for i := 6; i >= 0; i-- {
		day := time.Unix(baseDateTs-int64(i+1)*86400, 0)
		series = append(series, gin.H{
			"ts":           baseDateTs - int64(i+1)*86400,
			"date":         day.Format("01-02"),
			"used_lastday": sums[i],
			"node_count":   counts[i],
		})
	}
	return series
}

// NodeGroupTraffic 分组流量统计 (PHP nodeGroupTraffic: summary + 7d 序列 + 对比点)
func (h *Handler) NodeGroupTraffic(c *gin.Context) {
	// 动态发现分组
	var groupRows []struct {
		G int32 `gorm:"column:g"`
	}
	database.DB.Model(&model.Node{}).Where("is_clone = 0").
		Select("node_group AS g").Group("node_group").Order("node_group ASC").Scan(&groupRows)

	gb := 1024.0 * 1024 * 1024
	summary := make([]gin.H, 0, len(groupRows))
	series := gin.H{}
	totalUsedLastday, totalSupplyDaily, totalNodes := int64(0), int64(0), 0
	for _, r := range groupRows {
		agg := aggregateGroup(r.G, 7*3600, false)
		s7 := groupTraffic7d(r.G, 7*3600)
		usedLastday := int64(0)
		if len(s7) > 0 {
			if v, ok := s7[len(s7)-1]["used_lastday"].(int64); ok {
				usedLastday = v
			}
		}
		supply, _ := agg["supply_daily"].(int64)
		nc, _ := agg["node_count"].(int)
		agg["used_lastday"] = usedLastday
		agg["used_lastday_gb"] = round1(float64(usedLastday) / gb)
		agg["supply_daily_gb"] = round1(float64(supply) / gb)
		summary = append(summary, agg)
		series[strconv.Itoa(int(r.G))] = s7
		totalUsedLastday += usedLastday
		totalSupplyDaily += supply
		totalNodes += nc
	}

	// 对比图数据点 (昨日 vs 可供日均, GB/天)
	usedPoints := make([]gin.H, 0, len(summary))
	supplyPoints := make([]gin.H, 0, len(summary))
	for _, g := range summary {
		label := "G" + strconv.Itoa(int(g["group"].(int32)))
		usedPoints = append(usedPoints, gin.H{"label": label, "y": int64(g["used_lastday_gb"].(float64))})
		supplyPoints = append(supplyPoints, gin.H{"label": label, "y": int64(g["supply_daily_gb"].(float64))})
	}

	RespondData(c, "获取成功", gin.H{
		"summary": summary, "series": series,
		"total_used_lastday": totalUsedLastday, "total_supply_daily": totalSupplyDaily,
		"total_used_lastday_gb": round1(float64(totalUsedLastday) / gb),
		"total_supply_daily_gb": round1(float64(totalSupplyDaily) / gb),
		"total_nodes":           totalNodes,
		"used_points":           usedPoints, "supply_points": supplyPoints,
	})
}
