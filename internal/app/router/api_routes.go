package router

import (
	"github.com/gin-gonic/gin"

	"spanel-go/internal/app/handler"
	"spanel-go/internal/app/middleware"
)

// RegisterApiRoutes 注册全部 /api/ 开头的对外 API 路由。
//
// 对齐 PHP config/routes.php 的 /api、/api/v2、/api/v2/backend 三组, 共 30 条:
//
//	v0 死路由 x5 (410) / v1 弃用族 x6 (401) / v1 保留 x1 (clonepay) /
//	v2 生命周期族 x10 / v2 治理组 x3 / v2 backend x5
//
// 方案: .plans/2026-11-12_api-routes-all.md
func RegisterApiRoutes(r *gin.Engine) {
	v0 := handler.DefaultAPIv0DeprecatedHandler
	v1 := handler.DefaultAPIv1LegacyHandler

	// ── v0 死路由 (410 + 迁移指引) ──────────────────────────────────────────
	r.GET("/api/token/:token", v0.Token)
	r.POST("/api/token", v0.Token)
	r.GET("/api/node", v0.Node)
	r.GET("/api/user/:id", v0.UserInfo)
	r.GET("/api/sublink", v0.SubLink)

	// ── v1 弃用族 (401 直接拒绝, 2026-11-12 决议不移植) ────────────────────
	r.GET("/api/node/new", v1.NodeNew)
	r.POST("/api/simple_api_tools", v1.SimpleApiTools)
	r.GET("/api/getNodeDomain/:id", v1.GetNodeDomain)
	r.GET("/api/node/proxy/:id", v1.NodeProxy)
	r.POST("/api/ssn_sub/:id", v1.SsnSub)
	r.POST("/api/ssn_v2/:id", v1.SsnV2)

	// ── v1 保留: clonepay 支付同步回调 (IP 白名单 + MD5 签名) ──────────────
	r.POST("/api/clonepay", handler.DefaultClonepayHandler.Callback)

	// ── v2 节点生命周期族 (muKey 三态, status/message 信封) ─────────────────
	lc := handler.DefaultAPIv2LifecycleHandler
	r.GET("/api/nodes", lc.ListNodes)
	r.POST("/api/node/apply_id", lc.ApplyID)
	r.POST("/api/node/register", lc.Register)
	r.POST("/api/node/delete", lc.Delete)
	r.POST("/api/node/resolve_dns", lc.ResolveDNS)
	r.POST("/api/node/config", lc.GetConfig)
	r.POST("/api/node/status", lc.Status)
	r.POST("/api/node/nginx_config", lc.NginxConfig)
	r.POST("/api/node/unlock_check", lc.UnlockCheck)
	r.POST("/api/node/edit", lc.Edit)

	// ── v2 治理组 (muKey, data/error 信封) ─────────────────────────────────
	gov := handler.DefaultAPIv2NodeHandler
	r.GET("/api/v2/user", middleware.MuKeyAuth(middleware.ModeBearerOnly), handler.DefaultAPIv2UserHandler.UserInfo)
	r.POST("/api/v2/node/purge_dns", middleware.MuKeyAuth(middleware.ModeV2ThreeWay), gov.PurgeDNS)
	r.POST("/api/v2/node/offline", middleware.MuKeyAuth(middleware.ModeV2ThreeWay), gov.Offline)

	// ── v2 backend 组 (每节点独立 token) ────────────────────────────────────
	backend := r.Group("/api/v2/backend")
	backend.GET("/healthz", handler.DefaultAPIv2BackendHandler.Healthz) // 无鉴权, nginx/监控探活
	authed := backend.Group("", middleware.BackendToken())
	{
		authed.GET("/users", handler.DefaultAPIv2BackendHandler.Users)
		authed.GET("/users/delta", handler.DefaultAPIv2BackendHandler.UsersDelta)
		authed.POST("/traffic", handler.DefaultAPIv2BackendHandler.Traffic)
		authed.POST("/status", handler.DefaultAPIv2BackendHandler.Status)
	}
}
