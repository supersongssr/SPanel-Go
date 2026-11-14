// Package notify 管理通知最小实现（对齐 PHP app/Utils/Telegram.php 管理门面 +
// NotifyLogger 的 Redis 去重语义）。
//
// 行为口径：
//   - enable=false 或 token/chatid 未配置 → 静默 no-op（只记日志）；
//   - 发送走 Bot API sendMessage（admin token 优先，回退普通 token）；
//   - 单条失败只记日志，绝不影响业务主流程；
//   - DedupNotify 提供 (key, TTL) Redis 去重，Redis 故障时跳过通知（防刷屏优先）。
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"spanel-go/internal/config"
	"spanel-go/internal/pkg/database"
)

const sendTimeout = 10 * time.Second

var httpCLi = &http.Client{Timeout: sendTimeout}

// Enabled 通知总开关是否打开且基础配置可用
func Enabled() bool {
	cfg := config.GlobalConfig
	if cfg == nil || !cfg.Telegram.Enable {
		return false
	}
	return resolveAdminToken() != "" && chatIDConfigured(resolveAdminChatID())
}

// Send 发送纯文本管理通知（对齐 Telegram::Send）
func Send(text string) { dispatch(text, "") }

// SendMarkdown 发送 Markdown 管理通知（对齐 Telegram::SendMarkdown）
func SendMarkdown(text string) { dispatch(text, "Markdown") }

// Warning 发送 warning 级通知（对齐 NotifyLogger::warning：标题 + 正文拼接）
func Warning(title, message string) {
	Send("⚠️ " + title + "\n" + message)
}

// Error 发送 error 级通知（对齐 NotifyLogger::error：标题 + 正文拼接）
func Error(title, message string) {
	Send("🚨 " + title + "\n" + message)
}

// Info 发送 info 级通知（对齐 NotifyLogger::info）
func Info(title, message string) {
	Send("ℹ️ " + title + "\n" + message)
}

// DedupNotify 带去重的通知：Redis SETNX(key, TTL) 成功才发送。
// Redis 不可用时跳过本次通知（fail-safe，防高频上报刷爆 TG，对齐 PHP notifyDeprecatedEndpoint）。
func DedupNotify(key string, ttl time.Duration, send func()) {
	rdb := database.Redis
	if rdb == nil {
		log.Printf("[notify] redis unavailable, skip dedup notify key=%s", key)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ok, err := rdb.SetNX(ctx, key, "1", ttl).Result()
	if err != nil {
		log.Printf("[notify] redis dedup failed, skip notify key=%s err=%v", key, err)
		return
	}
	if !ok {
		return // TTL 内已通知过
	}
	send()
}

func dispatch(text, parseMode string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[notify] panic recovered: %v", r)
		}
	}()

	if !Enabled() {
		return
	}

	token := resolveAdminToken()
	chatID := resolveAdminChatID()
	topic := int64(0)
	if config.GlobalConfig != nil {
		topic = config.GlobalConfig.Telegram.AdminTopicID
	}

	payload := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
	}
	if topic > 0 {
		payload["message_thread_id"] = topic
	}
	body, _ := json.Marshal(payload)

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		log.Printf("[notify] build request failed: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpCLi.Do(req)
	if err != nil {
		log.Printf("[notify] send failed: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("[notify] send got http %d", resp.StatusCode)
	}
}

func resolveAdminToken() string {
	cfg := config.GlobalConfig
	if cfg == nil {
		return ""
	}
	if cfg.Telegram.AdminToken != "" {
		return cfg.Telegram.AdminToken
	}
	return cfg.Telegram.Token
}

func resolveAdminChatID() string {
	cfg := config.GlobalConfig
	if cfg == nil {
		return ""
	}
	return cfg.Telegram.AdminChatID
}

func chatIDConfigured(id string) bool {
	return id != "" && id != "x" // 'x' 为 PHP 历史占位值，视为未配置
}

// SendToChat 发送纯文本到指定 chat (sendDailyUsageByTG 等用户私发场景)。
// 返回错误供调用方统计失败数 (与管理通知的 fire-and-forget 语义不同)。
func SendToChat(chatID int64, text string) error {
	cfg := config.GlobalConfig
	if cfg == nil || !cfg.Telegram.Enable || cfg.Telegram.Token == "" {
		return fmt.Errorf("telegram not enabled")
	}
	payload := map[string]any{"chat_id": chatID, "text": text}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", cfg.Telegram.Token)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpCLi.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sendMessage http %d", resp.StatusCode)
	}
	return nil
}
