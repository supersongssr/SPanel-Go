package model

import "time"

// InviteCode 映射数据库 ss_invite_code 表
type InviteCode struct {
	ID        int32     `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Code      string    `gorm:"column:code" json:"code"`
	UserID    int32     `gorm:"column:user_id" json:"user_id"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
}

// TableName 设置对应的数据表名
func (InviteCode) TableName() string {
	return "ss_invite_code"
}
