package model

// EmailVerify 映射数据库 email_verify 表 (注册邮箱验证码)
type EmailVerify struct {
	ID       int64  `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Email    string `gorm:"column:email" json:"email"`
	IP       string `gorm:"column:ip" json:"ip"`
	Code     string `gorm:"column:code" json:"code"`
	ExpireIn int64  `gorm:"column:expire_in" json:"expire_in"`
}

// TableName 设置对应的数据表名 (与 PHP 共库)
func (EmailVerify) TableName() string { return "email_verify" }
