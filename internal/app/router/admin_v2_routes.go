package router

import (
	"github.com/gin-gonic/gin"

	"spanel-go/internal/app/handler/adminv2"
	"spanel-go/internal/app/middleware"
)

// RegisterAdminV2Routes 管理端 v2 API (/api/v2/admin)
// 方案: .plans/2026-11-18_admin移植方案_样式对齐旧版与api-v2.md
// 与 v1 (/api/v1/admin) 双轨并存 (Owner 决议 R5: v1 暂不下线), 端点按 Phase 逐步补齐。
func RegisterAdminV2Routes(r *gin.Engine) {
	g := r.Group("/api/v2/admin")
	g.Use(middleware.Auth(), middleware.Admin())
	h := adminv2.Default
	{
		// 总览
		g.GET("/dashboard", h.Dashboard)

		// 用户 (Phase 0: 列表; Phase 1: 编辑/删除/切换身份/操作记录)
		g.GET("/user", h.ListUsers)
		g.GET("/user/:id", h.GetUser)
		g.PUT("/user/:id", h.UpdateUser)
		g.DELETE("/user/:id", h.DeleteUser)
		g.POST("/user/:id/masquerade", h.Masquerade)

		// 用户操作审计
		g.GET("/user-log", h.ListUserLogs)

		// 节点
		g.GET("/node", h.ListNodes)
		g.GET("/node/:id", h.GetNode)
		g.POST("/node", h.CreateNode)
		g.PUT("/node/:id", h.UpdateNode)
		g.DELETE("/node/:id", h.DeleteNode)
		g.GET("/node/nodectl", h.NodeCtl)

		// 工单
		g.GET("/ticket", h.ListTickets)
		g.GET("/ticket/:id", h.GetTicket)
		g.PUT("/ticket/:id", h.ReplyTicket)

		// 个人资料
		g.GET("/profile", h.GetProfile)

		// 商品/购买
		g.GET("/shop", h.ListShops)
		g.GET("/shop/:id", h.GetShop)
		g.POST("/shop", h.CreateShop)
		g.PUT("/shop/:id", h.UpdateShop)
		g.DELETE("/shop/:id", h.DeleteShop) // 下架
		g.GET("/bought", h.ListBought)
		g.DELETE("/bought/:id", h.DeleteBought) // 中止续费

		// 充值码/捐赠
		g.GET("/code", h.ListCodes)
		g.POST("/code", h.CreateCodes)
		g.POST("/donate", h.CreateDonate)

		// 优惠码
		g.GET("/coupon", h.ListCoupons)
		g.POST("/coupon", h.CreateCoupon)
		g.DELETE("/coupon/:id", h.DeleteCoupon)

		// 邀请返利
		g.GET("/invite", h.ListInvites)
		g.POST("/invite/add", h.AddInviteQuota)

		// 公告
		g.GET("/announcement", h.ListAnnouncements)
		g.GET("/announcement/:id", h.GetAnnouncement)
		g.POST("/announcement", h.CreateAnnouncement)
		g.PUT("/announcement/:id", h.UpdateAnnouncement)
		g.DELETE("/announcement/:id", h.DeleteAnnouncement)

		// 订阅抓取
		g.GET("/rss-headers", h.ListRssHeaders)
		g.GET("/rss-headers/:id", h.GetRssHeader)
		g.POST("/rss-headers/clear", h.ClearRssHeaders)

		// 中转规则
		g.GET("/relay", h.ListRelays)
		g.GET("/relay/options", h.RelayNodeOptions)
		g.GET("/relay/:id", h.GetRelay)
		g.POST("/relay", h.CreateRelay)
		g.PUT("/relay/:id", h.UpdateRelay)
		g.DELETE("/relay/:id", h.DeleteRelay)
		g.GET("/relay/path-search/:id", h.RelayPathSearch)

		// 审计规则/记录
		g.GET("/detect", h.ListDetectRules)
		g.GET("/detect/:id", h.GetDetectRule)
		g.POST("/detect", h.CreateDetectRule)
		g.PUT("/detect/:id", h.UpdateDetectRule)
		g.DELETE("/detect/:id", h.DeleteDetectRule)
		g.GET("/detect-log", h.ListDetectLogs)

		// 自动命令
		g.GET("/auto", h.ListAutos)
		g.POST("/auto", h.CreateAuto)
		g.DELETE("/auto/:id", h.DeleteAuto)

		// IP 管理
		g.GET("/blockip", h.ListBlockIps)
		g.GET("/unblockip", h.ListUnblockIps)
		g.POST("/unblockip/unblock", h.DoUnblock)

		// 记录: 登录/在线/流量
		g.GET("/audit/login", h.ListAuditLogins)
		g.GET("/audit/alive", h.ListAuditAlive)
		g.GET("/traffic-log", h.ListTrafficLogs)

		// 统计
		g.GET("/stats/recharge", h.RechargeStats)
		g.GET("/stats/node-group-traffic", h.NodeGroupTraffic)
		g.GET("/stats/node-decision", h.NodeDecision)
	}
}
