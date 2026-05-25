package model

// Node 映射数据库 ss_node 表
type Node struct {
	ID                int32   `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Name              string  `gorm:"column:name" json:"name"`
	Type              int32   `gorm:"column:type" json:"type"` // 1显示 0不显示
	Server            string  `gorm:"column:server" json:"server"`
	Method            string  `gorm:"column:method" json:"method"`
	Info              string  `gorm:"column:info" json:"info"`
	Status            string  `gorm:"column:status" json:"status"`
	Sort              int32   `gorm:"column:sort" json:"sort"`
	CustomMethod      int8    `gorm:"column:custom_method" json:"custom_method"`
	TrafficRate       float32 `gorm:"column:traffic_rate" json:"traffic_rate"`
	NodeClass         int32   `gorm:"column:node_class" json:"node_class"` // 节点访问的等级限制
	NodeSpeedlimit     string  `gorm:"column:node_speedlimit;type:decimal(12,2)" json:"node_speedlimit"`
	NodeConnector     int32   `gorm:"column:node_connector" json:"node_connector"`
	NodeBandwidth     string  `gorm:"column:node_bandwidth;type:decimal(12,2)" json:"node_bandwidth"`
	NodeHeartbeat     int64   `gorm:"column:node_heartbeat" json:"node_heartbeat"`
	NodeIP            string  `gorm:"column:node_ip" json:"node_ip"`
	NodeGroup         int32   `gorm:"column:node_group" json:"node_group"`
	CustomRSS         int32   `gorm:"column:custom_rss" json:"custom_rss"`
	MuOnly            int32   `gorm:"column:mu_only" json:"mu_only"`
	NodeRxTx          string  `gorm:"column:node_rxtx" json:"node_rxtx"`
	TrafficResetDay   int32   `gorm:"column:traffic_reset_day" json:"traffic_reset_day"`
	NodeCost          int32   `gorm:"column:node_cost" json:"node_cost"`
	NodeOnline        int32   `gorm:"column:node_online" json:"node_online"`
	NodeOnCost        float32 `gorm:"column:node_oncost" json:"node_oncost"`
	NodeSort          int32   `gorm:"column:node_sort" json:"node_sort"`
	Cncdn             int8    `gorm:"column:cncdn" json:"cncdn"`
	IsClone           int32   `gorm:"column:is_clone" json:"is_clone"`
	TrafficUsed       int64   `gorm:"column:traffic_used" json:"traffic_used"`
	TrafficLeft       int64   `gorm:"column:traffic_left" json:"traffic_left"`
	TrafficUsedDaily  int64   `gorm:"column:traffic_used_daily" json:"traffic_used_daily"`
	TrafficLeftDaily  int64   `gorm:"column:traffic_left_daily" json:"traffic_left_daily"`
	TrafficLimit      int64   `gorm:"column:traffic_limit" json:"traffic_limit"`
	TrafficRawTotal   int64   `gorm:"column:traffic_raw_total" json:"traffic_raw_total"`
	NodeIDs           string  `gorm:"column:node_ids" json:"node_ids"`
	NodeUnlock        string  `gorm:"column:node_unlock" json:"node_unlock"`
	CountryCode       string  `gorm:"column:country_code" json:"country_code"`
	ServerUptime      int32   `gorm:"column:server_uptime" json:"server_uptime"`
}

// TableName 设置对应的数据表名
func (Node) TableName() string {
	return "ss_node"
}
