package model

// Shop 映射数据库 shop 表
type Shop struct {
	ID                 int64  `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Name               string `gorm:"column:name" json:"name"`
	Price              string `gorm:"column:price;type:decimal(12,2)" json:"price"`
	Content            string `gorm:"column:content" json:"content"`
	AutoRenew          int32  `gorm:"column:auto_renew" json:"auto_renew"`
	AutoResetBandwidth int32  `gorm:"column:auto_reset_bandwidth" json:"auto_reset_bandwidth"`
	Status             int32  `gorm:"column:status" json:"status"`
}

// TableName 设置对应的数据表名
func (Shop) TableName() string {
	return "shop"
}
