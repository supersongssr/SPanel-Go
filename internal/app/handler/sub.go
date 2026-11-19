package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"spanel-go/internal/config"
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
		// 订阅请求抓取先于禁用判定 (PHP: 禁用账号的请求也记录, 便于排查"订阅为什么不通")
		captureRssRequest(c, &user, token)
		c.String(http.StatusForbidden, "account has been suspended")
		return
	}

	// 4. 拉取该用户有权连接的节点列表
	var nodes []model.Node
	query := database.DB.Where("type = 1")
	if user.IsAdmin != 1 {
		query = query.Where("(node_group = ? OR node_group = 0)", user.NodeGroup)
	}

	// 3.5 订阅请求抓取 (rss_header_capture.enabled 开关, 对齐 PHP RssHeaderCapture;
	// 异步写入不阻断订阅链路, 任何失败仅记日志)
	captureRssRequest(c, &user, token)

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

// captureRssRequest 订阅请求抓取埋点 (rss_request_log 表; PHP RssHeaderCapture::capture 等价)。
// enabled=false 或 DB 异常时静默跳过, 绝不影响订阅响应。
func captureRssRequest(c *gin.Context, user *model.User, token string) {
	cfg := config.GlobalConfig
	if cfg == nil || !cfg.RssHeaderCapture.Enabled || database.DB == nil {
		return
	}
	q := c.Request.URL.Query()
	queryJSON, _ := json.Marshal(q)
	headers := map[string]string{}
	for k := range c.Request.Header {
		headers[k] = c.Request.Header.Get(k)
	}
	headersJSON, _ := json.Marshal(headers)

	// 客户端归属 (PHP resolveApp: ?app= → App 头 → X-App 头 → unknown; flag 为旧客户端兼容)
	app := q.Get("app")
	if app == "" {
		app = c.GetHeader("App")
	}
	if app == "" {
		app = c.GetHeader("X-App")
	}
	if app == "" {
		app = q.Get("flag")
	}
	if app == "" {
		app = "unknown"
	}

	// uri 带 query (PHP buildRow: path?query, 与旧列表展示一致)
	uri := c.Request.URL.Path
	if c.Request.URL.RawQuery != "" {
		uri += "?" + c.Request.URL.RawQuery
	}

	row := model.RssRequestLog{
		UserID: int64(user.ID), Email: user.Email, Token: token,
		App: app, IP: c.ClientIP(),
		Method: c.Request.Method, URI: uri,
		Query: string(queryJSON), UserAgent: c.Request.UserAgent(),
		Headers: string(headersJSON), CreatedAt: time.Now(),
	}
	go func() {
		defer func() { _ = recover() }()
		if err := database.DB.Create(&row).Error; err != nil {
			log.Printf("[RssCapture] write failed user=%d: %v", user.ID, err)
		}
	}()
}
