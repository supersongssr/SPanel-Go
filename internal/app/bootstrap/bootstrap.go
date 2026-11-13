package bootstrap

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"spanel-go/internal/config"
	"spanel-go/internal/pkg/database"
)

// Options 服务启动参数
type Options struct {
	Port   int         // 监听端口；0 表示读取 config.yaml 的 app.port
	Router *gin.Engine // 路由引擎
}

// Run 启动 SPanel-Go 服务：
// 加载配置 → 初始化 MySQL/Redis → HTTP 服务 → 优雅退出 (Graceful Shutdown)
func Run(opt Options) {
	log.Println("starting SPanel-Go...")

	// 1. 加载系统 YAML 配置文件
	cfgPath := "config.yaml"
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		log.Fatalf("config file '%s' not found, please check path", cfgPath)
	}

	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		log.Fatalf("failed to load system configurations: %v", err)
	}
	log.Printf("configuration loaded successfully. App: %s", cfg.App.Name)

	// 2. 解析监听端口 (-port 参数 > config.yaml app.port)
	port := opt.Port
	if port == 0 {
		port = cfg.App.Port
	}
	if port == 0 {
		log.Fatalf("no listening port: set -port flag or app.port in config.yaml")
	}

	// 3. 初始化 MySQL GORM 连接池与 Redis 缓存 (连接失败降级为警告，不阻断启动)
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

	// 4. HTTP 服务与优雅停止
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: opt.Router,
	}

	go func() {
		log.Printf("HTTP gateway serving on port %d...", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("server forced to shutdown: %v", err)
	}

	log.Println("server exited.")
}
