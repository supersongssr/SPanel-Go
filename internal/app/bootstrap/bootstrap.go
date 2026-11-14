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
	"spanel-go/internal/jobs"
	"spanel-go/internal/pkg/database"
)

// Options 服务启动参数
type Options struct {
	Port   int         // 监听端口；0 表示读取 config.yaml 的 app.port
	Router *gin.Engine // 路由引擎
}

// Run 启动 SPanel-Go 服务 (serve 模式):
// 加载配置 → 初始化 MySQL/Redis → (可选) 内嵌任务调度器 → HTTP 服务 → 优雅退出。
// DB/Redis 初始化失败降级为警告不阻断启动 (调度器随之禁用)。
func Run(opt Options) {
	log.Println("starting SPanel-Go...")

	// 1. 加载系统 YAML 配置文件
	cfg, ok := loadConfigOrDie()
	if !ok {
		return
	}

	// 2. 解析监听端口 (-port 参数 > config.yaml app.port)
	port := opt.Port
	if port == 0 {
		port = cfg.App.Port
	}
	if port == 0 {
		log.Fatalf("no listening port: set -port flag or app.port in config.yaml")
	}

	// 3. 初始化 MySQL GORM 连接池与 Redis 缓存 (连接失败降级为警告，不阻断启动)
	dbOK := true
	if err := database.InitDatabase(&cfg.Database); err != nil {
		dbOK = false
		log.Printf("[WARNING] MySQL database connection failed: %v", err)
		log.Println("[WARNING] Please check config.yaml mysql credentials.")
	} else {
		log.Println("MySQL GORM database pool initialized successfully.")
	}

	redisOK := true
	if err := database.InitRedis(&cfg.Redis); err != nil {
		redisOK = false
		log.Printf("[WARNING] Redis connection failed: %v", err)
		log.Println("[WARNING] Please check config.yaml redis settings.")
	} else {
		log.Println("Redis client initialized successfully.")
	}

	defer database.CloseDatabase()

	// 4. 内嵌后台任务调度器 (jobs.enabled 且 DB/Redis 就绪; 方案 2026-11-13 §3)
	startScheduler(cfg, dbOK, redisOK)

	// 5. SIGHUP 热加载任务配置 (改 config.yaml jobs 段后 kill -HUP 即生效, 无需重启)
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	go func() {
		for range sighup {
			log.Println("[jobs] SIGHUP: reloading jobs config...")
			if newCfg, err := config.LoadConfig("config.yaml"); err != nil {
				log.Printf("[jobs] reload config failed, keep old: %v", err)
				continue
			} else {
				jobs.StopScheduler(1 * time.Minute)
				startScheduler(newCfg, true, true)
			}
		}
	}()

	// 6. HTTP 服务与优雅停止
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

	// 先停调度器 (不再触发新任务, 等待在跑任务收尾, 上限 5 分钟), 再关 HTTP
	jobs.StopScheduler(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("server forced shutdown: %v", err)
	}

	log.Println("server exited.")
}

// InitCoreCLI CLI 子命令 (job/admin) 初始化: 加载配置 + 初始化 DB/Redis。
// 与 serve 不同: 任一失败直接 Fatal —— 单跑任务无库无锁无意义。
// 返回配置; database.CloseDatabase() 由调用方在退出前执行。
func InitCoreCLI(cfgPath string) *config.Config {
	log.Println("starting SPanel-Go (cli)...")

	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		log.Fatalf("config file '%s' not found, please check path", cfgPath)
	}
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		log.Fatalf("failed to load system configurations: %v", err)
	}
	log.Printf("configuration loaded successfully. App: %s", cfg.App.Name)

	if err := database.InitDatabase(&cfg.Database); err != nil {
		log.Fatalf("MySQL database connection failed: %v", err)
	}
	log.Println("MySQL GORM database pool initialized successfully.")

	if err := database.InitRedis(&cfg.Redis); err != nil {
		log.Fatalf("Redis connection failed: %v", err)
	}
	log.Println("Redis client initialized successfully.")

	return cfg
}

func loadConfigOrDie() (*config.Config, bool) {
	cfgPath := "config.yaml"
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		log.Fatalf("config file '%s' not found, please check path", cfgPath)
	}
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		log.Fatalf("failed to load system configurations: %v", err)
	}
	log.Printf("configuration loaded successfully. App: %s", cfg.App.Name)
	return cfg, true
}

// startScheduler 启动调度器 (DB/Redis 就绪检查内联)
func startScheduler(cfg *config.Config, dbOK, redisOK bool) {
	if cfg.Jobs.Enabled {
		if dbOK && redisOK {
			jobs.StartScheduler(cfg)
		} else {
			log.Println("[WARNING] jobs.enabled=true but DB/Redis not ready, scheduler disabled.")
		}
	}
}
