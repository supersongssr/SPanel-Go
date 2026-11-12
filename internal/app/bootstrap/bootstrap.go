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

// Options 单个服务实例的启动参数
type Options struct {
	Instance string      // 实例名称 (user / full)，用于日志标识
	Port     int         // 监听端口；0 表示按实例名从 config.yaml 自动解析
	Router   *gin.Engine // 路由引擎
}

// Run 启动一个 SPanel-Go 服务实例：
// 加载配置 → 初始化 MySQL/Redis → HTTP 服务 → 优雅退出 (Graceful Shutdown)
func Run(opt Options) {
	log.Printf("[%s] starting SPanel-Go instance...", opt.Instance)

	// 1. 加载系统 YAML 配置文件
	cfgPath := "config.yaml"
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		log.Fatalf("[%s] config file '%s' not found, please check path", opt.Instance, cfgPath)
	}

	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		log.Fatalf("[%s] failed to load system configurations: %v", opt.Instance, err)
	}
	log.Printf("[%s] configuration loaded successfully. App: %s", opt.Instance, cfg.App.Name)

	// 2. 解析监听端口 (user 实例回落 port，full 实例回落 port+1)
	port := opt.Port
	if port == 0 {
		switch opt.Instance {
		case "user":
			port = cfg.App.UserPort
			if port == 0 {
				port = cfg.App.Port
			}
		case "full":
			port = cfg.App.FullPort
			if port == 0 {
				port = cfg.App.Port + 1
			}
		default:
			log.Fatalf("[%s] unknown instance name", opt.Instance)
		}
	}

	// 3. 初始化 MySQL GORM 连接池与 Redis 缓存 (连接失败降级为警告，不阻断启动)
	if err := database.InitDatabase(&cfg.Database); err != nil {
		log.Printf("[%s][WARNING] MySQL database connection failed: %v", opt.Instance, err)
		log.Printf("[%s][WARNING] Please check config.yaml mysql credentials.", opt.Instance)
	} else {
		log.Printf("[%s] MySQL GORM database pool initialized successfully.", opt.Instance)
	}

	if err := database.InitRedis(&cfg.Redis); err != nil {
		log.Printf("[%s][WARNING] Redis connection failed: %v", opt.Instance, err)
		log.Printf("[%s][WARNING] Please check config.yaml redis settings.", opt.Instance)
	} else {
		log.Printf("[%s] Redis client initialized successfully.", opt.Instance)
	}

	defer database.CloseDatabase()

	// 4. HTTP 服务与优雅停止
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: opt.Router,
	}

	go func() {
		log.Printf("[%s] HTTP gateway serving on port %d...", opt.Instance, port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[%s] listen error: %v", opt.Instance, err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Printf("[%s] shutting down server...", opt.Instance)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("[%s] server forced to shutdown: %v", opt.Instance, err)
	}

	log.Printf("[%s] server exited.", opt.Instance)
}
