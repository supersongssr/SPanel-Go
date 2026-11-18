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

// SetupRouter 组装全功能路由引擎：
// 用户端 (auth/user/mu/link) + 管理端 (admin) + /api/* 对外 API + 全部静态页面
func SetupRouter() *gin.Engine {
	r := NewBaseRouter()
	RegisterUserRoutes(r)
	RegisterUserStatic(r)
	RegisterAdminRoutes(r)
	RegisterAdminStatic(r)
	RegisterAdminV2Routes(r)
	RegisterApiRoutes(r)
	return r
}
