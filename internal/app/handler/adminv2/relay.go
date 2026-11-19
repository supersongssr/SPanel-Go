package adminv2

import (
	"strconv"
	"strings"
	"time"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

/* 中转规则管理 — 对齐 PHP Admin\RelayController (CRUD/冲突检测/path_search 链路推导) */

var relaySortable = map[string]string{
	"id": "relay.id", "user_id": "relay.user_id", "port": "relay.port", "priority": "relay.priority",
}

// ListRelays 中转规则列表 (PHP ajax_relay: JOIN user + 节点名; user_id=0 → 全体用户)
func (h *Handler) ListRelays(c *gin.Context) {
	q := ParseListQuery(c, relaySortable, "id", "asc")
	listRelaysImpl(c, q)
}

// getRelayNodeIp 对齐 PHP Tools::getRelayNodeIp: dist 节点 node_ip 支持 "ip1#id1|id2,ip2" 定向格式
func getRelayNodeIp(sourceNode *model.Node, distNode *model.Node) string {
	ret := ""
	for _, part := range strings.Split(distNode.NodeIP, ",") {
		child := strings.SplitN(part, "#", 2)
		if len(child) == 1 {
			ret = child[0]
			continue
		}
		if len(child) == 2 {
			ids := strings.Split(child[1], "|")
			for _, id := range ids {
				if id == strconv.Itoa(int(sourceNode.ID)) {
					return child[0]
				}
			}
		}
	}
	return ret
}

// hasConflictRule 对齐 PHP Tools::has_conflict_rule (递归环检测 + 同节点回环 + 起源为0的目标是中转节点)
func hasConflictRule(input *model.Relay, ruleset []model.Relay, editRuleID int64, originNodeID int64) int64 {
	for i := range ruleset {
		rule := &ruleset[i]
		if rule.SourceNodeID == input.DistNodeID &&
			((rule.Port == input.Port || input.Port == 0) || rule.Port == 0) {
			if rule.DistNodeID == originNodeID && rule.ID != editRuleID {
				return rule.ID
			}
			// 递归处理该节点
			if id := hasConflictRule(rule, ruleset, editRuleID, originNodeID); id != 0 {
				return id
			}
		}
	}
	if (input.ID == editRuleID || editRuleID == 0) && input.DistNodeID != -1 {
		var distNode model.Node
		if err := database.DB.First(&distNode, input.DistNodeID).Error; err == nil {
			if input.SourceNodeID == 0 && distNode.Sort == 10 {
				return -1
			}
		}
		if input.DistNodeID == input.SourceNodeID {
			return -1
		}
	}
	return 0
}

func listRelaysImpl(c *gin.Context, q *ListQuery) {
	base := func() *gorm.DB {
		db := database.DB.Table("relay").
			Select("relay.id, relay.user_id, user.user_name, sn.name AS source_node_name, dn.name AS dist_node_name, relay.source_node_id, relay.dist_node_id, relay.dist_ip, relay.port, relay.priority").
			Joins("LEFT JOIN user ON user.id = relay.user_id").
			Joins("JOIN ss_node sn ON sn.id = relay.source_node_id").
			Joins("JOIN ss_node dn ON dn.id = relay.dist_node_id").
			Group("relay.id")
		if q.Search != "" {
			like := "%" + q.Search + "%"
			db = db.Where("user.user_name LIKE ? OR sn.name LIKE ? OR dn.name LIKE ? OR relay.user_id LIKE ?", like, like, like, like)
		}
		return db
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	type row struct {
		ID             int64  `json:"id" gorm:"column:id"`
		UserID         int64  `json:"user_id" gorm:"column:user_id"`
		UserName       string `json:"user_name" gorm:"column:user_name"`
		SourceNodeID   int64  `json:"source_node_id" gorm:"column:source_node_id"`
		SourceNodeName string `json:"source_node_name" gorm:"column:source_node_name"`
		DistNodeID     int64  `json:"dist_node_id" gorm:"column:dist_node_id"`
		DistNodeName   string `json:"dist_node_name" gorm:"column:dist_node_name"`
		DistIP         string `json:"dist_ip" gorm:"column:dist_ip"`
		Port           int32  `json:"port" gorm:"column:port"`
		Priority       int32  `json:"priority" gorm:"column:priority"`
	}
	var rows []row
	dbq := base().Order(q.OrderBy + " " + q.OrderDir) // 白名单值已带 relay. 前缀
	if q.Limit >= 0 {
		dbq = dbq.Offset(q.Offset).Limit(q.Limit)
	}
	if err := dbq.Scan(&rows).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	for i := range rows {
		if rows[i].UserID == 0 {
			rows[i].UserName = "全体用户"
		}
	}
	RespondList(c, rows, total)
}

// relayNodeOptions 下拉数据 (PHP create/edit: 起源 sort=10; 目标 sort=0 或 10)
func (h *Handler) RelayNodeOptions(c *gin.Context) {
	var source, dist []model.Node
	database.DB.Where("sort = 10").Order("name").Find(&source)
	database.DB.Where("sort = 0 OR sort = 10").Order("name").Find(&dist)
	type opt struct {
		ID   int32  `json:"id"`
		Name string `json:"name"`
	}
	src := make([]opt, 0, len(source))
	for _, n := range source {
		src = append(src, opt{ID: n.ID, Name: n.Name})
	}
	ds := make([]opt, 0, len(dist)+1)
	ds = append(ds, opt{ID: -1, Name: "不进行中转"}) // PHP add/edit: -1 = 不中转
	for _, n := range dist {
		ds = append(ds, opt{ID: n.ID, Name: n.Name})
	}
	RespondData(c, "获取成功", gin.H{"source_nodes": src, "dist_nodes": ds})
}

// GetRelay 编辑页数据 (规则 + 下拉)
func (h *Handler) GetRelay(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "规则ID无效")
		return
	}
	var r model.Relay
	if err := database.DB.First(&r, id).Error; err != nil {
		RespondErr(c, "规则不存在")
		return
	}
	// 下拉复用
	var source, dist []model.Node
	database.DB.Where("sort = 10").Order("name").Find(&source)
	database.DB.Where("sort = 0 OR sort = 10").Order("name").Find(&dist)
	RespondData(c, "获取成功", gin.H{
		"id": r.ID, "user_id": r.UserID, "source_node_id": r.SourceNodeID,
		"dist_node_id": r.DistNodeID, "dist_ip": r.DistIP, "port": r.Port, "priority": r.Priority,
		"source_nodes": source, "dist_nodes": dist,
	})
}

// relayForm PHP add/update 参数
type relayForm struct {
	UserID       int64 `json:"user_id"`
	SourceNodeID int64 `json:"source_node"`
	DistNodeID   int64 `json:"dist_node"`
	Port         int32 `json:"port"`
	Priority     int32 `json:"priority"`
}

// CreateRelay 添加规则 (PHP add: 节点校验 + dist_ip 推导 + 冲突检测)
func (h *Handler) CreateRelay(c *gin.Context) {
	var f relayForm
	if err := c.ShouldBindJSON(&f); err != nil {
		RespondErr(c, "参数有误: "+err.Error())
		return
	}
	var sourceNode model.Node
	if err := database.DB.First(&sourceNode, f.SourceNodeID).Error; err != nil {
		RespondErr(c, "起源节点错误。")
		return
	}
	var distNode model.Node
	if err := database.DB.First(&distNode, f.DistNodeID).Error; err != nil {
		RespondErr(c, "目标节点错误。")
		return
	}
	rule := model.Relay{
		UserID: f.UserID, DistNodeID: f.DistNodeID,
		DistIP:       getRelayNodeIp(&sourceNode, &distNode),
		SourceNodeID: f.SourceNodeID, Port: f.Port, Priority: f.Priority,
	}
	var ruleset []model.Relay
	if f.UserID == 0 {
		database.DB.Find(&ruleset)
	} else {
		database.DB.Where("user_id = ? OR user_id = 0", f.UserID).Find(&ruleset)
	}
	if id := hasConflictRule(&rule, ruleset, 0, rule.SourceNodeID); id != 0 {
		if id == -1 {
			RespondErr(c, "您即将添加的规则可能会造成冲突！")
		} else {
			RespondErr(c, "您即将添加的规则与规则 ID:"+strconv.FormatInt(id, 10)+" 冲突！")
		}
		return
	}
	if err := database.DB.Create(&rule).Error; err != nil {
		RespondErr(c, "添加失败")
		return
	}
	RespondData(c, "添加成功", gin.H{"id": rule.ID})
}

// UpdateRelay 编辑规则 (PHP update)
func (h *Handler) UpdateRelay(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "规则ID无效")
		return
	}
	var rule model.Relay
	if err := database.DB.First(&rule, id).Error; err != nil {
		RespondErr(c, "规则不存在")
		return
	}
	var f relayForm
	if err := c.ShouldBindJSON(&f); err != nil {
		RespondErr(c, "参数有误")
		return
	}
	var sourceNode model.Node
	if err := database.DB.First(&sourceNode, f.SourceNodeID).Error; err != nil && f.SourceNodeID != 0 {
		RespondErr(c, "起源节点 ID 错误。")
		return
	}
	var distNode model.Node
	if err := database.DB.First(&distNode, f.DistNodeID).Error; err != nil {
		RespondErr(c, "目标节点 ID 错误。")
		return
	}
	rule.UserID = f.UserID
	rule.DistNodeID = f.DistNodeID
	rule.DistIP = getRelayNodeIp(&sourceNode, &distNode)
	rule.SourceNodeID = f.SourceNodeID
	rule.Port = f.Port
	rule.Priority = f.Priority

	var ruleset []model.Relay
	if f.UserID == 0 {
		database.DB.Find(&ruleset)
	} else {
		database.DB.Where("user_id = ? OR user_id = 0", f.UserID).Find(&ruleset)
	}
	if cid := hasConflictRule(&rule, ruleset, rule.ID, rule.SourceNodeID); cid != 0 {
		if cid == -1 {
			RespondErr(c, "您即将添加的规则可能会造成冲突！")
		} else {
			RespondErr(c, "您即将添加的规则与规则 ID:"+strconv.FormatInt(cid, 10)+" 冲突！")
		}
		return
	}
	if err := database.DB.Save(&rule).Error; err != nil {
		RespondErr(c, "修改失败")
		return
	}
	RespondOK(c, "修改成功")
}

// DeleteRelay 删除规则
func (h *Handler) DeleteRelay(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "规则ID无效")
		return
	}
	if err := database.DB.Delete(&model.Relay{}, id).Error; err != nil {
		RespondErr(c, "删除失败")
		return
	}
	RespondOK(c, "删除成功")
}

// pickOutRelayRule 对齐 PHP Tools::pick_out_relay_rule (端口/起源匹配 + 优先级最高者)
func pickOutRelayRule(relayNodeID int64, port int32, ruleset []model.Relay) *model.Relay {
	var match *model.Relay
	for i := range ruleset {
		single := &ruleset[i]
		if (single.Port == port || single.Port == 0) && (single.SourceNodeID == 0 || single.SourceNodeID == relayNodeID) {
			hasHigher := false
			for j := range ruleset {
				pr := &ruleset[j]
				if (pr.Port == port || pr.Port == 0) && (pr.SourceNodeID == 0 || pr.SourceNodeID == relayNodeID) {
					if (pr.Priority > single.Priority && pr.ID != single.ID) ||
						(pr.Priority == single.Priority && pr.ID < single.ID) {
						hasHigher = true
						break
					}
				}
			}
			if hasHigher {
				continue
			}
			match = single
		}
	}
	return match
}

// isNodeAccessable 对齐 PHP Node::isNodeAccessable: 未超额 + 在线 + custom_rss=1
func isNodeAccessable(n *model.Node) bool {
	// isNodeTrafficOut: 有限额且已用 ≥ 限额
	trafficOut := n.TrafficLimit > 0 && n.TrafficUsed >= n.TrafficLimit
	// isNodeOnline: 90s 内有心跳
	online := n.NodeHeartbeat > time.Now().Unix()-90
	return !trafficOut && online && n.CustomRSS == 1
}

// RelayPathSearch 按用户推导中转链路 (PHP path_search)
func (h *Handler) RelayPathSearch(c *gin.Context) {
	uid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || uid <= 0 {
		RespondErr(c, "用户ID无效")
		return
	}
	type pathItem struct {
		Path   string `json:"path"`
		Status string `json:"status"`
		Port   int32  `json:"port"`
		Begin  int64  `json:"begin_node_id"`
		End    int64  `json:"end_node_id"`
	}
	paths := make([]pathItem, 0)

	var user model.User
	if err := database.DB.First(&user, uid).Error; err != nil {
		RespondData(c, "获取成功", gin.H{"user": nil, "paths": paths})
		return
	}

	// 用户可见中转节点 (sort=10, 等级/分组匹配)
	var nodes []model.Node
	database.DB.Where("(node_group = ? OR node_group = 0) AND sort = 10 AND node_class <= ?", user.NodeGroup, user.Class).
		Order("name").Find(&nodes)

	var relayRules []model.Relay
	database.DB.Where("user_id = ? OR user_id = 0", user.ID).Find(&relayRules)

	// 单端口多用户承载节点 (sort=9)
	var muNodes []model.Node
	database.DB.Where("sort = 9 AND node_class <= ? AND type = 1 AND (node_group = ? OR node_group = 0)", user.Class, user.NodeGroup).Find(&muNodes)

	nodeMap := map[int64]*model.Node{}
	{
		var all []model.Node
		database.DB.Find(&all)
		for i := range all {
			nodeMap[int64(all[i].ID)] = &all[i]
		}
	}

	// insertPath (PHP insertPathRule 简化等价: 拼接链路 + 可达性判定)
	insert := func(rule *model.Relay, port int32) {
		if rule.DistNodeID == -1 {
			return
		}
		for i := range paths {
			if paths[i].Port == port {
				if rule.DistNodeID == paths[i].Begin {
					bn := nodeMap[rule.SourceNodeID]
					if bn != nil {
						ok := isNodeAccessable(bn)
						prefix := bn.Name
						if !ok {
							prefix = "❌" + bn.Name
							paths[i].Status = "阻断"
						} else {
							paths[i].Status = "通畅" // PHP: 前驱可达则恢复通畅
						}
						paths[i].Path = prefix + " → " + paths[i].Path
						paths[i].Begin = rule.SourceNodeID
					}
					return
				}
				if paths[i].End == rule.SourceNodeID {
					en := nodeMap[rule.DistNodeID]
					if en != nil {
						ok := isNodeAccessable(en)
						suffix := en.Name
						if !ok {
							suffix = "❌" + en.Name
							paths[i].Status = "阻断"
						}
						paths[i].Path = paths[i].Path + " → " + suffix
						paths[i].End = rule.DistNodeID
					}
					return
				}
			}
		}
		bn, en := nodeMap[rule.SourceNodeID], nodeMap[rule.DistNodeID]
		if bn == nil || en == nil {
			return
		}
		p := pathItem{Port: port, Begin: rule.SourceNodeID, End: rule.DistNodeID}
		bOK, eOK := isNodeAccessable(bn), isNodeAccessable(en)
		p.Path = bn.Name + " → " + en.Name
		if !bOK {
			p.Path = "❌" + bn.Name + " → " + en.Name
		}
		if !eOK {
			p.Path = strings.SplitN(p.Path, " → ", 2)[0] + " → ❌" + en.Name
		}
		if !bOK || !eOK {
			p.Status = "阻断"
		} else {
			p.Status = "通畅"
		}
		paths = append(paths, p)
	}

	for i := range nodes {
		node := &nodes[i]
		if node.MuOnly == 0 {
			if rule := pickOutRelayRule(int64(node.ID), user.Port, relayRules); rule != nil {
				insert(rule, user.Port)
			}
		}
		if node.CustomRSS == 1 {
			for j := range muNodes {
				muNode := &muNodes[j]
				var muUser model.User
				portNum, _ := strconv.Atoi(muNode.Server)
				if err := database.DB.Where("port = ?", portNum).First(&muUser).Error; err != nil {
					continue
				}
				if !(muUser.Class >= node.NodeClass && (node.NodeGroup == 0 || node.NodeGroup == muUser.NodeGroup)) {
					continue
				}
				if muUser.IsMultiUser != 2 {
					if rule := pickOutRelayRule(int64(node.ID), muUser.Port, relayRules); rule != nil {
						insert(rule, muUser.Port)
					}
				}
			}
		}
	}

	// 第二轮链路合并 (PHP path_search 末段: 同端口两条存量链路两两合并, 被吸收方移除)
	merged := true
	for merged {
		merged = false
		for a := 0; a < len(paths); a++ {
			for b := 0; b < len(paths); b++ {
				if a == b || paths[a].Port != paths[b].Port {
					continue
				}
				// b 链尾 == a 链头 → a 前接 b
				if paths[b].End == paths[a].Begin {
					paths[a].Begin = paths[b].Begin
					paths[a].Path = paths[b].Path + " → " + paths[a].Path
					paths = append(paths[:b], paths[b+1:]...)
					merged = true
					break
				}
				// a 链尾 == b 链头 → a 后接 b
				if paths[a].End == paths[b].Begin {
					paths[a].End = paths[b].End
					paths[a].Path = paths[a].Path + " → " + paths[b].Path
					paths = append(paths[:b], paths[b+1:]...)
					merged = true
					break
				}
			}
			if merged {
				break
			}
		}
	}

	RespondData(c, "获取成功", gin.H{
		"user":  gin.H{"id": user.ID, "user_name": user.UserName, "port": user.Port, "class": user.Class, "node_group": user.NodeGroup},
		"paths": paths,
	})
}
