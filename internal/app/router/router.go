package router

import (
	"github.com/gin-gonic/gin"

	"spanel-go/internal/app/middleware"
)

// NewBaseRouter 创建基础路由引擎：注册全局中间件与健康检查等公共路由
func NewBaseRouter() *gin.Engine {
	r := gin.New()

	// 1. 注册核心中间件
	r.Use(gin.Recovery())
	r.Use(middleware.Cors())

	// 2. 基础公共路由
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{"msg": "pong"})
	})

	return r
}

// SetupUserRouter 用户端实例 (minimal)：公网最小暴露面，仅包含用户运行所需功能
// 组成：公共基础 + 用户路由组 (auth / user / 节点通信 / 订阅) + 用户端静态页面
func SetupUserRouter() *gin.Engine {
	r := NewBaseRouter()
	RegisterUserRoutes(r)
	RegisterUserStatic(r)
	return r
}

// SetupRouter 全功能实例 (full)：包含用户端全部功能 + 管理端路由与静态页面
// 建议仅部署在内网或网关 (nginx) 白名单之后，不直接暴露公网
// 注: /api/* 对外 API (节点/支付回调) 亦仅在本实例注册 (后端公网可达由网关层保障)
func SetupRouter() *gin.Engine {
	r := NewBaseRouter()
	RegisterUserRoutes(r)
	RegisterUserStatic(r)
	RegisterAdminRoutes(r)
	RegisterAdminStatic(r)
	RegisterApiRoutes(r)
	return r
}
