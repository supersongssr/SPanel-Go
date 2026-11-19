package model

import "time"

// RssRequestLog 订阅请求抓取 (MySQL 化 PHP RssHeaderCapture 的 SQLite 表;
// 由 /link/:token 订阅处理器按 rss_header_capture.enabled 开关写入, admin 后台查询)
type RssRequestLog struct {
	ID        int64     `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	UserID    int64     `gorm:"column:user_id" json:"user_id"`
	Email     string    `gorm:"column:email" json:"email"`
	Token     string    `gorm:"column:token" json:"token"`
	App       string    `gorm:"column:app" json:"app"` // 订阅客户端标识 (flag 参数)
	IP        string    `gorm:"column:ip" json:"ip"`
	Method    string    `gorm:"column:method" json:"method"`
	URI       string    `gorm:"column:uri" json:"uri"`
	Query     string    `gorm:"column:query" json:"query"`     // JSON
	UserAgent string    `gorm:"column:user_agent" json:"user_agent"`
	Headers   string    `gorm:"column:headers" json:"headers"` // JSON
	CreatedAt time.Time `gorm:"column:created_at;type:datetime" json:"created_at"`
}

func (RssRequestLog) TableName() string { return "rss_request_log" }
