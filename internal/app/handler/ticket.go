package handler

import (
	"net/http"
	"strconv"
	"time"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"

	"github.com/gin-gonic/gin"
)

type TicketHandler struct{}

var DefaultTicketHandler = &TicketHandler{}

type CreateTicketRequest struct {
	Title   string `json:"title" binding:"required"`
	Content string `json:"content" binding:"required"`
}

type ReplyTicketRequest struct {
	Content string `json:"content" binding:"required"`
}

// GetTickets 拉取当前用户的父工单列表 (rootid = 0)
func (h *TicketHandler) GetTickets(c *gin.Context) {
	userVal, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"ret": 0, "msg": "未授权访问"})
		return
	}
	user := userVal.(*model.User)

	var tickets []model.Ticket
	if err := database.DB.Where("userid = ? AND rootid = 0", user.ID).Order("datetime DESC").Find(&tickets).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "拉取工单列表失败"})
		return
	}

	formatted := make([]gin.H, 0, len(tickets))
	for _, t := range tickets {
		statusStr := "closed"
		if t.Status == 1 {
			statusStr = "open"
		}
		formatted = append(formatted, gin.H{
			"id":       t.ID,
			"title":    t.Title,
			"status":   statusStr,
			"datetime": time.Unix(t.DateTime, 0).Format("2006-01-02 15:04:05"),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "获取成功",
		"data": formatted,
	})
}

// CreateTicket 提交新的工单 (主工单，rootid = 0)
func (h *TicketHandler) CreateTicket(c *gin.Context) {
	userVal, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"ret": 0, "msg": "未授权访问"})
		return
	}
	user := userVal.(*model.User)

	var req CreateTicketRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误"})
		return
	}

	now := time.Now().Unix()
	ticket := model.Ticket{
		Title:    req.Title,
		Content:  req.Content,
		RootID:   0,
		UserID:   int64(user.ID),
		Sort:     0,
		DateTime: now,
		Status:   1, // 1 为 open (待处理)
	}

	if err := database.DB.Create(&ticket).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "提交工单失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "工单创建成功",
	})
}

// GetTicketDetail 获取主工单详情及全套追问会话流
func (h *TicketHandler) GetTicketDetail(c *gin.Context) {
	userVal, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"ret": 0, "msg": "未授权访问"})
		return
	}
	user := userVal.(*model.User)

	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "工单ID无效"})
		return
	}

	// 1. 查找主工单
	var parent model.Ticket
	if err := database.DB.Where("id = ? AND userid = ? AND rootid = 0", id, user.ID).First(&parent).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"ret": 0, "msg": "工单不存在或已越权"})
		return
	}

	// 2. 查找关联的所有对话 (包括主工单自身以及所有子回复，按 datetime 升序排序)
	var flow []model.Ticket
	if err := database.DB.Where("id = ? OR rootid = ?", id, id).Order("datetime ASC").Find(&flow).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "拉取会话流失败"})
		return
	}

	statusStr := "closed"
	if parent.Status == 1 {
		statusStr = "open"
	}

	chatFlow := make([]gin.H, 0, len(flow))
	for _, item := range flow {
		role := "admin"
		if item.UserID == int64(user.ID) {
			role = "user"
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
			"chat_flow": chatFlow,
		},
	})
}

// ReplyTicket 用户追加工单回复 (插入子工单记录)
func (h *TicketHandler) ReplyTicket(c *gin.Context) {
	userVal, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"ret": 0, "msg": "未授权访问"})
		return
	}
	user := userVal.(*model.User)

	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "工单ID无效"})
		return
	}

	// 1. 查找主工单是否存在且合法，且状态为 open
	var parent model.Ticket
	if err := database.DB.Where("id = ? AND userid = ? AND rootid = 0", id, user.ID).First(&parent).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"ret": 0, "msg": "工单不存在或已越权"})
		return
	}

	if parent.Status != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "该工单已被操作结单锁死，无法回复"})
		return
	}

	var req ReplyTicketRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误"})
		return
	}

	now := time.Now().Unix()
	reply := model.Ticket{
		Title:    "Re: " + parent.Title,
		Content:  req.Content,
		RootID:   parent.ID,
		UserID:   int64(user.ID),
		Sort:     0,
		DateTime: now,
		Status:   1,
	}

	// 在一个事务中写入回复并更新主工单的活跃时间
	tx := database.DB.Begin()
	if err := tx.Create(&reply).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "追加回复失败"})
		return
	}

	if err := tx.Model(&parent).Update("datetime", now).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "更新工单活跃时间失败"})
		return
	}
	tx.Commit()

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "回复提交成功",
	})
}
