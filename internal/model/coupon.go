package model

// Coupon 映射数据库 coupon 表
type Coupon struct {
	ID      int64  `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Code    string `gorm:"column:code" json:"code"`
	Onetime int32  `gorm:"column:onetime" json:"onetime"`
	Expire  int64  `gorm:"column:expire" json:"expire"`
	Shop    string `gorm:"column:shop" json:"shop"`
	Credit  int32  `gorm:"column:credit" json:"credit"`
}

// TableName 设置对应的数据表名
func (Coupon) TableName() string {
	return "coupon"
}
