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

		// 用户 (Phase 0: 列表先行; CRUD/编辑随 Phase 1 平移)
		g.GET("/user", h.ListUsers)
	}
}
