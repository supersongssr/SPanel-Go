package router

import (
	"github.com/gin-gonic/gin"

	"spanel-go/internal/app/handler"
	"spanel-go/internal/app/middleware"
)

// RegisterUserRoutes 注册用户端全部后端路由
// 方案: .plans/2026-11-16_user端移植方案_静态前端与api-v1.md §4
//
//	/api/v1/auth  7 条 (公开; pow-challenge 已按 §0-7 决策移除)
//	/api/v1/user  52 条 (Auth 中间件: HttpOnly Cookie 优先 + tv 吊销 + Bearer 兜底)
//	/mu /link     节点通信与订阅下发
func RegisterUserRoutes(r *gin.Engine) {
	pub := handler.DefaultPublicHandler
	au := handler.DefaultUserHandler
	tk := handler.DefaultTicketHandler

	// ── 1. 身份验证组 (/api/v1/auth, 公开) ────────────────────────────────
	authGroup := r.Group("/api/v1/auth")
	{
		authGroup.GET("/captcha", pub.Captcha)
		authGroup.POST("/login", pub.Login)
		authGroup.POST("/register", pub.Register)
		authGroup.POST("/send-code", pub.SendCode)
		authGroup.POST("/password/reset", pub.PasswordReset)
		authGroup.POST("/password/token/:token", pub.PasswordToken)
		authGroup.POST("/logout", pub.Logout)
	}

	// ── 2. 用户中心组 (/api/v1/user, Auth 保护) ──────────────────────────
	userGroup := r.Group("/api/v1/user")
	userGroup.Use(middleware.Auth())
	{
		// 只读数据
		userGroup.GET("/profile", au.Profile)
		userGroup.GET("/dashboard", au.Dashboard)
		userGroup.GET("/node", au.Nodes)             // 节点列表
		userGroup.GET("/node/unlock", au.NodeUnlock) // 解锁矩阵
		userGroup.GET("/node/:id/daily", au.NodeDailyTraffic)
		userGroup.GET("/shop", au.ShopList)
		userGroup.GET("/invite", au.GetInvite)
		userGroup.GET("/announcements", au.Announcements)
		userGroup.GET("/announcements/:id", au.Announcements)
		userGroup.GET("/donate", au.DonateList)
		userGroup.GET("/lookingglass", au.LookingGlass)
		userGroup.GET("/cncdn/looking", au.CncdnLooking)
		userGroup.GET("/cfcdn/looking", au.CfcdnLooking)
		userGroup.GET("/detect", au.DetectRules)
		userGroup.GET("/detect/log", au.DetectLogs)
		userGroup.GET("/disable", au.DisableInfo)
		userGroup.GET("/trafficlog", au.TrafficLog)
		userGroup.GET("/code/history", au.CodeHistory)
		userGroup.GET("/code/check", au.CodeCheck)
		userGroup.GET("/bought", au.BoughtList)
		userGroup.GET("/rss", au.RssList)
		userGroup.GET("/iosconf", au.GetIosConf) // attachment 下载 (cookie 直链)

		// edit 页动作
		userGroup.POST("/password", au.UpdateWebPassword)
		userGroup.POST("/sspwd", au.UpdateSSPassword)
		userGroup.POST("/method", au.UpdateSSMethod)
		userGroup.POST("/port/reset", au.ResetPort)
		userGroup.POST("/port/specify", au.SpecifyPort)
		userGroup.POST("/wechat", au.UpdateWechat)
		userGroup.POST("/ssr", au.UpdateSSR)
		userGroup.POST("/theme", au.UpdateTheme)
		userGroup.POST("/group", au.UpdateGroup)
		userGroup.POST("/mail", au.UpdateMail)
		userGroup.POST("/sublimit", au.UpdateSubLimit)
		userGroup.POST("/cncdn", au.UpdateCncdn)
		userGroup.POST("/cfcdn", au.UpdateCfcdn)
		userGroup.POST("/hide", au.UpdateHide)
		userGroup.POST("/pacset", au.PacSet)
		userGroup.POST("/unblock", au.Unblock)

		// 2FA
		userGroup.POST("/ga/check", au.GaCheck)
		userGroup.POST("/ga/set", au.GaEnable)
		userGroup.POST("/ga/disable", au.GaDisable)
		userGroup.POST("/ga/reset", au.GaReset)
		userGroup.POST("/telegram/reset", au.TelegramReset)

		// 账户
		userGroup.POST("/checkin", au.Checkin)
		userGroup.POST("/kill", au.Kill)
		userGroup.POST("/reactive", au.Reactive)
		userGroup.POST("/logout", au.Logout)
		userGroup.POST("/back-to-admin", au.BackToAdmin)

		// 交易
		userGroup.POST("/code/activate", au.CodeActivate)
		userGroup.POST("/coupon/check", au.CouponCheck)
		userGroup.POST("/buy", au.Buy)
		userGroup.POST("/invite/buy", au.InviteBuy)
		userGroup.POST("/invite/custom", au.InviteCustom)
		userGroup.POST("/invite/reset", au.InviteReset)
		userGroup.DELETE("/bought/:id", au.BoughtDelete)
		userGroup.POST("/relevel", au.Relevel)
		userGroup.POST("/vip/apply", au.VipApply)
		userGroup.POST("/cncdn/apply", au.CncdnApply)
		userGroup.POST("/sync-clonepay", au.SyncClonepay)

		// 中转规则 CRUD
		userGroup.GET("/relay", au.RelayList)
		userGroup.POST("/relay", au.RelayAdd)
		userGroup.PUT("/relay/:id", au.RelayUpdate)
		userGroup.DELETE("/relay/:id", au.RelayDelete)

		// 工单
		userGroup.GET("/ticket", tk.GetTickets)
		userGroup.POST("/ticket", tk.CreateTicket)
		userGroup.GET("/ticket/open", tk.OpenTickets) // 公开工单板块
		userGroup.GET("/ticket/open/:id", tk.OpenTicketDetail)
		userGroup.GET("/ticket/:id", tk.GetTicketDetail)
		userGroup.PUT("/ticket/:id", tk.ReplyTicket)
		userGroup.DELETE("/ticket/:id", tk.CloseTicket)

		// 订阅管理 (多 token)
		userGroup.POST("/rss/create", au.RssCreate)
		userGroup.POST("/rss/delete", au.RssDelete)
		userGroup.POST("/rss/reset", au.RssReset)
	}

	// ── 3. 节点通信组 (muKey) ────────────────────────────────────────────
	muGroup := r.Group("/mu")
	muGroup.Use(middleware.NodeAuth())
	{
		muGroup.POST("/users", handler.DefaultNodeHandler.MuUsers)
	}

	// ── 4. 订阅链接 (免 Auth, token 自鉴权; /rss/:token 与 /link 双轨 §4.3) ─
	r.GET("/link/:token", handler.DefaultSubHandler.GetSubscription)
	r.GET("/rss/:token", handler.DefaultSubHandler.GetSubscription)
}
