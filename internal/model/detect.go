package model

// DetectRule 映射数据库 detect_list 表 (审计规则)
type DetectRule struct {
	ID    int64  `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Name  string `gorm:"column:name" json:"name"`
	Text  string `gorm:"column:text" json:"text"`
	Regex string `gorm:"column:regex" json:"regex"`
	Type  int32  `gorm:"column:type" json:"type"` // 1 明文 2 hex
}

// TableName 设置对应的数据表名 (与 PHP 共库)
func (DetectRule) TableName() string { return "detect_list" }

// DetectLog 映射数据库 detect_log 表 (审计触发记录)
type DetectLog struct {
	ID       int64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	UserID   int64 `gorm:"column:user_id" json:"user_id"`
	ListID   int64 `gorm:"column:list_id" json:"list_id"`
	NodeID   int32 `gorm:"column:node_id" json:"node_id"`
	DateTime int64 `gorm:"column:datetime" json:"datetime"`
}

// TableName 设置对应的数据表名 (与 PHP 共库)
func (DetectLog) TableName() string { return "detect_log" }
