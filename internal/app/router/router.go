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

	// 3. 身份验证组 (Public)
	publicGroup := r.Group("/api/v1/public")
	{
		publicGroup.GET("/pow_challenge", handler.DefaultPublicHandler.PowChallenge)
		publicGroup.POST("/login", handler.DefaultPublicHandler.Login)
	}

	// 4. 用户中心组 (User Protected)
	userGroup := r.Group("/api/v1/user")
	userGroup.Use(middleware.Auth())
	{
		userGroup.GET("/profile", handler.DefaultUserHandler.Profile)
		userGroup.GET("/nodes", handler.DefaultUserHandler.Nodes)
	}

	// 5. 节点通信组 (Node/Mu API Protected)
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

	// 6. 订阅链接路由 (高性能网关，免 Auth 中间件，独立在 Handler 校验 Token)
	r.GET("/link/:token", handler.DefaultSubHandler.GetSubscription)

	return r
}
