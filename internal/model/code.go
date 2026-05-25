package model

import "time"

// Code 映射数据库 code 表 (充值码/激活码)
type Code struct {
	ID          int64     `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Code        string    `gorm:"column:code" json:"code"`
	Type        int32     `gorm:"column:type" json:"type"`
	Number      string    `gorm:"column:number;type:decimal(11,2)" json:"number"`
	IsUsed      int32     `gorm:"column:isused" json:"isused"`
	UserID      int64     `gorm:"column:userid" json:"userid"`
	UseDatetime time.Time `gorm:"column:usedatetime;type:datetime" json:"usedatetime"`
}

// TableName 设置对应的数据表名
func (Code) TableName() string {
	return "code"
}
