package model

// AliveIP 映射数据库 alive_ip 表
type AliveIP struct {
	ID       int64  `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	NodeID   int32  `gorm:"column:nodeid" json:"nodeid"`
	UserID   int32  `gorm:"column:userid" json:"userid"`
	IP       string `gorm:"column:ip" json:"ip"`
	DateTime int64  `gorm:"column:datetime" json:"datetime"`
}

// TableName 设置对应的数据表名
func (AliveIP) TableName() string {
	return "alive_ip"
}
