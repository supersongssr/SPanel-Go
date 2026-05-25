package model

import "time"

// Announcement 映射数据库 announcement 表
type Announcement struct {
	ID       int32     `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Date     time.Time `gorm:"column:date;type:datetime" json:"date"`
	Content  string    `gorm:"column:content" json:"content"`
	Markdown string    `gorm:"column:markdown" json:"markdown"`
}

// TableName 设置对应的数据表名
func (Announcement) TableName() string {
	return "announcement"
}
