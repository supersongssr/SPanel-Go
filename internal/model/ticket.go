package model

// Ticket 映射数据库 ticket 表 (用于支持工单处理系统)
type Ticket struct {
	ID       int64  `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Title    string `gorm:"column:title" json:"title"`
	Content  string `gorm:"column:content" json:"content"`
	RootID   int64  `gorm:"column:rootid" json:"rootid"`
	UserID   int64  `gorm:"column:userid" json:"userid"`
	Sort     int32  `gorm:"column:sort" json:"sort"`
	DateTime int64  `gorm:"column:datetime" json:"datetime"`
	Status   int32  `gorm:"column:status" json:"status"`
}

// TableName 设置表名
func (Ticket) TableName() string {
	return "ticket"
}
