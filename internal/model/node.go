package model

// Node 映射数据库 ss_node 表
type Node struct {
	ID               int32   `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Name             string  `gorm:"column:name" json:"name"`
	Type             int32   `gorm:"column:type" json:"type"` // 1显示 0不显示
	Server           string  `gorm:"column:server" json:"server"`
	Method           string  `gorm:"column:method" json:"method"`
	Info             string  `gorm:"column:info" json:"info"`
	Status           string  `gorm:"column:status" json:"status"`
	Sort             int32   `gorm:"column:sort" json:"sort"`
	CustomMethod     int8    `gorm:"column:custom_method" json:"custom_method"`
	TrafficRate      float32 `gorm:"column:traffic_rate" json:"traffic_rate"`
	NodeSpeedlimit   string  `gorm:"column:node_speedlimit;type:decimal(12,2)" json:"node_speedlimit"`
	NodeConnector    int32   `gorm:"column:node_connector" json:"node_connector"`
	NodeBandwidth    string  `gorm:"column:node_bandwidth;type:decimal(12,2)" json:"node_bandwidth"`
	NodeHeartbeat    int64   `gorm:"column:node_heartbeat" json:"node_heartbeat"`
	NodeIP           string  `gorm:"column:node_ip" json:"node_ip"`
	NodeGroup        int32   `gorm:"column:node_group" json:"node_group"`
	CustomRSS        int32   `gorm:"column:custom_rss" json:"custom_rss"`
	MuOnly           int32   `gorm:"column:mu_only" json:"mu_only"`
	NodeRxTx         string  `gorm:"column:node_rxtx" json:"node_rxtx"`
	TrafficResetDay  int32   `gorm:"column:traffic_reset_day" json:"traffic_reset_day"`
	NodeCost         int32   `gorm:"column:node_cost" json:"node_cost"`
	NodeOnline       int32   `gorm:"column:node_online" json:"node_online"`
	NodeOnCost       float32 `gorm:"column:node_oncost" json:"node_oncost"`
	NodeSort         int32   `gorm:"column:node_sort" json:"node_sort"`
	Cncdn            int8    `gorm:"column:cncdn" json:"cncdn"`
	IsClone          int32   `gorm:"column:is_clone" json:"is_clone"`
	TrafficUsed      int64   `gorm:"column:traffic_used" json:"traffic_used"`
	TrafficLeft      int64   `gorm:"column:traffic_left" json:"traffic_left"`
	TrafficUsedDaily int64   `gorm:"column:traffic_used_daily" json:"traffic_used_daily"`
	TrafficLeftDaily int64   `gorm:"column:traffic_left_daily" json:"traffic_left_daily"`
	TrafficLimit     int64   `gorm:"column:traffic_limit" json:"traffic_limit"`
	TrafficRawTotal  int64   `gorm:"column:traffic_raw_total" json:"traffic_raw_total"`
	NodeIDs          string  `gorm:"column:node_ids" json:"node_ids"`
	NodeUnlock       string  `gorm:"column:node_unlock" json:"node_unlock"`
	CountryCode      string  `gorm:"column:country_code" json:"country_code"`
	ServerUptime     int32   `gorm:"column:server_uptime" json:"server_uptime"`
	// —— v2 API 补充列 (与 PHP 共库已存在) ——
	NodeClass            int32  `gorm:"column:node_class" json:"node_class"`                           // 节点等级门槛
	APIToken             string `gorm:"column:api_token" json:"api_token"`                             // v2 backend 每节点独立 token (43 字符 base64url)
	APIIPAllowlist       string `gorm:"column:api_ip_allowlist" json:"api_ip_allowlist"`               // 可选 IP 白名单 (逗号分隔 IP/CIDR)
	ConfigHash           string `gorm:"column:config_hash" json:"config_hash"`                         // xray 配置 sha256 指纹
	TrafficLastResetTime int64  `gorm:"column:traffic_last_reset_time" json:"traffic_last_reset_time"` // 月度重置基准
	TrafficMaxDayValue   int64  `gorm:"column:traffic_max_day_value" json:"traffic_max_day_value"`     // 历史日峰值 (bytes)
	TrafficUsedLastday   int64  `gorm:"column:traffic_used_lastday" json:"traffic_used_lastday"`
	NodeBandwidthLastday string `gorm:"column:node_bandwidth_lastday;type:decimal(12,2)" json:"node_bandwidth_lastday"`
	NodeBandwidthLimit   string `gorm:"column:node_bandwidth_limit;type:decimal(12,2)" json:"node_bandwidth_limit"`
	BandwidthResetDay    int32  `gorm:"column:bandwidthlimit_resetday" json:"bandwidthlimit_resetday"` //nolint 废弃字段保留对齐
	ServerTotalTraffic   int64  `gorm:"column:server_total_traffic" json:"server_total_traffic"`
}

// TableName 设置对应的数据表名
func (Node) TableName() string {
	return "ss_node"
}
