package model

// RadiusBan 映射数据库 radius_ban 表 (CheckJob 用户封禁标记, Radius 账号联动)。
type RadiusBan struct {
	ID     int32 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Userid int32 `gorm:"column:userid;index" json:"userid"`
}

// TableName 设置对应的数据表名
func (RadiusBan) TableName() string { return "radius_ban" }
