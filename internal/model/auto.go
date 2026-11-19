package model

// Auto 自动下发命令 (对齐 PHP auto 表: type=1 命令下发 / 0 已被执行)
type Auto struct {
	ID       int64  `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Type     int32  `gorm:"column:type" json:"type"`
	Value    string `gorm:"column:value" json:"value"` // 命令内容
	Sign     string `gorm:"column:sign" json:"sign"`
	DateTime int64  `gorm:"column:datetime" json:"datetime"`
}

func (Auto) TableName() string { return "auto" }
