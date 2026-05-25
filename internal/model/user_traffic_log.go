package model

// UserTrafficLog 映射数据库 user_traffic_log 表
type UserTrafficLog struct {
	ID      int32   `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	UserID  int32   `gorm:"column:user_id?" json:"user_id"`
	U       int64   `gorm:"column:u" json:"u"`
	D       int64   `gorm:"column:d" json:"d"`
	NodeID  int32   `gorm:"column:node_id" json:"node_id"`
	Rate    float32 `gorm:"column:rate" json:"rate"`
	Traffic string  `gorm:"column:traffic" json:"traffic"`
	LogTime int32   `gorm:"column:log_time" json:"log_time"`
}

// TableName 设置对应的数据表名
func (UserTrafficLog) TableName() string {
	return "user_traffic_log"
}
