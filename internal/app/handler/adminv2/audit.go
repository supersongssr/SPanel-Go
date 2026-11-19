package adminv2

import (
	"context"
	"strconv"
	"time"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/notify"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

/* 审计规则 + 审计记录 + 自动命令 + IP 管理 + 流量/登录/在线记录 */

// ==================== 审计规则 (detect) ====================

var detectSortable = map[string]string{
	"id": "id", "name": "name", "text": "text", "regex": "regex", "type": "type",
}

// ListDetectRules 审计规则列表
func (h *Handler) ListDetectRules(c *gin.Context) {
	q := ParseListQuery(c, detectSortable, "id", "asc")
	base := func() *gorm.DB {
		db := database.DB.Model(&model.DetectRule{})
		if q.Search != "" {
			like := "%" + q.Search + "%"
			db = db.Where("name LIKE ? OR text LIKE ? OR regex LIKE ?", like, like, like)
		}
		return db
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	var rules []model.DetectRule
	dbq := base().Order(q.OrderBy + " " + q.OrderDir)
	if q.Limit >= 0 {
		dbq = dbq.Offset(q.Offset).Limit(q.Limit)
	}
	if err := dbq.Find(&rules).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	RespondList(c, rules, total)
}

// GetDetectRule 编辑页数据
func (h *Handler) GetDetectRule(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "规则ID无效")
		return
	}
	var r model.DetectRule
	if err := database.DB.First(&r, id).Error; err != nil {
		RespondErr(c, "规则不存在")
		return
	}
	RespondData(c, "获取成功", gin.H{"id": r.ID, "name": r.Name, "text": r.Text, "regex": r.Regex, "type": r.Type})
}

// CreateDetectRule 添加审计规则 (PHP add: TG 通知)
func (h *Handler) CreateDetectRule(c *gin.Context) {
	var req struct {
		Name  string `json:"name"`
		Text  string `json:"text"`
		Regex string `json:"regex"`
		Type  int32  `json:"type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespondErr(c, "参数有误")
		return
	}
	r := model.DetectRule{Name: req.Name, Text: req.Text, Regex: req.Regex, Type: req.Type}
	if err := database.DB.Create(&r).Error; err != nil {
		RespondErr(c, "添加失败")
		return
	}
	notify.SendMarkdown("有新的审计规则：" + r.Name)
	RespondData(c, "添加成功", gin.H{"id": r.ID})
}

// UpdateDetectRule 编辑审计规则 (PHP update: TG 通知)
func (h *Handler) UpdateDetectRule(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "规则ID无效")
		return
	}
	var r model.DetectRule
	if err := database.DB.First(&r, id).Error; err != nil {
		RespondErr(c, "规则不存在")
		return
	}
	var req struct {
		Name  string `json:"name"`
		Text  string `json:"text"`
		Regex string `json:"regex"`
		Type  int32  `json:"type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespondErr(c, "参数有误")
		return
	}
	r.Name, r.Text, r.Regex, r.Type = req.Name, req.Text, req.Regex, req.Type
	if err := database.DB.Save(&r).Error; err != nil {
		RespondErr(c, "修改失败")
		return
	}
	notify.SendMarkdown("规则更新：\n" + req.Name)
	RespondOK(c, "修改成功")
}

// DeleteDetectRule 删除审计规则
func (h *Handler) DeleteDetectRule(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "规则ID无效")
		return
	}
	if err := database.DB.Delete(&model.DetectRule{}, id).Error; err != nil {
		RespondErr(c, "删除失败")
		return
	}
	RespondOK(c, "删除成功")
}

var detectLogSortable = map[string]string{
	"id": "detect_log.id", "user_id": "detect_log.user_id", "node_id": "detect_log.node_id",
	"list_id": "detect_log.list_id", "datetime": "detect_log.datetime",
}

// ListDetectLogs 审计触发记录 (PHP ajax_log: 四表 JOIN)
func (h *Handler) ListDetectLogs(c *gin.Context) {
	q := ParseListQuery(c, detectLogSortable, "id", "desc")
	base := func() *gorm.DB {
		db := database.DB.Table("detect_log").
			Select("detect_log.id, detect_log.user_id, user.user_name, detect_log.node_id, node.name AS node_name, list.id AS rule_id, list.name AS rule_name, list.text AS rule_text, list.regex AS rule_regex, list.type AS rule_type, detect_log.datetime").
			Joins("JOIN user ON user.id = detect_log.user_id").
			Joins("JOIN ss_node AS node ON node.id = detect_log.node_id").
			Joins("JOIN detect_list AS list ON list.id = detect_log.list_id")
		if q.Search != "" {
			like := "%" + q.Search + "%"
			db = db.Where("user.user_name LIKE ? OR list.name LIKE ? OR node.name LIKE ?", like, like, like)
		}
		if v := c.Query("user_id"); v != "" {
			db = db.Where("detect_log.user_id = ?", v)
		}
		return db
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	type row struct {
		ID        int64  `json:"id" gorm:"column:id"`
		UserID    int64  `json:"user_id" gorm:"column:user_id"`
		UserName  string `json:"user_name" gorm:"column:user_name"`
		NodeID    int32  `json:"node_id" gorm:"column:node_id"`
		NodeName  string `json:"node_name" gorm:"column:node_name"`
		RuleID    int64  `json:"rule_id" gorm:"column:rule_id"`
		RuleName  string `json:"rule_name" gorm:"column:rule_name"`
		RuleText  string `json:"rule_text" gorm:"column:rule_text"`
		RuleRegex string `json:"rule_regex" gorm:"column:rule_regex"`
		RuleType  int32  `json:"rule_type" gorm:"column:rule_type"`
		DateTime  int64  `json:"datetime" gorm:"column:datetime"`
	}
	var rows []row
	dbq := base().Order(q.OrderBy + " " + q.OrderDir)
	if q.Limit >= 0 {
		dbq = dbq.Offset(q.Offset).Limit(q.Limit)
	}
	if err := dbq.Scan(&rows).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	RespondList(c, rows, total)
}

// ==================== 自动命令 (auto) ====================

var autoSortable = map[string]string{"id": "id", "datetime": "datetime", "type": "type"}

// ListAutos 自动命令列表
func (h *Handler) ListAutos(c *gin.Context) {
	q := ParseListQuery(c, autoSortable, "id", "desc")
	base := func() *gorm.DB {
		db := database.DB.Model(&model.Auto{})
		if q.Search != "" {
			db = db.Where("value LIKE ? OR sign LIKE ?", "%"+q.Search+"%", "%"+q.Search+"%")
		}
		return db
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	var autos []model.Auto
	dbq := base().Order(q.OrderBy + " " + q.OrderDir)
	if q.Limit >= 0 {
		dbq = dbq.Offset(q.Offset).Limit(q.Limit)
	}
	if err := dbq.Find(&autos).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	RespondList(c, autos, total)
}

// CreateAuto 下发命令 (PHP add: type=1)
func (h *Handler) CreateAuto(c *gin.Context) {
	var req struct {
		Content string `json:"content"`
		Sign    string `json:"sign"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Content == "" {
		RespondErr(c, "参数有误")
		return
	}
	a := model.Auto{DateTime: time.Now().Unix(), Value: req.Content, Sign: req.Sign, Type: 1}
	if err := database.DB.Create(&a).Error; err != nil {
		RespondErr(c, "添加失败")
		return
	}
	RespondData(c, "添加成功", gin.H{"id": a.ID})
}

// DeleteAuto 删除命令
func (h *Handler) DeleteAuto(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "命令ID无效")
		return
	}
	if err := database.DB.Delete(&model.Auto{}, id).Error; err != nil {
		RespondErr(c, "删除失败")
		return
	}
	RespondOK(c, "删除成功")
}

// ==================== IP 管理 ====================

var blockipSortable = map[string]string{"id": "blockip.id", "datetime": "blockip.datetime"}

// ListBlockIps 已封禁 IP (PHP ajax_block: JOIN 节点名; location 列不做 — R2 决议)
func (h *Handler) ListBlockIps(c *gin.Context) {
	q := ParseListQuery(c, blockipSortable, "id", "desc")
	base := func() *gorm.DB {
		db := database.DB.Table("blockip").
			Select("blockip.id, blockip.nodeid, node.name, blockip.ip, blockip.datetime").
			Joins("JOIN ss_node AS node ON node.id = blockip.nodeid")
		if q.Search != "" {
			db = db.Where("blockip.ip LIKE ? OR node.name LIKE ?", "%"+q.Search+"%", "%"+q.Search+"%")
		}
		return db
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	type row struct {
		ID       int64  `json:"id" gorm:"column:id"`
		NodeID   int32  `json:"nodeid" gorm:"column:nodeid"`
		Name     string `json:"name" gorm:"column:name"`
		IP       string `json:"ip" gorm:"column:ip"`
		DateTime int64  `json:"datetime" gorm:"column:datetime"`
	}
	var rows []row
	dbq := base().Order(q.OrderBy + " " + q.OrderDir)
	if q.Limit >= 0 {
		dbq = dbq.Offset(q.Offset).Limit(q.Limit)
	}
	if err := dbq.Scan(&rows).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	RespondList(c, rows, total)
}

var unblockipSortable = map[string]string{"id": "unblockip.id", "datetime": "unblockip.datetime"}

// ListUnblockIps 已解封 IP (PHP ajax_unblock: JOIN user)
func (h *Handler) ListUnblockIps(c *gin.Context) {
	q := ParseListQuery(c, unblockipSortable, "id", "desc")
	base := func() *gorm.DB {
		db := database.DB.Table("unblockip").
			Select("unblockip.id, unblockip.userid, user.user_name, unblockip.ip, unblockip.datetime").
			Joins("JOIN user ON user.id = unblockip.userid")
		if q.Search != "" {
			db = db.Where("unblockip.ip LIKE ? OR user.user_name LIKE ?", "%"+q.Search+"%", "%"+q.Search+"%")
		}
		return db
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	type row struct {
		ID       int64  `json:"id" gorm:"column:id"`
		UserID   int64  `json:"userid" gorm:"column:userid"`
		UserName string `json:"user_name" gorm:"column:user_name"`
		IP       string `json:"ip" gorm:"column:ip"`
		DateTime int64  `json:"datetime" gorm:"column:datetime"`
	}
	var rows []row
	dbq := base().Order(q.OrderBy + " " + q.OrderDir)
	if q.Limit >= 0 {
		dbq = dbq.Offset(q.Offset).Limit(q.Limit)
	}
	if err := dbq.Scan(&rows).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	RespondList(c, rows, total)
}

// DoUnblock 解封 (PHP doUnblock: 删 blockip 行 + 插 unblockip 命令)
func (h *Handler) DoUnblock(c *gin.Context) {
	var req struct {
		IP string `json:"ip"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.IP == "" {
		RespondErr(c, "请输入 IP")
		return
	}
	adminUser := c.MustGet("user").(*model.User)
	database.DB.Where("ip = ?", req.IP).Delete(&model.BlockIp{})
	row := model.UnblockIp{IP: req.IP, DateTime: time.Now().Unix(), UserID: int64(adminUser.ID)}
	if err := database.DB.Create(&row).Error; err != nil {
		RespondErr(c, "发送解封命令失败")
		return
	}
	RespondOK(c, "发送解封命令解封 "+req.IP+" 成功")
}

// ==================== 登录记录 / 在线 IP / 流量记录 ====================

var loginSortable = map[string]string{
	"id": "id", "userid": "userid", "ip": "ip", "datetime": "datetime", "type": "type",
}

// ListAuditLogins 登录记录 (PHP ajax_login: JOIN user; type 0 成功 1 失败)
func (h *Handler) ListAuditLogins(c *gin.Context) {
	q := ParseListQuery(c, loginSortable, "id", "desc")
	base := func() *gorm.DB {
		db := database.DB.Table("login_ip").
			Select("login_ip.id, login_ip.userid, user.user_name, login_ip.ip, login_ip.datetime, login_ip.type").
			Joins("JOIN user ON user.id = login_ip.userid")
		if q.Search != "" {
			like := "%" + q.Search + "%"
			db = db.Where("login_ip.ip LIKE ? OR user.user_name LIKE ? OR login_ip.userid LIKE ?", like, like, like)
		}
		if t := c.Query("type"); t != "" {
			db = db.Where("login_ip.type = ?", t)
		}
		return db
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	type row struct {
		ID       int64  `json:"id" gorm:"column:id"`
		UserID   int64  `json:"userid" gorm:"column:userid"`
		UserName string `json:"user_name" gorm:"column:user_name"`
		IP       string `json:"ip" gorm:"column:ip"`
		DateTime int64  `json:"datetime" gorm:"column:datetime"`
		Type     int32  `json:"type" gorm:"column:type"`
	}
	var rows []row
	dbq := base().Order(q.OrderBy + " " + q.OrderDir)
	if q.Limit >= 0 {
		dbq = dbq.Offset(q.Offset).Limit(q.Limit)
	}
	if err := dbq.Scan(&rows).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	RespondList(c, rows, total)
}

var aliveSortable = map[string]string{
	"id": "id", "userid": "userid", "nodeid": "nodeid", "ip": "ip", "datetime": "datetime",
}

// ListAuditAlive 在线 IP (PHP ajax_alive: JOIN user/node + is_node 判定)
func (h *Handler) ListAuditAlive(c *gin.Context) {
	q := ParseListQuery(c, aliveSortable, "id", "desc")
	base := func() *gorm.DB {
		db := database.DB.Table("alive_ip").
			Select("alive_ip.id, alive_ip.userid, user.user_name, alive_ip.nodeid, node.name AS node_name, alive_ip.ip, alive_ip.datetime").
			Joins("JOIN user ON user.id = alive_ip.userid").
			Joins("LEFT JOIN ss_node AS node ON node.id = alive_ip.nodeid")
		if q.Search != "" {
			like := "%" + q.Search + "%"
			db = db.Where("alive_ip.ip LIKE ? OR user.user_name LIKE ? OR node.name LIKE ?", like, like, like)
		}
		return db
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	// 节点 IP 集合 (is_node 判定)
	nodeIPSet := map[string]bool{}
	var nodeIPs []string
	database.DB.Model(&model.Node{}).Where("node_ip != ''").Pluck("node_ip", &nodeIPs)
	for _, nip := range nodeIPs {
		for _, p := range splitIPList(nip) {
			nodeIPSet[p] = true
		}
	}

	type row struct {
		ID       int64  `json:"id" gorm:"column:id"`
		UserID   int64  `json:"userid" gorm:"column:userid"`
		UserName string `json:"user_name" gorm:"column:user_name"`
		NodeID   int32  `json:"nodeid" gorm:"column:nodeid"`
		NodeName string `json:"node_name" gorm:"column:node_name"`
		IP       string `json:"ip" gorm:"column:ip"`
		DateTime int64  `json:"datetime" gorm:"column:datetime"`
		IsNode   bool   `json:"is_node"`
	}
	var raw []row
	dbq := base().Order(q.OrderBy + " " + q.OrderDir)
	if q.Limit >= 0 {
		dbq = dbq.Offset(q.Offset).Limit(q.Limit)
	}
	if err := dbq.Scan(&raw).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	for i := range raw {
		raw[i].IsNode = nodeIPSet[raw[i].IP]
	}
	RespondList(c, raw, total)
}

// splitIPList node_ip 支持 "ip1,ip2" 与 "ip#id|id" 格式, 提取纯 IP 部分
func splitIPList(s string) []string {
	out := make([]string, 0)
	for _, part := range splitComma(s) {
		if idx := indexByte(part, '#'); idx >= 0 {
			part = part[:idx]
		}
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

var trafficLogSortable = map[string]string{
	"id": "log.id", "user_id": "log.user_id", "node_id": "log.node_id",
	"rate": "log.rate", "log_time": "log.log_time",
}

// ListTrafficLogs 用户流量记录 (PHP ajax_trafficLog: user×node×倍率×结算流量)
func (h *Handler) ListTrafficLogs(c *gin.Context) {
	q := ParseListQuery(c, trafficLogSortable, "id", "desc")
	base := func() *gorm.DB {
		db := database.DB.Table("user_traffic_log AS log").
			Select("log.id, log.user_id, user.user_name, node.name AS node_name, log.rate, (log.u + log.d) AS origin_traffic, log.traffic, log.log_time").
			Joins("JOIN user ON user.id = log.user_id").
			Joins("JOIN ss_node AS node ON node.id = log.node_id")
		if q.Search != "" {
			like := "%" + q.Search + "%"
			db = db.Where("user.user_name LIKE ? OR node.name LIKE ? OR log.user_id LIKE ?", like, like, like)
		}
		if v := c.Query("user_id"); v != "" {
			db = db.Where("log.user_id = ?", v)
		}
		return db
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	type row struct {
		ID            int64   `json:"id" gorm:"column:id"`
		UserID        int64   `json:"user_id" gorm:"column:user_id"`
		UserName      string  `json:"user_name" gorm:"column:user_name"`
		NodeName      string  `json:"node_name" gorm:"column:node_name"`
		Rate          float64 `json:"rate" gorm:"column:rate"`
		OriginTraffic string  `json:"origin_traffic"` // 已人性化 (bytes → 可读)
		Traffic       int64   `json:"traffic" gorm:"column:traffic"`
		LogTime       int64   `json:"log_time" gorm:"column:log_time"`
	}
	var raw []struct {
		row
		Origin int64 `gorm:"column:origin_traffic"`
	}
	dbq := base().Order(q.OrderBy + " " + q.OrderDir)
	if q.Limit >= 0 {
		dbq = dbq.Offset(q.Offset).Limit(q.Limit)
	}
	if err := dbq.Scan(&raw).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	rows := make([]row, 0, len(raw))
	for _, r := range raw {
		r.row.OriginTraffic = flowAuto(r.Origin)
		rows = append(rows, r.row)
	}
	RespondList(c, rows, total)
}

var _ = context.Background
