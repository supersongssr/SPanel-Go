package model

// Speedtest 映射数据库 speedtest 表 (节点三网测速记录)
// 注意: unicomupload 列在历史库中带问号 (title? 同款历史遗留), 保持原样对齐。
type Speedtest struct {
	ID               int64  `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	NodeID           int32  `gorm:"column:nodeid" json:"nodeid"`
	DateTime         int64  `gorm:"column:datetime" json:"datetime"`
	Telecomping      string `gorm:"column:telecomping" json:"telecomping"`
	Telecomeupload   string `gorm:"column:telecomeupload" json:"telecomeupload"`
	Telecomedownload string `gorm:"column:telecomedownload" json:"telecomedownload"`
	Unicomping       string `gorm:"column:unicomping" json:"unicomping"`
	Unicomupload     string `gorm:"column:unicomupload?" json:"unicomupload"`
	Unicomdownload   string `gorm:"column:unicomdownload" json:"unicomdownload"`
	Cmccping         string `gorm:"column:cmccping" json:"cmccping"`
	Cmccupload       string `gorm:"column:cmccupload" json:"cmccupload"`
	Cmccdownload     string `gorm:"column:cmccdownload" json:"cmccdownload"`
}

// TableName 设置对应的数据表名 (与 PHP 共库)
func (Speedtest) TableName() string { return "speedtest" }
