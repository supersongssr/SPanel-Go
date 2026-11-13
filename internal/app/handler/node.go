package handler

import (
	"net/http"
	"strconv"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"

	"github.com/gin-gonic/gin"
)

type NodeHandler struct{}

var DefaultNodeHandler = &NodeHandler{}

// MuUsers 代理后端拉取有权限使用此节点的活跃用户列表接口
func (h *NodeHandler) MuUsers(c *gin.Context) {
	nodeIDStr := c.Query("node_id")
	nodeID, err := strconv.Atoi(nodeIDStr)
	if err != nil || nodeID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"ret": 0,
			"msg": "invalid node_id",
		})
		return
	}

	// 1. 校验数据库连接
	if database.DB == nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"ret": 0,
			"msg": "system maintenance: database is offline",
		})
		return
	}

	// 2. 获取节点限制以进行用户权限筛选
	var node model.Node
	if err := database.DB.First(&node, nodeID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"ret": 0,
			"msg": "node not found",
		})
		return
	}

	// 2. 筛选有权连接此节点的活跃用户
	// 条件：enable=1 且 (user.node_group == node.node_group 或 node.node_group == 0)
	var users []model.User
	query := database.DB.Where("enable = 1")

	if node.NodeGroup != 0 {
		query = query.Where("node_group = ?", node.NodeGroup)
	}

	if err := query.Find(&users).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"ret": 0,
			"msg": "database query fail",
		})
		return
	}

	// 3. 构造输出给代理后端需要的格式
	type UserInfo struct {
		ID       int32  `json:"id"`
		Port     int32  `json:"port"`
		Password string `json:"passwd"` // 连接密码
		Method   string `json:"method"`
		UUID     string `json:"v2ray_uuid"`
	}

	resUsers := make([]UserInfo, 0, len(users))
	for _, u := range users {
		resUsers = append(resUsers, UserInfo{
			ID:       u.ID,
			Port:     u.Port,
			Password: u.Passwd,
			Method:   u.Method,
			UUID:     u.V2rayUUID,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret":  1,
		"msg":  "success",
		"data": resUsers,
	})
}
