package model

// Bought 映射数据库 bought 表
type Bought struct {
	ID       int64  `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	UserID   int64  `gorm:"column:userid" json:"userid"`
	ShopID   int64  `gorm:"column:shopid" json:"shopid"`
	DateTime int64  `gorm:"column:datetime" json:"datetime"`
	Renew    int64  `gorm:"column:renew" json:"renew"`
	Coupon   string `gorm:"column:coupon" json:"coupon"`
	Price    string `gorm:"column:price;type:decimal(12,2)" json:"price"`
}

// TableName 设置对应的数据表名
func (Bought) TableName() string {
	return "bought"
}
