package model

// Payback 映射数据库 payback 表
type Payback struct {
	ID       int64  `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Total    string `gorm:"column:total;type:decimal(12,2)" json:"total"`
	UserID   int64  `gorm:"column:userid" json:"userid"`
	RefBy    int64  `gorm:"column:ref_by" json:"ref_by"`
	RefGet   string `gorm:"column:ref_get;type:decimal(12,2)" json:"ref_get"`
	DateTime int64  `gorm:"column:datetime" json:"datetime"`
	Callback int32  `gorm:"column:callback" json:"callback"`
}

// TableName 设置对应的数据表名
func (Payback) TableName() string {
	return "payback"
}
