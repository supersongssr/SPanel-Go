package model

// Cncdn 映射数据库 cncdn 表 (中转加速线路)
type Cncdn struct {
	ID     int32  `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Area   string `gorm:"column:area" json:"area"`
	AreaID string `gorm:"column:areaid" json:"areaid"`
	Server string `gorm:"column:server" json:"server"`
	CDNIP  string `gorm:"column:cdnip" json:"cdnip"`
	IPMD5  string `gorm:"column:ipmd5" json:"ipmd5"`
	Host   string `gorm:"column:host" json:"host"`
	Show   int32  `gorm:"column:show" json:"show"`
	Status int32  `gorm:"column:status" json:"status"`
}

// TableName 设置对应的数据表名 (与 PHP 共库)
func (Cncdn) TableName() string { return "cncdn" }

// BlockIp 映射数据库 blockip 表 (节点封禁 IP)
type BlockIp struct {
	ID       int64  `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	NodeID   int32  `gorm:"column:nodeid" json:"nodeid"`
	IP       string `gorm:"column:ip" json:"ip"`
	DateTime int64  `gorm:"column:datetime" json:"datetime"`
}

// TableName 设置对应的数据表名 (与 PHP 共库)
func (BlockIp) TableName() string { return "blockip" }

// UnblockIp 映射数据库 unblockip 表 (解封命令记录)
type UnblockIp struct {
	ID       int64  `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	IP       string `gorm:"column:ip" json:"ip"`
	DateTime int64  `gorm:"column:datetime" json:"datetime"`
	UserID   int64  `gorm:"column:userid" json:"userid"`
}

// TableName 设置对应的数据表名 (与 PHP 共库)
func (UnblockIp) TableName() string { return "unblockip" }
