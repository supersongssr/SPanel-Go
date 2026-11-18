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

		// 工单
		g.GET("/ticket", h.ListTickets)
		g.GET("/ticket/:id", h.GetTicket)
		g.PUT("/ticket/:id", h.ReplyTicket)

		// 个人资料
		g.GET("/profile", h.GetProfile)
	}
}
