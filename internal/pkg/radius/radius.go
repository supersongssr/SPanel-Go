// Package radius Radius 独立库连接与账号联动
// (对齐 PHP app/Utils/Radius.php 的 Add/Delete/GetUserName)。
//
// enable_radius=false 或未配置 host 时全部旁路 (no-op)。
// username 规范化: email 删除所有 '@' 与 '.' (str_replace 全局)。
package radius

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
)

var (
	mu   sync.Mutex
	conn *gorm.DB
)

// GetUserName email → radius username (删 @ 与 .)
func GetUserName(email string) string {
	email = strings.ReplaceAll(email, "@", "")
	return strings.ReplaceAll(email, ".", "")
}

// RadiusNas freeradius nas 表 (对齐 PHP RadiusNas model; AddNas/DelNas 用)
type RadiusNas struct {
	NasName     string `gorm:"column:nasname;primaryKey"`
	ShortName   string `gorm:"column:shortname"`
	Type        string `gorm:"column:type"`
	Secret      string `gorm:"column:secret"`
	Description string `gorm:"column:description"`
}

func (RadiusNas) TableName() string { return "nas" }

// AddNas 注册 VPN/Radius 节点 NAS (对齐 PHP Radius::AddNas: 存在即跳过; 未启用时 no-op)
func AddNas(ip, name string) {
	db := DB()
	if db == nil {
		return
	}
	var count int64
	db.Model(&RadiusNas{}).Where("shortname = ?", ip).Count(&count)
	if count > 0 {
		return
	}
	nas := RadiusNas{NasName: ip, ShortName: ip, Type: "other", Secret: "", Description: ip}
	if err := db.Create(&nas).Error; err != nil {
		log.Printf("[Radius] AddNas failed %s: %v", ip, err)
	}
}

// DelNas 删除 NAS 记录 (对齐 PHP Radius::DelNas; 未启用时 no-op)
func DelNas(ip string) {
	db := DB()
	if db == nil {
		return
	}
	if err := db.Where("shortname = ?", ip).Delete(&RadiusNas{}).Error; err != nil {
		log.Printf("[Radius] DelNas failed %s: %v", ip, err)
	}
}

// Enabled 总开关: enable_radius=true 且 host 已配置
func Enabled() bool {
	cfg := config.GlobalConfig
	return cfg != nil && cfg.Radius.Enable && cfg.Radius.Host != ""
}

// DB 惰性连接 Radius 二库 (失败返回 nil, 调用方旁路)
func DB() *gorm.DB {
	if !Enabled() {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	if conn != nil {
		return conn
	}
	cfg := config.GlobalConfig.Radius
	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		cfg.User, cfg.Password, cfg.Host, cfg.Database)
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		log.Printf("[Radius] connect failed: %v", err)
		return nil
	}
	conn = db
	return conn
}

// radcheck / radusergroup 最小结构
type radcheck struct {
	Username  string `gorm:"column:username"`
	Attribute string `gorm:"column:attribute"`
	Op        string `gorm:"column:op"`
	Value     string `gorm:"column:value"`
}

func (radcheck) TableName() string { return "radcheck" }

type radusergroup struct {
	Username  string `gorm:"column:username"`
	Groupname string `gorm:"column:groupname"`
	Priority  string `gorm:"column:priority"`
}

func (radusergroup) TableName() string { return "radusergroup" }

// Add 对齐 PHP Radius::Add: 不存在则建 (radcheck Cleartext-Password + radusergroup user),
// 已封禁 (radius_ban 有记录) 不重建; 存在则仅更新密码。
func Add(ctx context.Context, userID int32, email, passwd string) {
	db := DB()
	if db == nil {
		return
	}
	name := GetUserName(email)

	var exists radcheck
	err := db.WithContext(ctx).Where("username = ?", name).First(&exists).Error
	if err != nil {
		// 不存在 → 检查封禁后创建
		var banCount int64
		if database.DB != nil {
			database.DB.WithContext(ctx).Model(&model.RadiusBan{}).
				Where("userid = ?", userID).Count(&banCount)
		}
		if banCount > 0 {
			return // 已被封禁, 不重建
		}
		_ = db.WithContext(ctx).Create(&radcheck{
			Username: name, Attribute: "Cleartext-Password", Op: ":=", Value: passwd,
		}).Error
		_ = db.WithContext(ctx).Create(&radusergroup{
			Username: name, Groupname: "user", Priority: "0",
		}).Error
		return
	}
	// 已存在 → 仅更新密码
	_ = db.WithContext(ctx).Model(&radcheck{}).
		Where("username = ?", name).
		Update("value", passwd).Error
}

// ChangeUserName 对齐 PHP Radius::ChangeUserName: 改邮箱时同步改名 radcheck/radusergroup;
// 不存在 (从未建号) 则按 Add 语义补建 (含封禁检查); 存在则改名 + 更新密码。
func ChangeUserName(ctx context.Context, userID int32, originEmail, newEmail, passwd string) {
	db := DB()
	if db == nil {
		return
	}
	name1 := GetUserName(originEmail)
	name2 := GetUserName(newEmail)
	if name1 == name2 {
		return
	}
	var exists radcheck
	err := db.WithContext(ctx).Where("username = ?", name1).First(&exists).Error
	if err == nil {
		_ = db.WithContext(ctx).Model(&radcheck{}).
			Where("username = ?", name1).
			Updates(map[string]interface{}{"username": name2, "value": passwd}).Error
		_ = db.WithContext(ctx).Model(&radusergroup{}).
			Where("username = ?", name1).
			Update("username", name2).Error
		return
	}
	// 原名不存在 → 补建 (与 Add 一致, 含封禁检查)
	Add(ctx, userID, newEmail, passwd)
}

// Delete 对齐 PHP Radius::Delete: radcheck 存在才删 (radcheck + radusergroup)。
func Delete(ctx context.Context, email string) {
	db := DB()
	if db == nil {
		return
	}
	name := GetUserName(email)
	var exists radcheck
	if err := db.WithContext(ctx).Where("username = ?", name).First(&exists).Error; err != nil {
		return // 不存在 → 不动 (无孤儿清理)
	}
	_ = db.WithContext(ctx).Where("username = ?", name).Delete(&radcheck{}).Error
	_ = db.WithContext(ctx).Where("username = ?", name).Delete(&radusergroup{}).Error
}

// Close 释放连接 (进程退出)
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if conn != nil {
		if sqlDB, err := conn.DB(); err == nil {
			_ = sqlDB.Close()
		}
		conn = nil
	}
}
