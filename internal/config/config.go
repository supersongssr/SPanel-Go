package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config 全局配置结构体
type Config struct {
	App      AppConfig      `yaml:"app"`
	Security SecurityConfig `yaml:"security"`
	Database DatabaseConfig `yaml:"database"`
	Redis    RedisConfig    `yaml:"redis"`
	JWT      JWTConfig      `yaml:"jwt"`
	Pow      PowConfig      `yaml:"pow"`
}

type AppConfig struct {
	Name  string `yaml:"name"`
	Port  int    `yaml:"port"`
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
