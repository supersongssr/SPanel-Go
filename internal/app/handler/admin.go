package handler

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"time"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

type AdminHandler struct{}

var DefaultAdminHandler = &AdminHandler{}

// ==================== 1. Dashboard ====================

// Dashboard 管理员仪表盘统计数据
func (h *AdminHandler) Dashboard(c *gin.Context) {
	var totalUsers int64
	database.DB.Model(&model.User{}).Count(&totalUsers)

	var activeUsers int64
	database.DB.Model(&model.User{}).Where("enable = 1").Count(&activeUsers)

	var totalNodes int64
	database.DB.Model(&model.Node{}).Count(&totalNodes)

	// 总收入: SUM(price) from bought
	var totalRevenue float64
	database.DB.Model(&model.Bought{}).Select("COALESCE(SUM(price), 0)").Scan(&totalRevenue)

	// 总流量: SUM(u + d) from user
	var totalTrafficBytes int64
	database.DB.Model(&model.User{}).Select("COALESCE(SUM(u + d), 0)").Scan(&totalTrafficBytes)

	// 7天用户增长曲线
	type DayCount struct {
		Day   string `json:"day"`
		Count int64  `json:"count"`
	}
	userGrowth := make([]DayCount, 7)
	for i := 6; i >= 0; i-- {
		day := time.Now().AddDate(0, 0, -i)
		dayStr := day.Format("2006-01-02")
		dayStart := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
		dayEnd := dayStart.AddDate(0, 0, 1)

		var count int64
		database.DB.Model(&model.User{}).
			Where("reg_date >= ? AND reg_date < ?", dayStart, dayEnd).
			Count(&count)

		userGrowth[6-i] = DayCount{
			Day:   dayStr,
			Count: count,
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "获取成功",
		"data": gin.H{
			"total_users":        totalUsers,
			"active_users":       activeUsers,
			"total_nodes":        totalNodes,
			"total_revenue":      totalRevenue,
			"total_traffic_bytes": totalTrafficBytes,
			"user_growth":        userGrowth,
		},
	})
}

// ==================== 2. Users ====================

// ListUsers 用户列表 (支持搜索)
func (h *AdminHandler) ListUsers(c *gin.Context) {
	search := c.Query("search")

	var users []model.User
	query := database.DB.Model(&model.User{})
	if search != "" {
		like := "%" + search + "%"
		query = query.Where("user_name LIKE ? OR email LIKE ? OR id = ?", like, like, search)
	}

	if err := query.Order("id DESC").Find(&users).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "查询用户列表失败"})
		return
	}

	list := make([]gin.H, 0, len(users))
	for _, u := range users {
		list = append(list, gin.H{
			"id":              u.ID,
			"user_name":       u.UserName,
			"email":           u.Email,
			"money":           u.Money,
			"class":           u.Class,
			"class_expire":    u.ClassExpire.Format("2006-01-02 15:04:05"),
			"expire_in":       u.ExpireIn.Format("2006-01-02 15:04:05"),
			"node_group":      u.NodeGroup,
			"is_admin":        u.IsAdmin,
			"enable":          u.Enable,
			"u":               u.U,
			"d":               u.D,
			"transfer_enable": u.TransferEnable,
			"reg_date":        u.RegDate.Format("2006-01-02 15:04:05"),
			"ref_by":          u.RefBy,
			"invite_num":      u.InviteNum,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret":  1,
		"msg":  "获取成功",
		"data": list,
	})
}

// GetUser 获取单个用户详情
func (h *AdminHandler) GetUser(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "用户ID无效"})
		return
	}

	var user model.User
	if err := database.DB.First(&user, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"ret": 0, "msg": "用户不存在"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "获取成功",
		"data": gin.H{
			"id":              user.ID,
			"user_name":       user.UserName,
			"email":           user.Email,
			"money":           user.Money,
			"port":            user.Port,
			"passwd":          user.Passwd,
			"method":          user.Method,
			"class":           user.Class,
			"class_expire":    user.ClassExpire.Format("2006-01-02 15:04:05"),
			"expire_in":       user.ExpireIn.Format("2006-01-02 15:04:05"),
			"node_group":      user.NodeGroup,
			"is_admin":        user.IsAdmin,
			"enable":          user.Enable,
			"u":               user.U,
			"d":               user.D,
			"transfer_enable": user.TransferEnable,
			"reg_date":        user.RegDate.Format("2006-01-02 15:04:05"),
			"ref_by":          user.RefBy,
			"invite_num":      user.InviteNum,
			"node_speedlimit": user.NodeSpeedlimit,
			"node_connector":  user.NodeConnector,
		},
	})
}

// CreateUser 创建新用户
func (h *AdminHandler) CreateUser(c *gin.Context) {
	var req struct {
		UserName       string `json:"user_name" binding:"required"`
		Email          string `json:"email" binding:"required"`
		Password       string `json:"password" binding:"required"`
		Money          string `json:"money"`
		TransferEnable int64  `json:"transfer_enable"`
		Class          int32  `json:"class"`
		NodeGroup      int32  `json:"node_group"`
		IsAdmin        int32  `json:"is_admin"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误: " + err.Error()})
		return
	}

	// 检查邮箱唯一
	var count int64
	database.DB.Model(&model.User{}).Where("email = ?", req.Email).Count(&count)
	if count > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "邮箱已被注册"})
		return
	}

	// 密码哈希
	salt := config.GlobalConfig.App.Salt
	passHash := service.DefaultAuthService.SHA256Hash(req.Password, salt)

	money := req.Money
	if money == "" {
		money = "0.00"
	}

	user := model.User{
		UserName:       req.UserName,
		Email:          req.Email,
		Pass:           passHash,
		Passwd:         generateAdminRandomStr(8),
		Money:          money,
		TransferEnable: req.TransferEnable,
		Class:          req.Class,
		NodeGroup:      req.NodeGroup,
		IsAdmin:        req.IsAdmin,
		Enable:         1,
		Method:         "aes-256-cfb",
		RegDate:        time.Now(),
		ExpireIn:       time.Now().AddDate(1, 0, 0),
		ClassExpire:    time.Now().AddDate(1, 0, 0),
	}

	if err := database.DB.Create(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "创建用户失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "用户创建成功", "data": gin.H{"id": user.ID}})
}

// UpdateUser 更新用户信息
func (h *AdminHandler) UpdateUser(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "用户ID无效"})
		return
	}

	var user model.User
	if err := database.DB.First(&user, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"ret": 0, "msg": "用户不存在"})
		return
	}

	var req map[string]interface{}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误"})
		return
	}

	// 如果含 password，则转换为哈希
	if pwd, ok := req["password"]; ok {
		if pwdStr, ok := pwd.(string); ok && pwdStr != "" {
			salt := config.GlobalConfig.App.Salt
			req["pass"] = service.DefaultAuthService.SHA256Hash(pwdStr, salt)
		}
		delete(req, "password")
	}

	if err := database.DB.Model(&user).Updates(req).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "更新用户失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "用户更新成功"})
}

// DeleteUser 删除用户
func (h *AdminHandler) DeleteUser(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "用户ID无效"})
		return
	}

	if err := database.DB.Delete(&model.User{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "删除用户失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "用户已删除"})
}

// Masquerade 生成冒充用户的 JWT Token
func (h *AdminHandler) Masquerade(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "用户ID无效"})
		return
	}

	var user model.User
	if err := database.DB.First(&user, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"ret": 0, "msg": "目标用户不存在"})
		return
	}

	// 使用与 auth service 相同方式生成 JWT
	expireTime := time.Now().Add(time.Duration(config.GlobalConfig.JWT.ExpireHours) * time.Hour)
	claims := jwt.MapClaims{
		"user_id":   user.ID,
		"email":     user.Email,
		"is_admin":  user.IsAdmin,
		"exp":       expireTime.Unix(),
		"issued_at": time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(config.GlobalConfig.JWT.Secret))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "生成Token失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "伪装Token生成成功",
		"data": gin.H{
			"token":     tokenString,
			"user_name": user.UserName,
			"email":     user.Email,
		},
	})
}

// ==================== 3. Nodes ====================

// ListNodes 节点列表
func (h *AdminHandler) ListNodes(c *gin.Context) {
	var nodes []model.Node
	if err := database.DB.Order("sort ASC, id ASC").Find(&nodes).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "查询节点列表失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ret":  1,
		"msg":  "获取成功",
		"data": nodes,
	})
}

// GetNode 获取单个节点
func (h *AdminHandler) GetNode(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "节点ID无效"})
		return
	}

	var node model.Node
	if err := database.DB.First(&node, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"ret": 0, "msg": "节点不存在"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ret":  1,
		"msg":  "获取成功",
		"data": node,
	})
}

// CreateNode 创建节点 (ss_node 表无 node_class 列)
func (h *AdminHandler) CreateNode(c *gin.Context) {
	var node model.Node
	if err := c.ShouldBindJSON(&node); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误: " + err.Error()})
		return
	}

	if err := database.DB.Create(&node).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "创建节点失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "节点创建成功", "data": gin.H{"id": node.ID}})
}

// UpdateNode 更新节点
func (h *AdminHandler) UpdateNode(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "节点ID无效"})
		return
	}

	var node model.Node
	if err := database.DB.First(&node, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"ret": 0, "msg": "节点不存在"})
		return
	}

	var req map[string]interface{}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误"})
		return
	}

	// 确保不会误写 node_class
	delete(req, "node_class")

	if err := database.DB.Model(&node).Updates(req).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "更新节点失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "节点更新成功"})
}

// DeleteNode 删除节点
func (h *AdminHandler) DeleteNode(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "节点ID无效"})
		return
	}

	if err := database.DB.Delete(&model.Node{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "删除节点失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "节点已删除"})
}

// ==================== 4. Tickets ====================

// AdminListTickets 管理员查看所有工单 (包含用户邮箱)
func (h *AdminHandler) AdminListTickets(c *gin.Context) {
	var tickets []model.Ticket
	if err := database.DB.Where("rootid = 0").Order("datetime DESC").Find(&tickets).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "查询工单列表失败"})
		return
	}

	// 收集用户 ID 并批量查询邮箱
	userIDs := make([]int64, 0, len(tickets))
	for _, t := range tickets {
		userIDs = append(userIDs, t.UserID)
	}

	emailMap := make(map[int64]string)
	if len(userIDs) > 0 {
		var users []model.User
		database.DB.Where("id IN ?", userIDs).Find(&users)
		for _, u := range users {
			emailMap[int64(u.ID)] = u.Email
		}
	}

	list := make([]gin.H, 0, len(tickets))
	for _, t := range tickets {
		statusStr := "closed"
		if t.Status == 1 {
			statusStr = "open"
		}
		list = append(list, gin.H{
			"id":       t.ID,
			"title":    t.Title,
			"status":   statusStr,
			"userid":   t.UserID,
			"email":    emailMap[t.UserID],
			"datetime": time.Unix(t.DateTime, 0).Format("2006-01-02 15:04:05"),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret":  1,
		"msg":  "获取成功",
		"data": list,
	})
}

// AdminGetTicket 管理员查看工单详情
func (h *AdminHandler) AdminGetTicket(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "工单ID无效"})
		return
	}

	var parent model.Ticket
	if err := database.DB.Where("id = ? AND rootid = 0", id).First(&parent).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"ret": 0, "msg": "工单不存在"})
		return
	}

	var flow []model.Ticket
	if err := database.DB.Where("id = ? OR rootid = ?", id, id).Order("datetime ASC").Find(&flow).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "拉取会话流失败"})
		return
	}

	statusStr := "closed"
	if parent.Status == 1 {
		statusStr = "open"
	}

	// 查询工单发起人信息
	var ticketUser model.User
	database.DB.First(&ticketUser, parent.UserID)

	chatFlow := make([]gin.H, 0, len(flow))
	for _, item := range flow {
		role := "user"
		if item.UserID != parent.UserID {
			role = "admin"
		}
		chatFlow = append(chatFlow, gin.H{
			"id":       item.ID,
			"role":     role,
			"datetime": time.Unix(item.DateTime, 0).Format("2006-01-02 15:04:05"),
			"content":  item.Content,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "获取成功",
		"data": gin.H{
			"id":        parent.ID,
			"title":     parent.Title,
			"status":    statusStr,
			"userid":    parent.UserID,
			"email":     ticketUser.Email,
			"chat_flow": chatFlow,
		},
	})
}

// AdminReplyTicket 管理员回复工单
func (h *AdminHandler) AdminReplyTicket(c *gin.Context) {
	adminVal, _ := c.Get("user")
	admin := adminVal.(*model.User)

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "工单ID无效"})
		return
	}

	var parent model.Ticket
	if err := database.DB.Where("id = ? AND rootid = 0", id).First(&parent).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"ret": 0, "msg": "工单不存在"})
		return
	}

	var req struct {
		Content string `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误"})
		return
	}

	now := time.Now().Unix()
	reply := model.Ticket{
		Title:    "Re: " + parent.Title,
		Content:  req.Content,
		RootID:   parent.ID,
		UserID:   int64(admin.ID),
		Sort:     0,
		DateTime: now,
		Status:   1,
	}

	tx := database.DB.Begin()
	if err := tx.Create(&reply).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "回复失败"})
		return
	}
	// 更新主工单时间和状态为 open
	if err := tx.Model(&parent).Updates(map[string]interface{}{
		"datetime": now,
		"status":   1,
	}).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "更新工单状态失败"})
		return
	}
	tx.Commit()

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "回复成功"})
}

// AdminCloseTicket 管理员关闭工单 (status=0)
func (h *AdminHandler) AdminCloseTicket(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "工单ID无效"})
		return
	}

	if err := database.DB.Model(&model.Ticket{}).Where("id = ? AND rootid = 0", id).Update("status", 0).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "关闭工单失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "工单已关闭"})
}

// ==================== 5. Announcements ====================

// ListAnnouncements 公告列表
func (h *AdminHandler) ListAnnouncements(c *gin.Context) {
	var list []model.Announcement
	if err := database.DB.Order("id DESC").Find(&list).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "查询公告列表失败"})
		return
	}

	formatted := make([]gin.H, 0, len(list))
	for _, a := range list {
		formatted = append(formatted, gin.H{
			"id":       a.ID,
			"date":     a.Date.Format("2006-01-02 15:04:05"),
			"content":  a.Content,
			"markdown": a.Markdown,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret":  1,
		"msg":  "获取成功",
		"data": formatted,
	})
}

// CreateAnnouncement 创建公告
func (h *AdminHandler) CreateAnnouncement(c *gin.Context) {
	var req struct {
		Content  string `json:"content"`
		Markdown string `json:"markdown"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误"})
		return
	}

	ann := model.Announcement{
		Date:     time.Now(),
		Content:  req.Content,
		Markdown: req.Markdown,
	}

	if err := database.DB.Create(&ann).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "创建公告失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "公告创建成功", "data": gin.H{"id": ann.ID}})
}

// UpdateAnnouncement 更新公告
func (h *AdminHandler) UpdateAnnouncement(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "公告ID无效"})
		return
	}

	var ann model.Announcement
	if err := database.DB.First(&ann, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"ret": 0, "msg": "公告不存在"})
		return
	}

	var req struct {
		Content  string `json:"content"`
		Markdown string `json:"markdown"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误"})
		return
	}

	updates := map[string]interface{}{
		"content":  req.Content,
		"markdown": req.Markdown,
		"date":     time.Now(),
	}

	if err := database.DB.Model(&ann).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "更新公告失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "公告更新成功"})
}

// DeleteAnnouncement 删除公告
func (h *AdminHandler) DeleteAnnouncement(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "公告ID无效"})
		return
	}

	if err := database.DB.Delete(&model.Announcement{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "删除公告失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "公告已删除"})
}

// ==================== 6. Shops ====================

// ListShops 商品列表
func (h *AdminHandler) ListShops(c *gin.Context) {
	var shops []model.Shop
	if err := database.DB.Order("id ASC").Find(&shops).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "查询商品列表失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ret":  1,
		"msg":  "获取成功",
		"data": shops,
	})
}

// GetShop 获取单个商品
func (h *AdminHandler) GetShop(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "商品ID无效"})
		return
	}

	var shop model.Shop
	if err := database.DB.First(&shop, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"ret": 0, "msg": "商品不存在"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ret":  1,
		"msg":  "获取成功",
		"data": shop,
	})
}

// CreateShop 创建商品
func (h *AdminHandler) CreateShop(c *gin.Context) {
	var shop model.Shop
	if err := c.ShouldBindJSON(&shop); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误: " + err.Error()})
		return
	}

	if err := database.DB.Create(&shop).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "创建商品失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "商品创建成功", "data": gin.H{"id": shop.ID}})
}

// UpdateShop 更新商品
func (h *AdminHandler) UpdateShop(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "商品ID无效"})
		return
	}

	var shop model.Shop
	if err := database.DB.First(&shop, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"ret": 0, "msg": "商品不存在"})
		return
	}

	var req map[string]interface{}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误"})
		return
	}

	if err := database.DB.Model(&shop).Updates(req).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "更新商品失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "商品更新成功"})
}

// DeleteShop 删除商品
func (h *AdminHandler) DeleteShop(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "商品ID无效"})
		return
	}

	if err := database.DB.Delete(&model.Shop{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "删除商品失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "商品已删除"})
}

// ==================== 7. Bought ====================

// ListBought 购买记录列表
func (h *AdminHandler) ListBought(c *gin.Context) {
	var records []model.Bought
	if err := database.DB.Order("id DESC").Find(&records).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "查询购买记录失败"})
		return
	}

	// 批量查询用户和商品信息
	userIDs := make([]int64, 0, len(records))
	shopIDs := make([]int64, 0, len(records))
	for _, r := range records {
		userIDs = append(userIDs, r.UserID)
		shopIDs = append(shopIDs, r.ShopID)
	}

	userMap := make(map[int64]string)
	if len(userIDs) > 0 {
		var users []model.User
		database.DB.Where("id IN ?", userIDs).Find(&users)
		for _, u := range users {
			userMap[int64(u.ID)] = u.Email
		}
	}

	shopMap := make(map[int64]string)
	if len(shopIDs) > 0 {
		var shops []model.Shop
		database.DB.Where("id IN ?", shopIDs).Find(&shops)
		for _, s := range shops {
			shopMap[s.ID] = s.Name
		}
	}

	list := make([]gin.H, 0, len(records))
	for _, r := range records {
		list = append(list, gin.H{
			"id":        r.ID,
			"userid":    r.UserID,
			"email":     userMap[r.UserID],
			"shopid":    r.ShopID,
			"shop_name": shopMap[r.ShopID],
			"datetime":  time.Unix(r.DateTime, 0).Format("2006-01-02 15:04:05"),
			"renew":     r.Renew,
			"coupon":    r.Coupon,
			"price":     r.Price,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret":  1,
		"msg":  "获取成功",
		"data": list,
	})
}

// ==================== 8. Coupons ====================

// ListCoupons 优惠券列表
func (h *AdminHandler) ListCoupons(c *gin.Context) {
	var coupons []model.Coupon
	if err := database.DB.Order("id DESC").Find(&coupons).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "查询优惠券列表失败"})
		return
	}

	list := make([]gin.H, 0, len(coupons))
	for _, cp := range coupons {
		expireStr := ""
		if cp.Expire > 0 {
			expireStr = time.Unix(cp.Expire, 0).Format("2006-01-02 15:04:05")
		}
		list = append(list, gin.H{
			"id":      cp.ID,
			"code":    cp.Code,
			"onetime": cp.Onetime,
			"expire":  expireStr,
			"shop":    cp.Shop,
			"credit":  cp.Credit,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret":  1,
		"msg":  "获取成功",
		"data": list,
	})
}

// CreateCoupon 创建优惠券
func (h *AdminHandler) CreateCoupon(c *gin.Context) {
	var req struct {
		Code    string `json:"code"`
		Onetime int32  `json:"onetime"`
		Expire  int64  `json:"expire"`
		Shop    string `json:"shop"`
		Credit  int32  `json:"credit"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误"})
		return
	}

	code := req.Code
	if code == "" {
		code = generateAdminRandomStr(12)
	}

	coupon := model.Coupon{
		Code:    code,
		Onetime: req.Onetime,
		Expire:  req.Expire,
		Shop:    req.Shop,
		Credit:  req.Credit,
	}

	if err := database.DB.Create(&coupon).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "创建优惠券失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "优惠券创建成功", "data": gin.H{"id": coupon.ID, "code": coupon.Code}})
}

// DeleteCoupon 删除优惠券
func (h *AdminHandler) DeleteCoupon(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "优惠券ID无效"})
		return
	}

	if err := database.DB.Delete(&model.Coupon{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "删除优惠券失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "优惠券已删除"})
}

// ==================== 9. Codes ====================

// ListCodes 激活码列表
func (h *AdminHandler) ListCodes(c *gin.Context) {
	var codes []model.Code
	if err := database.DB.Order("id DESC").Find(&codes).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "查询激活码列表失败"})
		return
	}

	list := make([]gin.H, 0, len(codes))
	for _, cd := range codes {
		list = append(list, gin.H{
			"id":          cd.ID,
			"code":        cd.Code,
			"type":        cd.Type,
			"number":      cd.Number,
			"isused":      cd.IsUsed,
			"userid":      cd.UserID,
			"usedatetime": cd.UseDatetime.Format("2006-01-02 15:04:05"),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret":  1,
		"msg":  "获取成功",
		"data": list,
	})
}

// BatchCreateCodes 批量生成激活码
func (h *AdminHandler) BatchCreateCodes(c *gin.Context) {
	var req struct {
		Type   int32  `json:"type"`
		Amount string `json:"amount"`
		Count  int    `json:"count"`
		Prefix string `json:"prefix"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误"})
		return
	}

	if req.Count <= 0 || req.Count > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "批量数量范围: 1-100"})
		return
	}

	if req.Amount == "" {
		req.Amount = "0.00"
	}

	codes := make([]model.Code, 0, req.Count)
	codeStrs := make([]string, 0, req.Count)
	for i := 0; i < req.Count; i++ {
		codeStr := req.Prefix + generateAdminRandomStr(16)
		codes = append(codes, model.Code{
			Code:   codeStr,
			Type:   req.Type,
			Number: req.Amount,
			IsUsed: 0,
		})
		codeStrs = append(codeStrs, codeStr)
	}

	if err := database.DB.Create(&codes).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "批量创建激活码失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": fmt.Sprintf("成功生成 %d 个激活码", req.Count),
		"data": gin.H{
			"codes": codeStrs,
		},
	})
}

// ==================== 10. Invites ====================

// ListInvites 邀请统计和返利列表
func (h *AdminHandler) ListInvites(c *gin.Context) {
	// 邀请码列表
	var inviteCodes []model.InviteCode
	database.DB.Order("id DESC").Find(&inviteCodes)

	// 返利记录
	var paybacks []model.Payback
	database.DB.Order("id DESC").Find(&paybacks)

	// 查询关联用户
	userIDs := make([]int64, 0)
	for _, p := range paybacks {
		userIDs = append(userIDs, p.UserID, p.RefBy)
	}
	for _, ic := range inviteCodes {
		userIDs = append(userIDs, int64(ic.UserID))
	}

	userMap := make(map[int64]string)
	if len(userIDs) > 0 {
		var users []model.User
		database.DB.Where("id IN ?", userIDs).Find(&users)
		for _, u := range users {
			userMap[int64(u.ID)] = u.Email
		}
	}

	inviteList := make([]gin.H, 0, len(inviteCodes))
	for _, ic := range inviteCodes {
		inviteList = append(inviteList, gin.H{
			"id":         ic.ID,
			"code":       ic.Code,
			"user_id":    ic.UserID,
			"user_email": userMap[int64(ic.UserID)],
			"created_at": ic.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}

	paybackList := make([]gin.H, 0, len(paybacks))
	for _, p := range paybacks {
		paybackList = append(paybackList, gin.H{
			"id":         p.ID,
			"total":      p.Total,
			"userid":     p.UserID,
			"user_email": userMap[p.UserID],
			"ref_by":     p.RefBy,
			"ref_email":  userMap[p.RefBy],
			"ref_get":    p.RefGet,
			"datetime":   time.Unix(p.DateTime, 0).Format("2006-01-02 15:04:05"),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "获取成功",
		"data": gin.H{
			"invites":  inviteList,
			"paybacks": paybackList,
		},
	})
}

// AddInviteQuota 给用户增加邀请名额
func (h *AdminHandler) AddInviteQuota(c *gin.Context) {
	var req struct {
		UserSearch string `json:"user_search" binding:"required"`
		Num        int32  `json:"num"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误"})
		return
	}

	if req.Num <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "邀请名额数量必须大于0"})
		return
	}

	// 支持通过 email 或 ID 查找用户
	var user model.User
	query := database.DB.Where("email = ? OR id = ?", req.UserSearch, req.UserSearch)
	if err := query.First(&user).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"ret": 0, "msg": "用户不存在"})
		return
	}

	newNum := user.InviteNum + req.Num
	if err := database.DB.Model(&user).Update("invite_num", newNum).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "更新邀请名额失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": fmt.Sprintf("成功为 %s 增加 %d 个邀请名额，当前总计 %d", user.Email, req.Num, newNum),
	})
}

// ==================== 11. Audits ====================

// AuditLogin 登录审计日志
func (h *AdminHandler) AuditLogin(c *gin.Context) {
	var logs []model.LoginIP
	if err := database.DB.Order("id DESC").Limit(500).Find(&logs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "查询登录日志失败"})
		return
	}

	userIDs := make([]int64, 0, len(logs))
	for _, l := range logs {
		userIDs = append(userIDs, l.UserID)
	}

	userMap := make(map[int64]string)
	if len(userIDs) > 0 {
		var users []model.User
		database.DB.Where("id IN ?", userIDs).Find(&users)
		for _, u := range users {
			userMap[int64(u.ID)] = u.Email
		}
	}

	list := make([]gin.H, 0, len(logs))
	for _, l := range logs {
		list = append(list, gin.H{
			"id":       l.ID,
			"userid":   l.UserID,
			"email":    userMap[l.UserID],
			"ip":       l.IP,
			"datetime": time.Unix(l.DateTime, 0).Format("2006-01-02 15:04:05"),
			"type":     l.Type,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret":  1,
		"msg":  "获取成功",
		"data": list,
	})
}

// AuditAlive 活跃连接审计
func (h *AdminHandler) AuditAlive(c *gin.Context) {
	var logs []model.AliveIP
	if err := database.DB.Order("id DESC").Limit(500).Find(&logs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "查询活跃连接失败"})
		return
	}

	userIDs := make([]int32, 0, len(logs))
	for _, l := range logs {
		userIDs = append(userIDs, l.UserID)
	}

	userMap := make(map[int32]string)
	if len(userIDs) > 0 {
		var users []model.User
		database.DB.Where("id IN ?", userIDs).Find(&users)
		for _, u := range users {
			userMap[u.ID] = u.Email
		}
	}

	list := make([]gin.H, 0, len(logs))
	for _, l := range logs {
		list = append(list, gin.H{
			"id":       l.ID,
			"nodeid":   l.NodeID,
			"userid":   l.UserID,
			"email":    userMap[l.UserID],
			"ip":       l.IP,
			"datetime": time.Unix(l.DateTime, 0).Format("2006-01-02 15:04:05"),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret":  1,
		"msg":  "获取成功",
		"data": list,
	})
}

// AuditTraffic 流量审计日志
func (h *AdminHandler) AuditTraffic(c *gin.Context) {
	var logs []model.UserTrafficLog
	if err := database.DB.Order("id DESC").Limit(500).Find(&logs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "查询流量日志失败"})
		return
	}

	userIDs := make([]int32, 0, len(logs))
	for _, l := range logs {
		userIDs = append(userIDs, l.UserID)
	}

	userMap := make(map[int32]string)
	if len(userIDs) > 0 {
		var users []model.User
		database.DB.Where("id IN ?", userIDs).Find(&users)
		for _, u := range users {
			userMap[u.ID] = u.Email
		}
	}

	list := make([]gin.H, 0, len(logs))
	for _, l := range logs {
		list = append(list, gin.H{
			"id":       l.ID,
			"user_id":  l.UserID,
			"email":    userMap[l.UserID],
			"u":        l.U,
			"d":        l.D,
			"node_id":  l.NodeID,
			"rate":     l.Rate,
			"traffic":  l.Traffic,
			"log_time": time.Unix(int64(l.LogTime), 0).Format("2006-01-02 15:04:05"),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret":  1,
		"msg":  "获取成功",
		"data": list,
	})
}

// ==================== 辅助函数 ====================

// generateAdminRandomStr 生成强随机字符串 (admin 包内使用)
func generateAdminRandomStr(length int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	for i := range b {
		num, err := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		if err != nil {
			b[i] = 'a'
		} else {
			b[i] = letters[num.Int64()]
		}
	}
	return string(b)
}
