package config

import (
	"fmt"
	"os"
	"path/filepath"

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
	Jobs       JobsConfig       `yaml:"jobs"`
	SMTP       SMTPConfig       `yaml:"smtp"`
	Radius     RadiusConfig     `yaml:"radius"`
	Backup     BackupConfig     `yaml:"backup"`
	User       UserConfig       `yaml:"user"` // 用户端业务开关 (对齐 PHP min_port/max_port/port_price*/enable_kill/enable_user_custom_group)
	RssHeaderCapture RssHeaderCaptureConfig `yaml:"rss_header_capture"` // 订阅请求抓取 (对齐 PHP 同名开关, admin 后台查询)
}

// RssHeaderCaptureConfig 订阅请求抓取开关 (PHP config['rss_header_capture'])
type RssHeaderCaptureConfig struct {
	Enabled    bool `yaml:"enabled"`     // 是否采集 /link 请求
	RetainDays int  `yaml:"retain_days"` // 保留天数 (清理任务用, 0=永久)
}

// UserConfig 用户端业务开关 (缺省值在 Usage 侧内联回退, 未配置时全部取 PHP 默认)
type UserConfig struct {
	MinPort               int     `yaml:"min_port"`                 // 端口池下限, 默认 11111
	MaxPort               int     `yaml:"max_port"`                 // 端口池上限, 默认 55555
	PortPrice             float64 `yaml:"port_price"`               // 随机重置端口价格, <0 关闭购买, 默认 0.1
	PortPriceSpecify      float64 `yaml:"port_price_specify"`       // 钉定端口价格, <0 关闭, 默认 1
	EnableKill            bool    `yaml:"enable_kill"`              // 允许用户注销账户, 默认 false
	EnableUserCustomGroup bool    `yaml:"enable_user_custom_group"` // 自助换分组, 默认 false
}

// SMTPConfig 邮件发送配置 (对齐 PHP mailDriver=smtp 的 smtp_* 键; P2 mailer)
type SMTPConfig struct {
	Host     string `yaml:"host"` // smtp 服务器
	Port     int    `yaml:"port"` // 465=SSL / 587=STARTTLS
	Username string `yaml:"username"`
	Password string `yaml:"password"` // PHP 键名拼写 smtp_passsword
	Sender   string `yaml:"sender"`   // 发件人地址
	Name     string `yaml:"name"`     // 发件人显示名
	SSL      bool   `yaml:"ssl"`      // smtp_ssl: 465→隐式 TLS, 587→STARTTLS
}

// RadiusConfig Radius 独立库连接 (对齐 PHP radius_db_* 键; enable_radius 总开关)
type RadiusConfig struct {
	Enable   bool   `yaml:"enable"`   // enable_radius
	Host     string `yaml:"host"`     // radius_db_host (host:port)
	User     string `yaml:"user"`     // radius_db_user
	Password string `yaml:"password"` // radius_db_password
	Database string `yaml:"database"` // radius_db_database
}

// BackupConfig 自动备份配置 (对齐 PHP auto_backup_email / auto_backup_passwd / backup_notify)
type BackupConfig struct {
	Email    string `yaml:"email"`    // 接收备份的邮箱; 空 = 备份任务直接跳过
	Password string `yaml:"password"` // zip 加密密码
	Notify   bool   `yaml:"notify"`   // backup_notify: 完成后 TG 播报
}

// JobsConfig 后台任务调度配置 (对齐 /etc/crontab 的 6 条 SPanel 任务)
// schedules 未列出的任务回退注册表内置默认表达式; 列出且置 "" = 不排程仅手动。
type JobsConfig struct {
	Enabled   bool              `yaml:"enabled"`   // 总开关 (false = serve 不启动调度器, 纯 Web)
	Timezone  string            `yaml:"timezone"`  // cron 时区, 缺省 Asia/Shanghai
	Catchup   bool              `yaml:"catchup"`   // 启动补偿: 停机跨过触发点的任务启动时补跑一次 (默认 false)
	Schedules map[string]string `yaml:"schedules"` // 任务名 → cron 5 段表达式
	Params    JobsParams        `yaml:"params"`    // 任务参数 (对齐 PHP Config::getWithDefault 键)
}

// JobsParams 任务参数。数值 0 视为未配置回退默认; 负数 = 关闭该模块 (对齐 PHP <=0 关闭语义)。
// 默认 true 的 bool 用 *bool (nil = 未配置 → true)。
type JobsParams struct {
	UserRateLimitEnabled           bool    `yaml:"user_rate_limit_enabled"`             // 小时/日限额总开关 (默认 false)
	UserHourlyTrafficLimitGB       int     `yaml:"user_hourly_traffic_limit"`           // 每小时限额 GB, 默认 8
	UserDailyTrafficLimitGB        int     `yaml:"user_daily_traffic_limit"`            // 每日限额 GB, 默认 32
	UserCycleQuotaEnabled          *bool   `yaml:"user_cycle_quota_enabled"`            // 周期配额模块开关, 默认 true
	UserActionLogRetention         int     `yaml:"user_action_log_retention"`           // 审计每用户保留条数, 默认 100
	UserActionLogAnonRetentionDays int     `yaml:"user_action_log_anon_retention_days"` // 匿名审计保留天数, 默认 7
	NodeHeartbeatOfflineSetTimeout int     `yaml:"node_heartbeat_offline_set_timeout"`  // 心跳超时下线秒数, 默认 259200 (3天)
	NodeSortEngine                 string  `yaml:"node_sort_engine"`                    // 健康分引擎 v2|v1, 默认 v2
	NodeForceResetStaleDays        int     `yaml:"node_force_reset_stale_days"`         // 强制重置窗口天, 默认 32 (负=关)
	NodeGcEnable                   *bool   `yaml:"node_gc_enable"`                      // 死节点 GC 总开关, 默认 true
	NodeGcDeadDays                 int     `yaml:"node_gc_dead_days"`                   // L3 删行天数, 默认 64 (负=关)
	NodeDnsCleanupInactiveDays     int     `yaml:"node_dns_cleanup_inactive_days"`      // L2 DNS 清理天数, 默认 32 (负=关)
	NodeDnsCleanupMarkNodeName     *bool   `yaml:"node_dns_cleanup_mark_node_name"`     // 清空后节点名追加 DNS❌, 默认 true
	NodeDeadPoolMinTotal           int     `yaml:"node_dead_pool_min_total"`            // 水位线回收门槛总数, 默认 1000
	NodeDeadPoolMaxRatio           float64 `yaml:"node_dead_pool_max_ratio"`            // 水位线触发比例, 默认 0.50
	NodeDeadPoolTargetRatio        float64 `yaml:"node_dead_pool_target_ratio"`         // 回收目标比例, 默认 0.48
	NodeDeadPoolMinAgeDays         int     `yaml:"node_dead_pool_min_age_days"`         // 回收候选最小死龄, 默认 32
	AccountExpireDeleteDays        int     `yaml:"account_expire_delete_days"`          // 过期 N 天删号 (负=关, 0 也开), 默认 7
	AutoCleanUncheckDays           int     `yaml:"auto_clean_uncheck_days"`             // 0级未签到 N 天删号, 默认 32 (<=0 关)
	AutoCleanUnusedDays            int     `yaml:"auto_clean_unused_days"`              // 0级未使用 N 天删号, 默认 32 (<=0 关)
	AutoCleanMinMoney              float64 `yaml:"auto_clean_min_money"`                // 0级清理余额上限, 默认 1
	TrafficlogDetailKeepHours      int     `yaml:"trafficlog_detail_keep_hours"`        // 流量明细保留小时, 默认 24
	UserTrafficDailyDays           int     `yaml:"user_traffic_daily_days"`             // 日汇总保留天数, 默认 90 (<=0 永久)
	JobRunLogKeepDays              int     `yaml:"job_run_log_keep_days"`               // 任务台账保留天数, 默认 90
}

// ApplyDefaults 填充缺省值 (对齐 PHP getWithDefault 默认口径)
func (p *JobsParams) ApplyDefaults() {
	if p.UserHourlyTrafficLimitGB == 0 {
		p.UserHourlyTrafficLimitGB = 8
	}
	if p.UserDailyTrafficLimitGB == 0 {
		p.UserDailyTrafficLimitGB = 32
	}
	if p.UserCycleQuotaEnabled == nil {
		t := true
		p.UserCycleQuotaEnabled = &t
	}
	if p.UserActionLogRetention == 0 {
		p.UserActionLogRetention = 100
	}
	if p.UserActionLogAnonRetentionDays == 0 {
		p.UserActionLogAnonRetentionDays = 7
	}
	if p.NodeHeartbeatOfflineSetTimeout == 0 {
		p.NodeHeartbeatOfflineSetTimeout = 259200
	}
	if p.NodeSortEngine == "" {
		p.NodeSortEngine = "v2"
	}
	if p.NodeForceResetStaleDays == 0 {
		p.NodeForceResetStaleDays = 32
	}
	if p.NodeGcEnable == nil {
		t := true
		p.NodeGcEnable = &t
	}
	if p.NodeGcDeadDays == 0 {
		p.NodeGcDeadDays = 64
	}
	if p.NodeDnsCleanupInactiveDays == 0 {
		p.NodeDnsCleanupInactiveDays = 32
	}
	if p.NodeDnsCleanupMarkNodeName == nil {
		t := true
		p.NodeDnsCleanupMarkNodeName = &t
	}
	if p.NodeDeadPoolMinTotal == 0 {
		p.NodeDeadPoolMinTotal = 1000
	}
	if p.NodeDeadPoolMaxRatio == 0 {
		p.NodeDeadPoolMaxRatio = 0.50
	}
	if p.NodeDeadPoolTargetRatio == 0 {
		p.NodeDeadPoolTargetRatio = 0.48
	}
	if p.NodeDeadPoolMinAgeDays == 0 {
		p.NodeDeadPoolMinAgeDays = 32
	}
	if p.AccountExpireDeleteDays == 0 {
		p.AccountExpireDeleteDays = 7
	}
	if p.AutoCleanUncheckDays == 0 {
		p.AutoCleanUncheckDays = 32
	}
	if p.AutoCleanUnusedDays == 0 {
		p.AutoCleanUnusedDays = 32
	}
	if p.AutoCleanMinMoney == 0 {
		p.AutoCleanMinMoney = 1
	}
	if p.TrafficlogDetailKeepHours == 0 {
		p.TrafficlogDetailKeepHours = 24
	}
	if p.UserTrafficDailyDays == 0 {
		p.UserTrafficDailyDays = 90
	}
	if p.JobRunLogKeepDays == 0 {
		p.JobRunLogKeepDays = 90
	}
}

// BoolOrDefault *bool 缺省取 def
func BoolOrDefault(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
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
	// CloudflareCDNPreferredIPsCSV 对齐 PHP 键 cloudflare_cdn_preferred_ips_csv
	// (rotateCdnIps 消费型 CSV 池, 末尾 LIFO 弹出; 空 = 未配置, 耗尽/未配回退真实 IP)。
	CloudflareCDNPreferredIPsCSV string `yaml:"cloudflare_cdn_preferred_ips_csv"`
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
	EnableRegCaptcha   bool   `yaml:"enable_reg_captcha"`
	EnableLoginCaptcha bool   `yaml:"enable_login_captcha"`
	EnableEmailVerify  bool   `yaml:"enable_email_verify"`
	RegisterMode       string `yaml:"register_mode"`      // open | invite | close (PHP 同名键, 默认 open)
	EmailVerifyTTL     int    `yaml:"email_verify_ttl"`   // EV 验证码有效期秒, 默认 600
	EmailVerifyIPLimit int    `yaml:"email_verify_iplimit"` // 同 IP 在途验证码上限, 默认 5
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

// LoadConfig 从指定路径加载 YAML 配置文件 (config.yaml 默认配置)。
// 若同目录存在 .config.yaml (用户隐私覆盖配置, 已加入 .gitignore),
// 则将其同名键深度覆盖进默认配置 —— 密码、密钥等敏感信息应放在 .config.yaml。
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file error: %w", err)
	}

	if overridePath := filepath.Join(filepath.Dir(path), ".config.yaml"); fileExists(overridePath) {
		override, err := os.ReadFile(overridePath)
		if err != nil {
			return nil, fmt.Errorf("read override config %s error: %w", overridePath, err)
		}
		if data, err = mergeYAML(data, override); err != nil {
			return nil, fmt.Errorf("merge %s error: %w", overridePath, err)
		}
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal yaml error: %w", err)
	}

	GlobalConfig = &cfg
	applyConfigDefaults(&cfg)
	return &cfg, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// mergeYAML 深度合并两份 YAML 文档: override 中的键递归覆盖 base 的同名键,
// base 中未被覆盖的键保持不变; 返回合并后的 YAML 字节。
func mergeYAML(base, override []byte) ([]byte, error) {
	var baseMap, overrideMap map[string]any
	if err := yaml.Unmarshal(base, &baseMap); err != nil {
		return nil, fmt.Errorf("unmarshal base yaml: %w", err)
	}
	if err := yaml.Unmarshal(override, &overrideMap); err != nil {
		return nil, fmt.Errorf("unmarshal override yaml: %w", err)
	}
	deepMerge(baseMap, overrideMap)
	return yaml.Marshal(baseMap)
}

// deepMerge 将 src 递归覆盖进 dst: 双方均为 map 时下钻合并, 否则 src 覆盖 dst。
func deepMerge(dst, src map[string]any) {
	for k, v := range src {
		if dstMap, ok := dst[k].(map[string]any); ok {
			if srcMap, ok := v.(map[string]any); ok {
				deepMerge(dstMap, srcMap)
				continue
			}
		}
		dst[k] = v
	}
}

// applyConfigDefaults 各配置段缺省值
func applyConfigDefaults(cfg *Config) {
	if cfg.Jobs.Timezone == "" {
		cfg.Jobs.Timezone = "Asia/Shanghai"
	}
	cfg.Jobs.Params.ApplyDefaults()
}
