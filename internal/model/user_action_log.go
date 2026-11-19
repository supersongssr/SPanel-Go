package model

import "time"

// UserActionLog 用户操作审计日志 (表结构对齐 PHP user_action_log, jobs 已在清理该表;
// admin v2 补齐查询/写入能力 — 方案 .plans/2026-11-18 §4.2)
type UserActionLog struct {
	ID           int64     `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	UserID       int32     `gorm:"column:user_id" json:"user_id"`             // 被操作的目标用户
	OperatorID   int32     `gorm:"column:operator_id" json:"operator_id"`     // 操作者; 0=系统
	OperatorType int8      `gorm:"column:operator_type" json:"operator_type"` // 0系统 1用户自己 2管理员 3API/节点
	Level        string    `gorm:"column:level" json:"level"`                 // info/warning/error
	Category     string    `gorm:"column:category" json:"category"`           // auth/profile/security/money/traffic/ban/admin/ticket/node
	Action       string    `gorm:"column:action" json:"action"`               // 机器可读动作键, 如 user.ban
	Message      string    `gorm:"column:message" json:"message"`             // 中文人类可读摘要
	Detail       string    `gorm:"column:detail" json:"detail"`               // 变更前后 JSON (text)
	IP           string    `gorm:"column:ip" json:"ip"`
	UserAgent    string    `gorm:"column:user_agent" json:"user_agent"`
	CreatedAt    time.Time `gorm:"column:created_at;type:datetime" json:"created_at"`
}

func (UserActionLog) TableName() string { return "user_action_log" }
