package model

// LoginIP 映射数据库 login_ip 表
type LoginIP struct {
	ID       int64  `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	UserID   int64  `gorm:"column:userid" json:"userid"`
	IP       string `gorm:"column:ip" json:"ip"`
	DateTime int64  `gorm:"column:datetime" json:"datetime"`
	Type     int32  `gorm:"column:type" json:"type"`
}

// TableName 设置对应的数据表名
func (LoginIP) TableName() string {
	return "login_ip"
}
