package database

import (
	"context"
	"fmt"
	"time"

	"spanel-go/internal/config"

	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DB 全局 MySQL GORM 句柄
var DB *gorm.DB

// Redis 全局 Redis 客户端句柄
var Redis *redis.Client

// InitDatabase 初始化 MySQL (GORM) 连接池
func InitDatabase(cfg *config.DatabaseConfig) error {
	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?charset=%s&parseTime=True&loc=Local",
		cfg.Username,
		cfg.Password,
		cfg.Host,
		cfg.Database,
		cfg.Charset,
	)

	// 使用默认 Logger，生产环境可调整等级
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info),
	})
	if err != nil {
		return fmt.Errorf("failed to open database connection: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("failed to get sql.DB: %w", err)
	}

	// 调优连接池参数
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetimeMin) * time.Minute)

	// 连通性 Ping 校验
	if err := sqlDB.Ping(); err != nil {
		return fmt.Errorf("database ping fail: %w", err)
	}

	DB = db
	return nil
}

// InitRedis 初始化 Redis 连接
func InitRedis(cfg *config.RedisConfig) error {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Host,
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	// 测试连通性
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("failed to connect to redis: %w", err)
	}

	Redis = rdb
	return nil
}

// CloseDatabase 安全释放连接池资源
func CloseDatabase() {
	if DB != nil {
		if sqlDB, err := DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}
	if Redis != nil {
		_ = Redis.Close()
	}
}
