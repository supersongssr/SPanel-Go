package handler

import (
	"net/http"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"

	"github.com/gin-gonic/gin"
)

type UserHandler struct{}

var DefaultUserHandler = &UserHandler{}

// Profile 获取当前用户的详细资料与状态数据
func (h *UserHandler) Profile(c *gin.Context) {
	userVal, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"ret": 0,
			"msg": "未授权的访问",
		})
		return
	}

	user := userVal.(*model.User)

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "获取成功",
		"data": gin.H{
			"id":              user.ID,
			"user_name":       user.UserName,
			"email":           user.Email,
			"port":            user.Port,
			"money":           user.Money,
			"class":           user.Class,
			"class_expire":    user.ClassExpire.Format("2006-01-02 15:04:05"),
			"expire_in":       user.ExpireIn.Format("2006-01-02 15:04:05"),
			"node_group":      user.NodeGroup,
			"method":          user.Method,
			"u":               user.U,
			"d":               user.D,
			"transfer_enable": user.TransferEnable,
			"unused_traffic":  user.TransferEnable - (user.U + user.D),
		},
	})
}

// Nodes 获取当前用户已授权的所有可用代理节点列表
func (h *UserHandler) Nodes(c *gin.Context) {
	userVal, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"ret": 0,
			"msg": "未授权的访问",
		})
		return
	}

	user := userVal.(*model.User)

	// 1. 校验数据库连接
	if database.DB == nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"ret": 0,
			"msg": "系统维护中：数据库连接异常，请稍后重试",
		})
		return
	}

	var nodes []model.Node
	query := database.DB.Where("type = 1") // 仅拉取可见节点

	// 如果是非管理员，执行严格的等级与群组鉴权过滤
	if user.IsAdmin != 1 {
		query = query.Where("(node_group = ? OR node_group = 0)", user.NodeGroup).
			Where("node_class <= ?", user.Class)
	}

	if err := query.Order("sort ASC").Find(&nodes).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"ret": 0,
			"msg": "获取节点列表失败，请稍后重试",
		})
		return
	}

	// 格式化输出列表，剔除敏感后端属性
	formattedNodes := make([]gin.H, 0, len(nodes))
	for _, n := range nodes {
		formattedNodes = append(formattedNodes, gin.H{
			"id":             n.ID,
			"name":           n.Name,
			"server":         n.Server,
			"method":         n.Method,
			"info":           n.Info,
			"status":         n.Status,
			"traffic_rate":   n.TrafficRate,
			"node_class":     n.NodeClass,
			"node_bandwidth": n.NodeBandwidth,
			"node_group":     n.NodeGroup,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret":  1,
		"msg":  "获取成功",
		"data": formattedNodes,
	})
}

// Dashboard 用户仪表盘数据看板 Stub
func (h *UserHandler) Dashboard(c *gin.Context) {
	userVal, _ := c.Get("user")
	user := userVal.(*model.User)

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "获取成功",
		"data": gin.H{
			"announcement": gin.H{
				"content": "欢迎使用全新 SPanel-Go 高性能极速面板！系统测试环境部署成功。",
				"date":    "2026-05-25",
			},
			"user": gin.H{
				"id":              user.ID,
				"user_name":       user.UserName,
				"email":           user.Email,
				"money":           user.Money,
				"class":           user.Class,
				"port":            user.Port,
				"method":          user.Method,
				"passwd":          user.Passwd,
				"v2ray_uuid":      user.V2rayUUID,
				"u":               user.U,
				"d":               user.D,
				"transfer_enable": user.TransferEnable,
			},
		},
	})
}

// Checkin 用户签到接口 Stub
func (h *UserHandler) Checkin(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "签到成功！获取测试环境随机奖励 1024 MB 流量",
	})
}

