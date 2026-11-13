package model

// DnsRecord 映射数据库 dns_records 表 (节点注册/对账/注销的 DNS 记录)
type DnsRecord struct {
	ID         int32  `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	NodeID     int32  `gorm:"column:node_id" json:"node_id"`
	RecordType string `gorm:"column:record_type" json:"record_type"` // A | AAAA
	Subdomain  string `gorm:"column:subdomain" json:"subdomain"`
	RootDomain string `gorm:"column:root_domain" json:"root_domain"`
	Content    string `gorm:"column:content" json:"content"` // IP 地址
	Proxied    int8   `gorm:"column:proxied" json:"proxied"`
	TTL        int32  `gorm:"column:ttl" json:"ttl"`
	CFRecordID string `gorm:"column:cf_record_id" json:"cf_record_id"`
	UpdatedAt  int32  `gorm:"column:updated_at" json:"updated_at"`
}

// TableName 设置对应的数据表名
func (DnsRecord) TableName() string { return "dns_records" }
