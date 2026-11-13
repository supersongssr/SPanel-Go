package router

import (
	"github.com/gin-gonic/gin"

	"spanel-go/internal/app/handler"
	"spanel-go/internal/app/middleware"
)

// RegisterAdminRoutes 注册管理端后端路由：
// 全部位于 /api/v1/admin 之下，经 Auth + Admin 双重中间件保护
func RegisterAdminRoutes(r *gin.Engine) {
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
}
