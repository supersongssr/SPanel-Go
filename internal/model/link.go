package model

// Link 映射数据库 link 表 (用于匹配用户订阅 Token)
type Link struct {
	ID      int64  `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Type    int32  `gorm:"column:type" json:"type"`
	Address string `gorm:"column:address" json:"address"`
	Port    int32  `gorm:"column:port" json:"port"`
	Token   string `gorm:"column:token" json:"token"`
	Ios     int32  `gorm:"column:ios" json:"ios"`
	UserID  int64  `gorm:"column:userid" json:"userid"`
	ISP     string `gorm:"column:isp" json:"isp"`
	Geo     int32  `gorm:"column:geo" json:"geo"`
	Method  string `gorm:"column:method" json:"method"`
}

// TableName 设置对应的数据表名
func (Link) TableName() string {
	return "link"
}
