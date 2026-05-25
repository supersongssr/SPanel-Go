package handler

import (
	"net/http"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/service"

	"github.com/gin-gonic/gin"
)

type SubHandler struct{}

var DefaultSubHandler = &SubHandler{}

// GetSubscription 统一订阅生成网关入口 (免 Auth 中间件，直接查表做高吞吐处理)
func (h *SubHandler) GetSubscription(c *gin.Context) {
	token := c.Param("token")
	if token == "" {
		c.String(http.StatusBadRequest, "invalid subscription token")
		return
	}

	// 1. 校验数据库连接
	if database.DB == nil {
		c.String(http.StatusInternalServerError, "system maintenance: database is offline")
		return
	}

	// 2. 查找 token 映射记录
	var link model.Link
	if err := database.DB.Where("token = ?", token).First(&link).Error; err != nil {
		c.String(http.StatusNotFound, "subscription link expired or invalid")
		return
	}

	// 2. 查找关联的用户状态
	var user model.User
	if err := database.DB.First(&user, link.UserID).Error; err != nil {
		c.String(http.StatusNotFound, "associated user account not found")
		return
	}

	// 3. 校验用户是否可用
	if user.Enable != 1 {
		c.String(http.StatusForbidden, "account has been suspended")
		return
	}

	// 4. 拉取该用户有权连接的节点列表
	var nodes []model.Node
	query := database.DB.Where("type = 1")
	if user.IsAdmin != 1 {
		query = query.Where("(node_group = ? OR node_group = 0)", user.NodeGroup)
	}

	if err := query.Order("sort ASC").Find(&nodes).Error; err != nil {
		c.String(http.StatusInternalServerError, "failed to compile node list")
		return
	}

	// 5. 调用订阅生成服务输出配置文件 (默认格式化为 Clash 适配的 YAML)
	clashYaml := service.DefaultSubscriptionService.BuildClashConfig(&user, nodes)

	// 6. 返回纯文本配置文件，配合标准订阅响应头以防缓存
	c.Header("Content-Type", "text/yaml; charset=utf-8")
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
	c.String(http.StatusOK, clashYaml)
}
