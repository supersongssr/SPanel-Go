package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config 全局配置结构体
type Config struct {
	App        AppConfig        `yaml:"app"`
	Security   SecurityConfig   `yaml:"security"`
	Database   DatabaseConfig   `yaml:"database"`
	Redis      RedisConfig      `yaml:"redis"`
	JWT        JWTConfig        `yaml:"jwt"`
	Pow        PowConfig        `yaml:"pow"`
	Payment    PaymentConfig    `yaml:"payment"`
	Telegram   TelegramConfig   `yaml:"telegram"`
	Invite     InviteConfig     `yaml:"invite"`
	Cloudflare CloudflareConfig `yaml:"cloudflare"`
	Node       NodeConfig       `yaml:"node"`
}

// PaymentConfig 支付系统配置 (对齐 PHP payment_system / clonepay_apis)
type PaymentConfig struct {
	System       string                     `yaml:"system"` // none | fakapay | clonepay
	ClonepayAPIs map[string]ClonepayAPIConf `yaml:"clonepay_apis"`
}

// ClonepayAPIConf 单个 clonepay 站点密钥三元组
type ClonepayAPIConf struct {
	SafeIP   string `yaml:"safeip"`
	SafeIPV6 string `yaml:"safeipv6"`
	PayToken string `yaml:"paytoken"`
}

// TelegramConfig 管理通知 bot 配置 (对齐 PHP enable_telegram / telegram_token 等)
type TelegramConfig struct {
	Enable       bool   `yaml:"enable"`
	Token        string `yaml:"token"`          // bot token
	AdminToken   string `yaml:"admin_token"`    // 管理通知 bot token, 留空回退 token
	AdminChatID  string `yaml:"admin_chat_id"`  // 管理通知目标 chat id
	AdminTopicID int64  `yaml:"admin_topic_id"` // 论坛群话题 id, 0 不指定
}

// InviteConfig 邀请返利配置 (对齐 PHP code_payback / code_payback2 / invite_gift_money / invite_gift)
type InviteConfig struct {
	CodePayback     float64 `yaml:"code_payback"`      // 一级返利 %
	CodePayback2    float64 `yaml:"code_payback2"`     // 二级返利 % (两级合计勿超 50)
	InviteGiftMoney float64 `yaml:"invite_gift_money"` // 达标奖励金额
	InviteGift      float64 `yaml:"invite_gift"`       // 达标奖励流量 (GB)
}

// CloudflareConfig CF DNS 配置 (对齐 PHP cloudflare_api_token / node_domain_pool)
type CloudflareConfig struct {
	APIToken   string                    `yaml:"api_token"`
	DomainPool map[string]DomainPoolConf `yaml:"domain_pool"`
}

// DomainPoolConf 域名池单域名配置
// 对齐 PHP node_domain_pool 条目: provider/zone_id/records_limit/expires_at/api_token/cdn
type DomainPoolConf struct {
	Provider     string `yaml:"provider"`      // 默认 cloudflare
	ZoneID       string `yaml:"zone_id"`       // 留空则用 token 调 API 自动查询
	RecordsLimit int    `yaml:"records_limit"` // 0 = 不限
	ExpiresAt    string `yaml:"expires_at"`    // 仅运维告警用
	APIToken     string `yaml:"api_token"`     // 域名独立 token, 留空回退默认
	CDN          bool   `yaml:"cdn"`           // 是否 xhttp-cdn 专用域名
}

// NodeConfig 节点生命周期相关配置 (对齐 PHP node_default_v2_name 等)
type NodeConfig struct {
	DefaultV2Name          string `yaml:"default_v2_name"`           // 协议组缺省名
	FallbackHost           string `yaml:"fallback_host"`             // nginx 伪装回落域名
	EnforceReporterIPCheck *bool  `yaml:"enforce_reporter_ip_check"` // 上报者 IP 强制校验, 缺省 true
	// NodeGroup 对齐 PHP Config::get('node_group') (register 未上报 node_group 时的回退默认分组)。
	// PHP config.default.php 默认 '4'; Go 0 视为未配置, 读取时回退 4。
	NodeGroup int `yaml:"node_group"`
	// NodeCloneClassMax 对齐 PHP Config::getWithDefault('node_clone_class_max', 10)
	// (clone 节点等级随机上限)。Go 0 视为未配置, 读取时回退 10。
	NodeCloneClassMax int `yaml:"node_clone_class_max"`
	// CloudflareCDNPreferredDomains 对齐 PHP Config 键 cloudflare_cdn_preferred_domains
	// (CDN 模式 add= 优选域名池, 空 → 回退节点真实 IP)。PHP 默认 []。
	CloudflareCDNPreferredDomains []string `yaml:"cloudflare_cdn_preferred_domains"`
}

// EnforceIPCheck 解析后的上报者 IP 强制校验开关 (缺省 true, 对齐 PHP getWithDefault)
func (n *NodeConfig) EnforceIPCheck() bool {
	if n == nil || n.EnforceReporterIPCheck == nil {
		return true
	}
	return *n.EnforceReporterIPCheck
}

type AppConfig struct {
	Name  string `yaml:"name"`
	Port  int    `yaml:"port"` // 监听端口
	Key   string `yaml:"key"`
	Salt  string `yaml:"salt"`
	MuKey string `yaml:"muKey"`
}

type SecurityConfig struct {
	EnableRegCaptcha   bool `yaml:"enable_reg_captcha"`
	EnableLoginCaptcha bool `yaml:"enable_login_captcha"`
	EnableEmailVerify  bool `yaml:"enable_email_verify"`
}

type DatabaseConfig struct {
	Driver             string `yaml:"driver"`
	Host               string `yaml:"host"`
	Database           string `yaml:"database"`
	Username           string `yaml:"username"`
	Password           string `yaml:"password"`
	Charset            string `yaml:"charset"`
	MaxIdleConns       int    `yaml:"max_idle_conns"`
	MaxOpenConns       int    `yaml:"max_open_conns"`
	ConnMaxLifetimeMin int    `yaml:"conn_max_lifetime_mins"`
}

type RedisConfig struct {
	Host     string `yaml:"host"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

type JWTConfig struct {
	Secret      string `yaml:"secret"`
	ExpireHours int    `yaml:"expire_hours"`
}

type PowConfig struct {
	Enabled       bool  `yaml:"enabled"`
	MinDifficulty int64 `yaml:"min_difficulty"`
}

// GlobalConfig 全局配置句柄
var GlobalConfig *Config

// LoadConfig 从指定路径加载 YAML 配置文件
func LoadConfig(path string) (*Config, error) {
	file, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file error: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(file, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal yaml error: %w", err)
	}

	GlobalConfig = &cfg
	return &cfg, nil
}
