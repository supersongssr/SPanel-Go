package router

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"spanel-go/internal/app/handler"
	"spanel-go/internal/app/middleware"
)

// RegisterUserRoutes 注册用户端全部后端路由：
// 身份验证 (auth)、用户中心 (user)、节点通信 (node/mu)、订阅分发 (link)
func RegisterUserRoutes(r *gin.Engine) {
	// 1. 身份验证组 (Public / Auth)
	authGroup := r.Group("/api/v1/auth")
	{
		authGroup.GET("/captcha", handler.DefaultPublicHandler.Captcha)
		authGroup.POST("/pow-challenge", handler.DefaultPublicHandler.PowChallenge)
		authGroup.POST("/login", handler.DefaultPublicHandler.Login)
		authGroup.POST("/register", handler.DefaultPublicHandler.Register)
		authGroup.POST("/send-code", handler.DefaultPublicHandler.SendCode)
	}

	// 2. 用户中心组 (User Protected)
	userGroup := r.Group("/api/v1/user")
	userGroup.Use(middleware.Auth())
	{
		userGroup.GET("/profile", handler.DefaultUserHandler.Profile)
		userGroup.GET("/node", handler.DefaultUserHandler.Nodes)          // 对应 frontend /user/node
		userGroup.GET("/dashboard", handler.DefaultUserHandler.Dashboard) // 对应 frontend /user/dashboard
		userGroup.POST("/checkin", handler.DefaultUserHandler.Checkin)    // 对应 frontend /user/checkin
		userGroup.GET("/invite", handler.DefaultUserHandler.GetInvite)    // 对应 frontend /user/invite

		// 工单系统路由挂载
		userGroup.GET("/ticket", handler.DefaultTicketHandler.GetTickets)
		userGroup.POST("/ticket", handler.DefaultTicketHandler.CreateTicket)
		userGroup.GET("/ticket/:id", handler.DefaultTicketHandler.GetTicketDetail)
		userGroup.PUT("/ticket/:id", handler.DefaultTicketHandler.ReplyTicket)

		// 个人及连接安全设置路由挂载
		userGroup.POST("/port/reset", handler.DefaultUserHandler.ResetPort)
		userGroup.POST("/sspwd", handler.DefaultUserHandler.UpdateSSPassword)
		userGroup.POST("/method", handler.DefaultUserHandler.UpdateSSMethod)
		userGroup.POST("/password", handler.DefaultUserHandler.UpdateWebPassword)
		userGroup.POST("/ga/set", handler.DefaultUserHandler.Enable2FA)
		userGroup.POST("/ga/reset", handler.DefaultUserHandler.Disable2FA)
	}

	// 3. 节点通信组 (Mu API Protected)
	// 注: 旧版简化 /api/node/status 桩已删除 —— PHP 版完整端口
	// (V2ApiController::status, muKey 三态鉴权 + reportStatus 全量语义)
	// 由 RegisterApiRoutes 注册, 见 internal/app/router/api_routes.go
	muGroup := r.Group("/mu")
	muGroup.Use(middleware.NodeAuth())
	{
		muGroup.POST("/users", handler.DefaultNodeHandler.MuUsers)
	}

	// 4. 订阅链接路由 (高性能网关，免 Auth 中间件，独立在 Handler 校验 Token)
	r.GET("/link/:token", handler.DefaultSubHandler.GetSubscription)
}

// RegisterUserStatic 挂载用户端前端静态页面 (public/user, public/auth, public/assets)
func RegisterUserStatic(r *gin.Engine) {
	mountStatic(r, "user", "auth", "assets")
	registerCommonPages(r)
}

// RegisterAdminStatic 挂载管理端前端静态页面 (public/admin)
// 注：根页面与 404 兜底页已由 RegisterUserStatic 统一注册
func RegisterAdminStatic(r *gin.Engine) {
	mountStatic(r, "admin")
}

// mountStatic 将 public/<name> 目录挂载至 /<name>/* 路径
func mountStatic(r *gin.Engine, names ...string) {
	for _, n := range names {
		r.Static("/"+n, filepath.Join("public", n))
	}
}

// registerCommonPages 注册根页面与 404 兜底页 (全局仅注册一次，由 RegisterUserStatic 调用)
func registerCommonPages(r *gin.Engine) {
	r.StaticFile("/", "public/index.html")
	r.StaticFile("/index.html", "public/index.html")
	r.StaticFile("/404.html", "public/404.html")

	// API 路径返回 JSON 404，其余路径返回 404 页面 (HTTP 状态码为 404)
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api") || strings.HasPrefix(p, "/mu") || strings.HasPrefix(p, "/link") {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "msg": "not found"})
			return
		}
		body, err := os.ReadFile("public/404.html")
		if err != nil {
			c.String(http.StatusNotFound, "404 page not found")
			return
		}
		c.Data(http.StatusNotFound, "text/html; charset=utf-8", body)
	})
}
