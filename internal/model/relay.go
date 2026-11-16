package model

// Relay 映射数据库 relay 表 (用户中转规则)
type Relay struct {
	ID           int64  `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	UserID       int64  `gorm:"column:user_id" json:"user_id"`
	SourceNodeID int64  `gorm:"column:source_node_id" json:"source_node_id"`
	DistNodeID   int64  `gorm:"column:dist_node_id" json:"dist_node_id"`
	DistIP       string `gorm:"column:dist_ip" json:"dist_ip"`
	Port         int32  `gorm:"column:port" json:"port"`
	Priority     int32  `gorm:"column:priority" json:"priority"`
}

// TableName 设置对应的数据表名 (与 PHP 共库)
func (Relay) TableName() string { return "relay" }
