package adminv2

import (
	"net"
	"strconv"
	"strings"
	"time"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/notify"
	"spanel-go/internal/pkg/radius"
	"spanel-go/internal/pkg/tools"
	"spanel-go/internal/service"

	"github.com/gin-gonic/gin"
)

/* 节点管理 — 对齐 PHP Admin\NodeController (27 列/CRUD/联动语义/nodectl) */

var nodeSortable = map[string]string{
	"id": "id", "name": "name", "type": "type", "node_sort": "node_sort", "sort": "sort",
	"status": "status", "server": "server", "node_ip": "node_ip", "traffic_rate": "traffic_rate",
	"node_cost": "node_cost", "node_online": "node_online", "node_oncost": "node_oncost",
	"node_group": "node_group", "node_class": "node_class", "node_speedlimit": "node_speedlimit",
	"node_bandwidth": "node_bandwidth", "traffic_used": "traffic_used", "traffic_limit": "traffic_limit",
	"traffic_reset_day": "traffic_reset_day", "node_heartbeat": "node_heartbeat",
	"custom_method": "custom_method", "custom_rss": "custom_rss", "mu_only": "mu_only", "info": "info",
}

var sortNames = map[int32]string{
	0: "SS", 1: "VPN/Radius基础", 2: "SSH", 5: "Anyconnect", 9: "Sr-单多",
	10: "Ss-中转", 11: "V2", 12: "CDN", 13: "Vl", 14: "Tr", 15: "Hy2",
}

func sortName(s int32) string {
	if n, ok := sortNames[s]; ok {
		return n
	}
	return "?"
}

// ListNodes 节点列表 (PHP node/ajax: ss_node 全量, 27 列; 计算列前端渲染)
func (h *Handler) ListNodes(c *gin.Context) {
	q := ParseListQuery(c, nodeSortable, "id", "asc")

	query := database.DB.Model(&model.Node{})
	if q.Search != "" {
		like := "%" + q.Search + "%"
		query = query.Where("name LIKE ? OR server LIKE ? OR node_ip LIKE ? OR info LIKE ?", like, like, like, like)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	dbq := database.DB.Model(&model.Node{})
	if q.Search != "" {
		like := "%" + q.Search + "%"
		dbq = dbq.Where("name LIKE ? OR server LIKE ? OR node_ip LIKE ? OR info LIKE ?", like, like, like, like)
	}
	var nodes []model.Node
	if q.Limit < 0 {
		dbq = dbq.Order(q.OrderBy + " " + q.OrderDir)
	} else {
		dbq = dbq.Order(q.OrderBy + " " + q.OrderDir).Offset(q.Offset).Limit(q.Limit)
	}
	if err := dbq.Find(&nodes).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}

	type item struct {
		ID              int32   `json:"id"`
		Name            string  `json:"name"`
		Type            int32   `json:"type"`
		NodeSort        int32   `json:"node_sort"`
		Sort            int32   `json:"sort"`
		Status          string  `json:"status"`
		Server          string  `json:"server"`
		NodeIP          string  `json:"node_ip"`
		TrafficRate     float32 `json:"traffic_rate"`
		NodeCost        int32   `json:"node_cost"`
		NodeOnline      int32   `json:"node_online"`
		NodeOnCost      float32 `json:"node_oncost"`
		NodeGroup       int32   `json:"node_group"`
		NodeClass       int32   `json:"node_class"`
		NodeSpeedlimit  string  `json:"node_speedlimit"`
		NodeBandwidth   string  `json:"node_bandwidth"`
		TrafficUsed     string  `json:"traffic_used"`  // GB
		TrafficLimit    string  `json:"traffic_limit"` // GB
		TrafficResetDay int32   `json:"traffic_reset_day"`
		NodeHeartbeat   string  `json:"node_heartbeat"`
		CustomMethod    int8    `json:"custom_method"`
		CustomRSS       int32   `json:"custom_rss"`
		MuOnly          int32   `json:"mu_only"`
		Info            string  `json:"info"`
		HeartbeatOk     bool    `json:"heartbeat_ok"` // 300s 内有心跳
	}
	items := make([]item, 0, len(nodes))
	now := time.Now().Unix()
	for _, n := range nodes {
		hb := ""
		if n.NodeHeartbeat > 0 {
			hb = time.Unix(n.NodeHeartbeat, 0).Format("2006-01-02 15:04:05")
		}
		items = append(items, item{
			ID: n.ID, Name: n.Name, Type: n.Type, NodeSort: n.NodeSort, Sort: n.Sort,
			Status: n.Status, Server: n.Server, NodeIP: n.NodeIP, TrafficRate: n.TrafficRate,
			NodeCost: n.NodeCost, NodeOnline: n.NodeOnline, NodeOnCost: n.NodeOnCost,
			NodeGroup: n.NodeGroup, NodeClass: n.NodeClass, NodeSpeedlimit: n.NodeSpeedlimit,
			NodeBandwidth:   n.NodeBandwidth,
			TrafficUsed:     tools.FlowAutoShow(float64(n.TrafficUsed)),
			TrafficLimit:    tools.FlowAutoShow(float64(n.TrafficLimit)),
			TrafficResetDay: n.TrafficResetDay, NodeHeartbeat: hb,
			CustomMethod: n.CustomMethod, CustomRSS: n.CustomRSS, MuOnly: n.MuOnly,
			Info: n.Info, HeartbeatOk: now-n.NodeHeartbeat < 300,
		})
	}
	RespondList(c, items, total)
}

// nodeForm 编辑/创建共用的表单字段 (PHP create/edit tpl + add/update 参数)
type nodeForm struct {
	Name               string  `json:"name"`
	Server             string  `json:"server"`
	Method             string  `json:"method"`
	CustomMethod       int32   `json:"custom_method"`
	CustomRSS          int32   `json:"custom_rss"`
	MuOnly             int32   `json:"mu_only"`
	Rate               float64 `json:"rate"`
	Cost               int32   `json:"cost"`
	Info               string  `json:"info"`
	Type               int32   `json:"type"`
	Group              int32   `json:"group"`
	NodeBandwidth      string  `json:"node_bandwidth"` // 标称带宽 Mbps
	Status             string  `json:"status"`
	Sort               int32   `json:"sort"`
	NodeIP             string  `json:"node_ip"`
	Class              int32   `json:"class"`
	NodeBandwidthLimit float64 `json:"node_bandwidth_limit"` // 月流量限额 GB
	TrafficResetDay    int32   `json:"traffic_reset_day"`
	// 仅编辑
	NodeSort             int32    `json:"node_sort"`
	TrafficUsedCalibrate *float64 `json:"traffic_used_calibrate"` // GB
	ServerUptimeDays     *float64 `json:"server_uptime_days"`
}

func (f *nodeForm) apply(n *model.Node) {
	gb := int64(1024 * 1024 * 1024)
	n.Name = f.Name
	n.Server = strings.TrimSpace(f.Server)
	n.Method = f.Method
	n.CustomMethod = int8(f.CustomMethod)
	n.CustomRSS = f.CustomRSS
	n.MuOnly = f.MuOnly
	n.TrafficRate = float32(f.Rate) // 手动倍率 (PHP TrafficRateCalculator::applyManualRate)
	n.NodeCost = f.Cost
	n.Info = f.Info
	n.Type = f.Type
	n.NodeGroup = f.Group
	n.NodeBandwidth = f.NodeBandwidth
	n.Status = f.Status
	n.Sort = f.Sort
	n.NodeIP = strings.TrimSpace(f.NodeIP)
	if n.NodeIP == "" {
		n.NodeIP = n.Server
	}
	n.NodeClass = f.Class
	n.TrafficLimit = int64(f.NodeBandwidthLimit * float64(gb))
	n.TrafficResetDay = f.TrafficResetDay
}

// GetNode 编辑页数据
func (h *Handler) GetNode(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "节点ID无效")
		return
	}
	var n model.Node
	if err := database.DB.First(&n, id).Error; err != nil {
		RespondErr(c, "节点不存在")
		return
	}
	gb := 1024.0 * 1024 * 1024
	RespondData(c, "获取成功", gin.H{
		"id": n.ID, "name": n.Name, "server": n.Server, "method": n.Method,
		"custom_method": n.CustomMethod, "custom_rss": n.CustomRSS, "mu_only": n.MuOnly,
		"rate": n.TrafficRate, "cost": n.NodeCost, "info": n.Info, "type": n.Type,
		"group": n.NodeGroup, "node_bandwidth": n.NodeBandwidth, "status": n.Status,
		"sort": n.Sort, "node_ip": n.NodeIP, "class": n.NodeClass,
		"node_bandwidth_limit": float64(n.TrafficLimit) / gb,
		"traffic_reset_day":    n.TrafficResetDay,
		"node_sort":            n.NodeSort,
		"traffic_used_gb":      float64(n.TrafficUsed) / gb,
		"traffic_left_gb":      float64(n.TrafficLeft) / gb,
		"server_uptime_days":   float64(n.ServerUptime) / 86400,
		"is_clone":             n.IsClone,
		"node_heartbeat":       n.NodeHeartbeat,
		"heartbeat_ok":         time.Now().Unix()-n.NodeHeartbeat < 300,
	})
}

// CreateNode 添加节点 (PHP add: 建行 + resetCycle 归零 + Telegram 通知)
func (h *Handler) CreateNode(c *gin.Context) {
	var f nodeForm
	if err := c.ShouldBindJSON(&f); err != nil {
		RespondErr(c, "参数有误: "+err.Error())
		return
	}
	n := model.Node{}
	f.apply(&n)
	service.MonthlyResetCycle(&n) // 流量字段初始化 (归零 used + 重算 left)
	if err := database.DB.Create(&n).Error; err != nil {
		RespondErr(c, "节点添加失败: "+err.Error())
		return
	}
	notify.Send("新节点添加~" + f.Name)
	RespondData(c, "节点添加成功", gin.H{"id": n.ID})
}

// UpdateNode 编辑节点 (PHP update 全语义: 打戳/Radius NAS/config_hash 置脏/流量校准/运行天数)
func (h *Handler) UpdateNode(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "节点ID无效")
		return
	}
	var n model.Node
	if err := database.DB.First(&n, id).Error; err != nil {
		RespondErr(c, "节点不存在")
		return
	}
	oldClass := n.NodeClass
	oldGroup := n.NodeGroup

	var f nodeForm
	if err := c.ShouldBindJSON(&f); err != nil {
		RespondErr(c, "参数有误: "+err.Error())
		return
	}
	gb := 1024.0 * 1024 * 1024
	f.apply(&n)

	// sort=1 (VPN/Radius) → 注册 NAS (PHP: 同 server 的 SS 节点 5 分钟内心跳过或不存在时)
	if n.Sort == 1 {
		var ssNode model.Node
		err := database.DB.Where("sort = 0 AND server = ?", f.Server).First(&ssNode).Error
		if err != nil || time.Now().Unix()-ssNode.NodeHeartbeat < 300 || ssNode.NodeHeartbeat == 0 {
			radius.AddNas(resolveHost(f.Server), f.Server)
		}
	}

	n.NodeSort = f.NodeSort

	// 流量校准 (GB → bytes, 手动覆盖)
	if f.TrafficUsedCalibrate != nil && *f.TrafficUsedCalibrate >= 0 {
		n.TrafficUsed = int64(*f.TrafficUsedCalibrate * gb)
	}

	// 重算周期派生字段 (left + daily)
	service.MonthlyApplyDerived(&n)

	// server/method/sort 等影响 xray 配置 → 保存即置脏 config_hash (节点心跳惰性重算)
	n.ConfigHash = ""

	// 运行天数校准 (天 → 秒)
	if f.ServerUptimeDays != nil && *f.ServerUptimeDays >= 0 {
		n.ServerUptime = int32(*f.ServerUptimeDays * 86400)
	}

	if err := database.DB.Save(&n).Error; err != nil {
		RespondErr(c, "修改失败: "+err.Error())
		return
	}

	// 等级/分组实际变化 → 全表打戳 (增量同步 bump, 各节点当轮全量重算资格)
	if n.NodeClass != oldClass || n.NodeGroup != oldGroup {
		_ = database.DB.Exec("UPDATE `user` SET `status_changed_at` = UNIX_TIMESTAMP()").Error
	}

	notify.Send("节点修改~~" + f.Name + "VIP等级 " + strconv.Itoa(int(n.NodeClass)) + " #" + strconv.FormatInt(id, 10))
	RespondOK(c, "修改成功")
}

// resolveHost server → IP (PHP gethostbyname)
func resolveHost(server string) string {
	host := server
	if strings.Contains(host, ":") {
		h, _, err := net.SplitHostPort(host)
		if err == nil {
			host = h
		}
	}
	if ips, err := net.LookupIP(host); err == nil && len(ips) > 0 {
		return ips[0].String()
	}
	return host
}

// DeleteNode 删除节点 (PHP delete: Radius DelNas + NodePurgeService 全量级联 + Telegram)
func (h *Handler) DeleteNode(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "节点ID无效")
		return
	}
	var n model.Node
	if err := database.DB.First(&n, id).Error; err != nil {
		RespondErr(c, "节点不存在")
		return
	}
	if n.Sort == 1 {
		radius.DelNas(n.NodeIP)
	}
	name := n.Name
	service.PurgeNode(&n, "all", 0)
	notify.Send("节点被删除~" + name)
	RespondOK(c, "删除成功")
}

// NodeCtl 节点调整 (PHP nodectl: group×class 筛选 + 心跳/订阅/健康分着色字段)
func (h *Handler) NodeCtl(c *gin.Context) {
	group, _ := strconv.Atoi(c.DefaultQuery("group", "0"))
	class, _ := strconv.Atoi(c.DefaultQuery("class", "10"))

	var nodes []model.Node
	database.DB.
		Where("node_group = ? AND is_clone = 0 AND node_class <= ?", group, class).
		Order("type DESC").Order("custom_rss ASC").Order("traffic_rate DESC").
		Find(&nodes)

	type item struct {
		ID            int32   `json:"id"`
		Name          string  `json:"name"`
		Type          int32   `json:"type"`
		Sort          int32   `json:"sort"`
		TrafficRate   float32 `json:"traffic_rate"`
		NodeCost      int32   `json:"node_cost"`
		NodeOnline    int32   `json:"node_online"`
		NodeOnCost    float32 `json:"node_oncost"`
		NodeClass     int32   `json:"node_class"`
		NodeBandwidth string  `json:"node_bandwidth"`
		CustomRSS     int32   `json:"custom_rss"`
		NodeHeartbeat int64   `json:"node_heartbeat"`
		HeartbeatOk   bool    `json:"heartbeat_ok"` // 红色: 5 分钟无心跳
		Status        string  `json:"status"`
		TrafficUsed   string  `json:"traffic_used_gb"`
		TrafficLimit  string  `json:"traffic_limit_gb"`
		Info          string  `json:"info"`
	}
	now := time.Now().Unix()
	items := make([]item, 0, len(nodes))
	for _, n := range nodes {
		items = append(items, item{
			ID: n.ID, Name: n.Name, Type: n.Type, Sort: n.Sort, TrafficRate: n.TrafficRate,
			NodeCost: n.NodeCost, NodeOnline: n.NodeOnline, NodeOnCost: n.NodeOnCost,
			NodeClass: n.NodeClass, NodeBandwidth: n.NodeBandwidth, CustomRSS: n.CustomRSS,
			NodeHeartbeat: n.NodeHeartbeat, HeartbeatOk: now-n.NodeHeartbeat < 300,
			Status:       n.Status,
			TrafficUsed:  tools.FlowAutoShow(float64(n.TrafficUsed)),
			TrafficLimit: tools.FlowAutoShow(float64(n.TrafficLimit)),
			Info:         n.Info,
		})
	}
	RespondData(c, "获取成功", gin.H{"items": items})
}
