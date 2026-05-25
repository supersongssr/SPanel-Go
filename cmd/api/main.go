package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"spanel-go/internal/config"
	"spanel-go/internal/app/router"
	"spanel-go/internal/pkg/database"
)

func main() {
	log.Println("Starting SPanel-Go initial backend server...")

	// 1. 加载系统 YAML 配置文件
	cfgPath := "config.yaml"
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		log.Fatalf("config file '%s' not found, please check path", cfgPath)
	}

	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		log.Fatalf("failed to load system configurations: %v", err)
	}
	log.Printf("configuration loaded successfully. App: %s, Port: %d", cfg.App.Name, cfg.App.Port)

	// 2. 初始化 MySQL GORM 连接池与 Redis 缓存 (由于是初版骨架，连接报错降级为警告不阻断主线程运行)
	if err := database.InitDatabase(&cfg.Database); err != nil {
		log.Printf("[WARNING] MySQL database connection failed: %v", err)
		log.Println("[WARNING] Please check config.yaml mysql credentials.")
	} else {
		log.Println("MySQL GORM database pool initialized successfully.")
	}

	if err := database.InitRedis(&cfg.Redis); err != nil {
		log.Printf("[WARNING] Redis connection failed: %v", err)
		log.Println("[WARNING] Please check config.yaml redis settings.")
	} else {
		log.Println("Redis client initialized successfully.")
	}

	defer database.CloseDatabase()

	// 3. 配置与装配 Gin 路由引擎
	r := router.SetupRouter()

	// 4. 实现优雅停止服务 (Graceful Shutdown)
	serverAddr := fmt.Sprintf(":%d", cfg.App.Port)
	srv := &http.Server{
		Addr:    serverAddr,
		Handler: r,
	}

	go func() {
		log.Printf("HTTP gateway serving on port %d...", cfg.App.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("gateway server listen error: %v", err)
		}
	}()

	// 监听中断与退出信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down SPanel-Go backend gateway gracefully...")
	database.CloseDatabase()
	log.Println("SPanel-Go server exited successfully.")
}
