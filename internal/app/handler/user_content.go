package handler

// 用户端长尾内容页: 公告/审计规则与日志/测速/解锁矩阵/节点流量/流量日志/中转 CRUD/订阅管理/iOS 配置下载
// 移植基线: PHP UserController node*/tutorial/announcement/lookingglass/cncdnlooking/cfcdnlooking/
//           detect*/trafficLog/GetIosConf + RelayController 全套 + rss 管理三端点

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"

	"github.com/gin-gonic/gin"
)

// ---- 公告 ----

// Announcements 公告列表/详情 (?id= 详情; markdown 首行作标题)
func (h *UserHandler) Announcements(c *gin.Context) {
	if idStr := c.Param("id"); idStr != "" && idStr != "0" {
		id, _ := strconv.Atoi(idStr)
		var ann model.Announcement
		if err := database.DB.First(&ann, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"ret": 0, "msg": "公告不存在"})
			return
		}
		title := ann.Markdown
		if idx := strings.IndexAny(title, "\r\n"); idx > 0 {
			title = title[:idx]
		}
		if title == "" {
			title = fmt.Sprintf("公告 #%d", ann.ID)
		}
		c.JSON(http.StatusOK, gin.H{
			"ret": 1, "msg": "获取成功",
			"data": gin.H{
				"id": ann.ID, "title": title,
				"date":    ann.Date.Format("2006-01-02 15:04:05"),
				"content": ann.Content, "markdown": ann.Markdown,
			},
		})
		return
	}

	var anns []model.Announcement
	database.DB.Order("id DESC").Limit(30).Find(&anns)

	items := make([]gin.H, 0, len(anns))
	for _, a := range anns {
		title := a.Markdown
		if idx := strings.IndexAny(title, "\r\n"); idx > 0 {
			title = title[:idx]
		}
		if title == "" {
			title = fmt.Sprintf("公告 #%d", a.ID)
		}
		items = append(items, gin.H{
			"id": a.ID, "title": title,
			"date":     a.Date.Format("2006-01-02 15:04:05"),
			"markdown": a.Markdown,
		})
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "获取成功", "data": gin.H{"items": items}})
}

// ---- 审计 ----

// DetectRules 审计规则列表 (用户只读)
func (h *UserHandler) DetectRules(c *gin.Context) {
	page, size := parsePage(c)
	var total int64
	database.DB.Model(&model.DetectRule{}).Count(&total)

	var rules []model.DetectRule
	database.DB.Order("id ASC").Offset((page - 1) * size).Limit(size).Find(&rules)

	c.JSON(http.StatusOK, gin.H{
		"ret": 1, "msg": "获取成功",
		"data": gin.H{"items": rules, "total": total},
	})
}

// DetectLogs 本人被审计记录
func (h *UserHandler) DetectLogs(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	page, size := parsePage(c)
	var total int64
	database.DB.Model(&model.DetectLog{}).Where("user_id = ?", user.ID).Count(&total)

	var logs []model.DetectLog
	database.DB.Where("user_id = ?", user.ID).Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&logs)

	ruleIDs := make([]int64, 0, len(logs))
	for _, l := range logs {
		ruleIDs = append(ruleIDs, l.ListID)
	}
	ruleMap := make(map[int64]model.DetectRule)
	if len(ruleIDs) > 0 {
		var rules []model.DetectRule
		database.DB.Where("id IN ?", ruleIDs).Find(&rules)
		for _, r := range rules {
			ruleMap[r.ID] = r
		}
	}
	nodeIDs := make([]int32, 0, len(logs))
	for _, l := range logs {
		nodeIDs = append(nodeIDs, l.NodeID)
	}
	nodeMap := make(map[int32]string)
	if len(nodeIDs) > 0 {
		var nodes []model.Node
		database.DB.Select("id, name").Where("id IN ?", nodeIDs).Find(&nodes)
		for _, n := range nodes {
			nodeMap[n.ID] = n.Name
		}
	}

	items := make([]gin.H, 0, len(logs))
	for _, l := range logs {
		items = append(items, gin.H{
			"id":       l.ID,
			"rule":     ruleMap[l.ListID].Name,
			"node":     nodeMap[l.NodeID],
			"datetime": time.Unix(l.DateTime, 0).Format("2006-01-02 15:04:05"),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1, "msg": "获取成功",
		"data": gin.H{"items": items, "total": total},
	})
}

// ---- 测速 Looking Glass ----

// LookingGlass 节点测速记录 (近 N 小时, PHP 同口径默认 6h)
func (h *UserHandler) LookingGlass(c *gin.Context) {
	hours := 6
	if v, err := strconv.Atoi(c.DefaultQuery("hours", "6")); err == nil && v > 0 && v <= 72 {
		hours = v
	}
	since := time.Now().Add(-time.Duration(hours) * time.Hour).Unix()

	var tests []model.Speedtest
	database.DB.Where("datetime > ?", since).Order("id DESC").Limit(200).Find(&tests)

	nodeIDs := make([]int32, 0, len(tests))
	for _, t := range tests {
		nodeIDs = append(nodeIDs, t.NodeID)
	}
	nodeMap := make(map[int32]string)
	if len(nodeIDs) > 0 {
		var nodes []model.Node
		database.DB.Select("id, name").Where("id IN ?", nodeIDs).Find(&nodes)
		for _, n := range nodes {
			nodeMap[n.ID] = n.Name
		}
	}

	items := make([]gin.H, 0, len(tests))
	for _, t := range tests {
		items = append(items, gin.H{
			"id":      t.ID,
			"node":    nodeMap[t.NodeID],
			"time":    time.Unix(t.DateTime, 0).Format("2006-01-02 15:04:05"),
			"telecom": gin.H{"ping": t.Telecomping, "up": t.Telecomeupload, "down": t.Telecomedownload},
			"unicom":  gin.H{"ping": t.Unicomping, "up": t.Unicomupload, "down": t.Unicomdownload},
			"cmcc":    gin.H{"ping": t.Cmccping, "up": t.Cmccupload, "down": t.Cmccdownload},
		})
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "获取成功", "data": gin.H{"items": items}})
}

// CncdnLooking 中转加速说明 + 用户榜 (cncdn_count>32)
func (h *UserHandler) CncdnLooking(c *gin.Context) {
	var lines []model.Cncdn
	database.DB.Where("status = 1 AND `show` = 1").Order("id ASC").Find(&lines)

	var top []model.User
	database.DB.Select("id, user_name, cncdn_count").
		Where("cncdn_count > 32").Order("CAST(cncdn_count AS SIGNED) DESC").Limit(20).Find(&top)

	topItems := make([]gin.H, 0, len(top))
	for _, u := range top {
		name := u.UserName
		if u.IsHide == 1 {
			name = "匿名用户"
		}
		topItems = append(topItems, gin.H{"name": name, "count": u.CncdnCount})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1, "msg": "获取成功",
		"data": gin.H{"lines": lines, "top": topItems},
	})
}

// CfcdnLooking 优选 IP 说明 + 榜单 (组 1 且 t>24h, cfcdn_count 排序)
func (h *UserHandler) CfcdnLooking(c *gin.Context) {
	dayAgo := time.Now().Unix() - 86400
	var top []model.User
	database.DB.Select("id, user_name, cfcdn, cfcdn_count, is_hide").
		Where("node_group = 1 AND t > ?", dayAgo).
		Order("CAST(cfcdn_count AS SIGNED) DESC").Limit(20).Find(&top)

	topItems := make([]gin.H, 0, len(top))
	for _, u := range top {
		name := u.UserName
		if u.IsHide == 1 {
			name = "匿名用户"
		}
		topItems = append(topItems, gin.H{"name": name, "ip": u.Cfcdn, "count": u.CfcdnCount})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1, "msg": "获取成功",
		"data": gin.H{"top": topItems},
	})
}

// ---- 节点解锁矩阵 / 节点流量 ----

// NodeUnlock 流媒体/AI 解锁矩阵 (node.node_unlock JSON 列直出, 前端筛选)
func (h *UserHandler) NodeUnlock(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	query := database.DB.Table("ss_node").Select("id, name, status, node_unlock, traffic_rate").Where("type = 1")
	if user.IsAdmin != 1 {
		query = query.Where("(node_group = ? OR node_group = 0)", user.NodeGroup)
	}

	type nodeUnlockRow struct {
		ID          int32   `json:"id"`
		Name        string  `json:"name"`
		Status      string  `json:"status"`
		NodeUnlock  string  `json:"node_unlock_raw"`
		TrafficRate float32 `json:"traffic_rate"`
	}
	var rows []nodeUnlockRow
	query.Order("sort ASC").Find(&rows)

	items := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		unlock := gin.H{}
		if r.NodeUnlock != "" {
			trimmed := strings.TrimSpace(r.NodeUnlock)
			if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
				var m interface{}
				if err := json.Unmarshal([]byte(trimmed), &m); err == nil {
					unlock = gin.H{"data": m}
				}
			} else {
				unlock = gin.H{"data": trimmed}
			}
		}
		items = append(items, gin.H{
			"id": r.ID, "name": r.Name, "status": r.Status,
			"traffic_rate": r.TrafficRate, "unlock": unlock,
		})
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "获取成功", "data": gin.H{"items": items}})
}

// NodeDailyTraffic 隐藏页: 按节点 id 查每日流量 (?node_id=)
func (h *UserHandler) NodeDailyTraffic(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	nodeID, _ := strconv.Atoi(c.DefaultQuery("node_id", c.Param("id")))
	if nodeID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "请提供节点 id"})
		return
	}

	days := 7
	if v, err := strconv.Atoi(c.DefaultQuery("days", "7")); err == nil && v > 0 && v <= 90 {
		days = v
	}
	since := time.Now().AddDate(0, 0, -days)

	type dailyRow struct {
		LogTime string  `json:"date"`
		U       int64   `json:"u"`
		D       int64   `json:"d"`
		Rate    float32 `json:"rate"`
	}
	var dailies []dailyRow
	database.DB.Table("user_traffic_log").
		Select("FROM_UNIXTIME(log_time, '%Y-%m-%d') AS log_time, SUM(u) AS u, SUM(d) AS d, MAX(rate) AS rate").
		Where("user_id = ? AND node_id = ? AND log_time > ?", user.ID, nodeID, since.Unix()).
		Group("FROM_UNIXTIME(log_time, '%Y-%m-%d')").Order("log_time ASC").Scan(&dailies)

	var node model.Node
	database.DB.Select("id, name").First(&node, nodeID)

	c.JSON(http.StatusOK, gin.H{
		"ret": 1, "msg": "获取成功",
		"data": gin.H{"node": gin.H{"id": node.ID, "name": node.Name}, "daily": dailies},
	})
}

// TrafficLog 流量记录三层页 (24h 明细 + N 天日柱 + 节点累计; 服务端聚合)
func (h *UserHandler) TrafficLog(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	days := 90
	if v, err := strconv.Atoi(c.DefaultQuery("days", "90")); err == nil && v > 0 && v <= 365 {
		days = v
	}

	// 1. 近 24h 明细 (最多 200 条)
	var details []model.UserTrafficLog
	database.DB.Where("user_id = ? AND log_time > ?", user.ID, time.Now().Unix()-86400).
		Order("id DESC").Limit(200).Find(&details)

	// 2. N 天日粒度汇总
	type dailyRow struct {
		LogTime string `json:"date"`
		U       int64  `json:"u"`
		D       int64  `json:"d"`
	}
	var dailies []dailyRow
	database.DB.Table("user_traffic_log").
		Select("FROM_UNIXTIME(log_time, '%Y-%m-%d') AS log_time, SUM(u) AS u, SUM(d) AS d").
		Where("user_id = ? AND log_time > ?", user.ID, time.Now().AddDate(0, 0, -days).Unix()).
		Group("FROM_UNIXTIME(log_time, '%Y-%m-%d')").Order("log_time ASC").Scan(&dailies)

	// 3. 节点累计
	type nodeRow struct {
		NodeID int32   `json:"node_id"`
		U      int64   `json:"u"`
		D      int64   `json:"d"`
		Rate   float32 `json:"rate"`
	}
	var nodeTotals []nodeRow
	database.DB.Table("user_traffic_log").
		Select("node_id, SUM(u) AS u, SUM(d) AS d, MAX(rate) AS rate").
		Where("user_id = ?", user.ID).
		Group("node_id").Order("u + d DESC").Scan(&nodeTotals)

	nodeIDs := make([]int32, 0, len(nodeTotals))
	for _, n := range nodeTotals {
		nodeIDs = append(nodeIDs, n.NodeID)
	}
	nodeNameMap := make(map[int32]string)
	if len(nodeIDs) > 0 {
		var nodes []model.Node
		database.DB.Select("id, name").Where("id IN ?", nodeIDs).Find(&nodes)
		for _, n := range nodes {
			nodeNameMap[n.ID] = n.Name
		}
	}

	nodeItems := make([]gin.H, 0, len(nodeTotals))
	for _, n := range nodeTotals {
		nodeItems = append(nodeItems, gin.H{
			"node_id": n.NodeID, "name": nodeNameMap[n.NodeID],
			"u": n.U, "d": n.D, "rate": n.Rate,
		})
	}

	detailItems := make([]gin.H, 0, len(details))
	for _, d := range details {
		detailItems = append(detailItems, gin.H{
			"node_id": d.NodeID, "name": nodeNameMap[d.NodeID],
			"u": d.U, "d": d.D, "rate": d.Rate,
			"time": time.Unix(int64(d.LogTime), 0).Format("01-02 15:04"),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1, "msg": "获取成功",
		"data": gin.H{
			"details": detailItems,
			"daily":   dailies,
			"nodes":   nodeItems,
		},
	})
}

// ---- 中转规则 CRUD ----

// RelayList 本人中转规则列表 + 可选节点下拉数据
func (h *UserHandler) RelayList(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var relays []model.Relay
	database.DB.Where("user_id = ?", user.ID).Order("id DESC").Find(&relays)

	nodeIDs := make([]int64, 0)
	for _, r := range relays {
		nodeIDs = append(nodeIDs, r.SourceNodeID, r.DistNodeID)
	}
	nodeNameMap := make(map[int64]string)
	if len(nodeIDs) > 0 {
		var nodes []model.Node
		database.DB.Select("id, name").Where("id IN ?", nodeIDs).Find(&nodes)
		for _, n := range nodes {
			nodeNameMap[int64(n.ID)] = n.Name
		}
	}

	items := make([]gin.H, 0, len(relays))
	for _, r := range relays {
		items = append(items, gin.H{
			"id":          r.ID,
			"source_node": gin.H{"id": r.SourceNodeID, "name": nodeNameMap[r.SourceNodeID]},
			"dist_node":   gin.H{"id": r.DistNodeID, "name": nodeNameMap[r.DistNodeID]},
			"dist_ip":     r.DistIP,
			"port":        r.Port,
			"priority":    r.Priority,
		})
	}

	// 下拉数据 (用户可见节点)
	var nodes []model.Node
	nq := database.DB.Select("id, name").Where("type = 1")
	if user.IsAdmin != 1 {
		nq = nq.Where("(node_group = ? OR node_group = 0)", user.NodeGroup)
	}
	nq.Order("sort ASC").Find(&nodes)
	nodeOptions := make([]gin.H, 0, len(nodes))
	for _, n := range nodes {
		nodeOptions = append(nodeOptions, gin.H{"id": n.ID, "name": n.Name})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1, "msg": "获取成功",
		"data": gin.H{"items": items, "nodes": nodeOptions},
	})
}

// relayConflictExists 冲突检测: 同源节点同端口已被他人占用
func relayConflictExists(sourceNodeID, port, excludeID int64) bool {
	q := database.DB.Model(&model.Relay{}).
		Where("source_node_id = ? AND port = ?", sourceNodeID, port)
	if excludeID > 0 {
		q = q.Where("id <> ?", excludeID)
	}
	var n int64
	q.Count(&n)
	return n > 0
}

// RelayAdd 创建中转规则 (冲突检测)
func (h *UserHandler) RelayAdd(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var req struct {
		SourceNodeID int64  `json:"source_node_id" binding:"required"`
		DistNodeID   int64  `json:"dist_node_id" binding:"required"`
		DistIP       string `json:"dist_ip"`
		Port         int    `json:"port" binding:"required"`
		Priority     int    `json:"priority"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误"})
		return
	}
	if req.SourceNodeID == req.DistNodeID {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "起源节点与目标节点不能相同"})
		return
	}
	if relayConflictExists(req.SourceNodeID, int64(req.Port), 0) {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "该起源节点上的端口已被占用，请换一个"})
		return
	}

	relay := model.Relay{
		UserID:       int64(user.ID),
		SourceNodeID: req.SourceNodeID,
		DistNodeID:   req.DistNodeID,
		DistIP:       req.DistIP,
		Port:         int32(req.Port),
		Priority:     int32(req.Priority),
	}
	if err := database.DB.Create(&relay).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "创建失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "中转规则已创建"})
}

// RelayUpdate 更新中转规则 (归属校验 + 冲突检测)
func (h *UserHandler) RelayUpdate(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	id := c.Param("id")
	var relay model.Relay
	if err := database.DB.Where("id = ? AND user_id = ?", id, user.ID).First(&relay).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"ret": 0, "msg": "规则不存在"})
		return
	}

	var req struct {
		SourceNodeID int64  `json:"source_node_id"`
		DistNodeID   int64  `json:"dist_node_id"`
		DistIP       string `json:"dist_ip"`
		Port         int    `json:"port"`
		Priority     int    `json:"priority"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误"})
		return
	}
	if req.SourceNodeID > 0 && req.DistNodeID > 0 && req.SourceNodeID == req.DistNodeID {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "起源节点与目标节点不能相同"})
		return
	}
	if req.SourceNodeID > 0 && req.Port > 0 && relayConflictExists(req.SourceNodeID, int64(req.Port), relay.ID) {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "该起源节点上的端口已被占用"})
		return
	}

	updates := map[string]interface{}{}
	if req.SourceNodeID > 0 {
		updates["source_node_id"] = req.SourceNodeID
	}
	if req.DistNodeID > 0 {
		updates["dist_node_id"] = req.DistNodeID
	}
	if req.DistIP != "" {
		updates["dist_ip"] = req.DistIP
	}
	if req.Port > 0 {
		updates["port"] = req.Port
	}
	updates["priority"] = req.Priority

	if err := database.DB.Model(&relay).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "更新失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "中转规则已更新"})
}

// RelayDelete 删除中转规则 (归属校验)
func (h *UserHandler) RelayDelete(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	id := c.Param("id")
	res := database.DB.Where("id = ? AND user_id = ?", id, user.ID).Delete(&model.Relay{})
	if res.Error != nil || res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"ret": 0, "msg": "规则不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "中转规则已删除"})
}

// ---- 订阅管理 (多 token, 基于 link 表 type=11) ----

// RssList 订阅列表 (ensureDefault 懒生成 + 上限)
func (h *UserHandler) RssList(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var links []model.Link
	database.DB.Where("userid = ? AND type = 11", user.ID).Order("id ASC").Find(&links)

	// ensureDefault 懒生成默认订阅
	if len(links) == 0 {
		token := generateLocalRandomStr(16)
		link := model.Link{Type: 11, UserID: int64(user.ID), Token: token}
		if err := database.DB.Create(&link).Error; err == nil {
			links = append(links, link)
		}
	}

	items := make([]gin.H, 0, len(links))
	for _, l := range links {
		items = append(items, gin.H{
			"id":    l.ID,
			"token": l.Token,
			"url":   "/link/" + l.Token,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1, "msg": "获取成功",
		"data": gin.H{
			"items":     items,
			"max":       5,
			"sub_limit": user.SubLimit,
		},
	})
}

// RssCreate 新建订阅 token (上限校验)
func (h *UserHandler) RssCreate(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var cnt int64
	database.DB.Model(&model.Link{}).Where("userid = ? AND type = 11", user.ID).Count(&cnt)
	if cnt >= 5 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "订阅 token 数量已达上限 (5)"})
		return
	}

	token := generateLocalRandomStr(16)
	link := model.Link{Type: 11, UserID: int64(user.ID), Token: token}
	if err := database.DB.Create(&link).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "创建失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "订阅已创建", "data": gin.H{"token": token}})
}

// RssDelete 删除订阅 token (归属校验)
func (h *UserHandler) RssDelete(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var req struct {
		ID int64 `json:"id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误"})
		return
	}

	res := database.DB.Where("id = ? AND userid = ? AND type = 11", req.ID, user.ID).Delete(&model.Link{})
	if res.Error != nil || res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"ret": 0, "msg": "订阅不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "订阅已删除"})
}

// RssReset 重置全部订阅 (清空重建默认; /user/url_reset bridge 调用)
func (h *UserHandler) RssReset(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	database.DB.Where("userid = ? AND type = 11", user.ID).Delete(&model.Link{})
	token := generateLocalRandomStr(16)
	if err := database.DB.Create(&model.Link{Type: 11, UserID: int64(user.ID), Token: token}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "重置失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "订阅已全部重置", "data": gin.H{"token": token}})
}

// ---- iOS 配置下载 ----

// GetIosConf 下载 allinone.conf (attachment; cookie 直链携带, 免 ?token=)
func (h *UserHandler) GetIosConf(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var nodes []model.Node
	q := database.DB.Where("type = 1")
	if user.IsAdmin != 1 {
		q = q.Where("(node_group = ? OR node_group = 0)", user.NodeGroup)
	}
	q.Order("sort ASC").Find(&nodes)

	var b strings.Builder
	b.WriteString("# SPanel allinone.conf\n")
	b.WriteString(fmt.Sprintf("# user: %s port: %d method: %s passwd: %s\n", user.Email, user.Port, user.Method, user.Passwd))
	b.WriteString("[]\n")
	for _, n := range nodes {
		b.WriteString(fmt.Sprintf("%s = %s, %d, %s, %s, %s\n", n.Name, n.Server, user.Port, user.Method, user.Passwd, n.Name))
	}

	c.Header("Content-Disposition", "attachment; filename=allinone.conf")
	c.Data(http.StatusOK, "application/octet-stream", []byte(b.String()))
}
