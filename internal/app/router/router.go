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
