package model

// PasswordReset 映射数据库 ss_password_reset 表 (密码重置令牌, 24h 有效)
type PasswordReset struct {
	ID         int32  `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Email      string `gorm:"column:email" json:"email"`
	Token      string `gorm:"column:token" json:"token"`
	InitTime   int64  `gorm:"column:init_time" json:"init_time"`
	ExpireTime int64  `gorm:"column:expire_time" json:"expire_time"`
}

// TableName 设置对应的数据表名 (与 PHP 共库)
func (PasswordReset) TableName() string { return "ss_password_reset" }
