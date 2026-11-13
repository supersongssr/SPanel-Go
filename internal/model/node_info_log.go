package model

// NodeInfoLog 映射数据库 ss_node_info 表 (节点负载/uptime 心跳日志)
type NodeInfoLog struct {
	ID      int32   `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	NodeID  int32   `gorm:"column:node_id" json:"node_id"`
	Uptime  float32 `gorm:"column:uptime" json:"uptime"`
	Load    string  `gorm:"column:load" json:"load"` // "x.xx x.xx x.xx"
	LogTime int32   `gorm:"column:log_time" json:"log_time"`
}

// TableName 设置对应的数据表名
func (NodeInfoLog) TableName() string { return "ss_node_info" }

// NodeOnlineLog 映射数据库 ss_node_online_log 表 (节点在线人数日志)
type NodeOnlineLog struct {
	ID         int32 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	NodeID     int32 `gorm:"column:node_id" json:"node_id"`
	OnlineUser int32 `gorm:"column:online_user" json:"online_user"`
	LogTime    int32 `gorm:"column:log_time" json:"log_time"`
}

// TableName 设置对应的数据表名
func (NodeOnlineLog) TableName() string { return "ss_node_online_log" }
