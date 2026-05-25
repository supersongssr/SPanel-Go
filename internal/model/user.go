package model

import (
	"time"
)

// User 映射数据库 user 表 (禁用自动 GORM timestamps 以防写回报错)
type User struct {
	ID                 int32     `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	UserName           string    `gorm:"column:user_name" json:"user_name"`
	Email              string    `gorm:"column:email" json:"email"`
	Pass               string    `gorm:"column:pass" json:"-"`       // 登录哈希密码，禁止 json 输出
	Passwd             string    `gorm:"column:passwd" json:"passwd"` // SS 链接密码
	Port               int32     `gorm:"column:port" json:"port"`
	U                  int64     `gorm:"column:u" json:"u"` // 上行流量 (bytes)
	D                  int64     `gorm:"column:d" json:"d"` // 下行流量 (bytes)
	TransferEnable     int64     `gorm:"column:transfer_enable" json:"transfer_enable"`
	Enable             int8      `gorm:"column:enable" json:"enable"`
	IsAdmin            int32     `gorm:"column:is_admin" json:"is_admin"`
	Class              int32     `gorm:"column:class" json:"class"`
	NodeGroup          int32     `gorm:"column:node_group" json:"node_group"`
	ExpireTime         int32     `gorm:"column:expire_time" json:"expire_time"`
	ExpireIn           time.Time `gorm:"column:expire_in;type:datetime" json:"expire_in"`
	ClassExpire        time.Time `gorm:"column:class_expire;type:datetime" json:"class_expire"`
	Money              string    `gorm:"column:money;type:decimal(12,2)" json:"money"` // 用 string 接收 decimal 规避 JS 精度损失
	RegDate            time.Time `gorm:"column:reg_date;type:datetime" json:"reg_date"`
	InviteNum          int32     `gorm:"column:invite_num" json:"invite_num"`
	RefBy              int32     `gorm:"column:ref_by" json:"ref_by"`
	Method             string    `gorm:"column:method" json:"method"`
	RegIP              string    `gorm:"column:reg_ip" json:"reg_ip"`
	NodeSpeedlimit     string    `gorm:"column:node_speedlimit;type:decimal(12,2)" json:"node_speedlimit"`
	NodeConnector      int32     `gorm:"column:node_connector" json:"node_connector"`
	LastCheckInTime    int32     `gorm:"column:last_check_in_time" json:"last_check_in_time"`
	V2rayUUID          string    `gorm:"column:v2ray_uuid" json:"v2ray_uuid"`
	Protocol           string    `gorm:"column:protocol" json:"protocol"`
	ProtocolParam      string    `gorm:"column:protocol_param" json:"protocol_param"`
	Obfs               string    `gorm:"column:obfs" json:"obfs"`
	ObfsParam          string    `gorm:"column:obfs_param" json:"obfs_param"`
	SubLimit           int32     `gorm:"column:sub_limit" json:"sub_limit"`
}

// TableName 设置对应的数据表名
func (User) TableName() string {
	return "user"
}
