package router

import (
	"spanel-go/internal/app/handler"
	"spanel-go/internal/app/middleware"

	"github.com/gin-gonic/gin"
)

// SetupRouter 初始化 Gin 路由引擎，配置路由组与拦截中间件
func SetupRouter() *gin.Engine {
	r := gin.New()

	// 1. 注册核心中间件
	r.Use(gin.Recovery())
	r.Use(middleware.Cors())

	// 2. 基础公共路由
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{"msg": "pong"})
	})

	// 3. 身份验证组 (Public / Auth)
	authGroup := r.Group("/api/v1/auth")
	{
		authGroup.GET("/captcha", handler.DefaultPublicHandler.Captcha)
		authGroup.POST("/pow-challenge", handler.DefaultPublicHandler.PowChallenge)
		authGroup.POST("/login", handler.DefaultPublicHandler.Login)
		authGroup.POST("/register", handler.DefaultPublicHandler.Register)
		authGroup.POST("/send-code", handler.DefaultPublicHandler.SendCode)
	}

	// 4. 用户中心组 (User Protected)
	userGroup := r.Group("/api/v1/user")
	userGroup.Use(middleware.Auth())
	{
		userGroup.GET("/profile", handler.DefaultUserHandler.Profile)
		userGroup.GET("/node", handler.DefaultUserHandler.Nodes) // 对应 frontend /user/node
		userGroup.GET("/dashboard", handler.DefaultUserHandler.Dashboard) // 对应 frontend /user/dashboard
		userGroup.POST("/checkin", handler.DefaultUserHandler.Checkin) // 对应 frontend /user/checkin

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

	// 5. 管理员中心组 (Admin Protected)
	adminGroup := r.Group("/api/v1/admin")
	adminGroup.Use(middleware.Auth(), middleware.Admin())
	{
		// 仪表盘
		adminGroup.GET("/dashboard", handler.DefaultAdminHandler.Dashboard)

		// 用户管理
		adminGroup.GET("/user", handler.DefaultAdminHandler.ListUsers)
		adminGroup.GET("/user/:id", handler.DefaultAdminHandler.GetUser)
		adminGroup.POST("/user", handler.DefaultAdminHandler.CreateUser)
		adminGroup.PUT("/user/:id", handler.DefaultAdminHandler.UpdateUser)
		adminGroup.DELETE("/user/:id", handler.DefaultAdminHandler.DeleteUser)
		adminGroup.POST("/user/:id/masquerade", handler.DefaultAdminHandler.Masquerade)

		// 节点管理
		adminGroup.GET("/node", handler.DefaultAdminHandler.ListNodes)
		adminGroup.GET("/node/:id", handler.DefaultAdminHandler.GetNode)
		adminGroup.POST("/node", handler.DefaultAdminHandler.CreateNode)
		adminGroup.PUT("/node/:id", handler.DefaultAdminHandler.UpdateNode)
		adminGroup.DELETE("/node/:id", handler.DefaultAdminHandler.DeleteNode)

		// 工单管理
		adminGroup.GET("/ticket", handler.DefaultAdminHandler.AdminListTickets)
		adminGroup.GET("/ticket/:id", handler.DefaultAdminHandler.AdminGetTicket)
		adminGroup.PUT("/ticket/:id", handler.DefaultAdminHandler.AdminReplyTicket)
		adminGroup.DELETE("/ticket/:id", handler.DefaultAdminHandler.AdminCloseTicket)

		// 公告管理
		adminGroup.GET("/announcement", handler.DefaultAdminHandler.ListAnnouncements)
		adminGroup.POST("/announcement", handler.DefaultAdminHandler.CreateAnnouncement)
		adminGroup.PUT("/announcement/:id", handler.DefaultAdminHandler.UpdateAnnouncement)
		adminGroup.DELETE("/announcement/:id", handler.DefaultAdminHandler.DeleteAnnouncement)

		// 商品管理
		adminGroup.GET("/shop", handler.DefaultAdminHandler.ListShops)
		adminGroup.GET("/shop/:id", handler.DefaultAdminHandler.GetShop)
		adminGroup.POST("/shop", handler.DefaultAdminHandler.CreateShop)
		adminGroup.PUT("/shop/:id", handler.DefaultAdminHandler.UpdateShop)
		adminGroup.DELETE("/shop/:id", handler.DefaultAdminHandler.DeleteShop)

		// 购买记录
		adminGroup.GET("/bought", handler.DefaultAdminHandler.ListBought)

		// 优惠券管理
		adminGroup.GET("/coupon", handler.DefaultAdminHandler.ListCoupons)
		adminGroup.POST("/coupon", handler.DefaultAdminHandler.CreateCoupon)
		adminGroup.DELETE("/coupon/:id", handler.DefaultAdminHandler.DeleteCoupon)

		// 充值码管理
		adminGroup.GET("/code", handler.DefaultAdminHandler.ListCodes)
		adminGroup.POST("/code", handler.DefaultAdminHandler.BatchCreateCodes)

		// 邀请返利管理
		adminGroup.GET("/invite", handler.DefaultAdminHandler.ListInvites)
		adminGroup.POST("/invite/add", handler.DefaultAdminHandler.AddInviteQuota)

		// 审计日志
		adminGroup.GET("/audit/login", handler.DefaultAdminHandler.AuditLogin)
		adminGroup.GET("/audit/alive", handler.DefaultAdminHandler.AuditAlive)
		adminGroup.GET("/audit/traffic", handler.DefaultAdminHandler.AuditTraffic)
	}

	// 6. 节点通信组 (Node/Mu API Protected)
	nodeGroup := r.Group("/api/node")
	nodeGroup.Use(middleware.NodeAuth())
	{
		nodeGroup.POST("/status", handler.DefaultNodeHandler.Status)
	}

	muGroup := r.Group("/mu")
	muGroup.Use(middleware.NodeAuth())
	{
		muGroup.POST("/users", handler.DefaultNodeHandler.MuUsers)
	}

	// 7. 订阅链接路由 (高性能网关，免 Auth 中间件，独立在 Handler 校验 Token)
	r.GET("/link/:token", handler.DefaultSubHandler.GetSubscription)

	return r
}
