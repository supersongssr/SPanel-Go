// Package mailer SMTP 邮件发送 (对齐 PHP app/Services/Mail/Smtp.php + PHPMailer)。
//
// 行为口径: HTML 正文 + 附件; 465 隐式 TLS / 587 STARTTLS (smtp_ssl=true 时);
// 未配置 (host 为空) → 直接返回错误由调用方决定降级; 单条失败不影响业务主流程
// (调用方自行捕获)。
package mailer

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"strings"

	"spanel-go/internal/config"
)

// Mail 单封邮件
type Mail struct {
	To          string
	Subject     string
	HTMLBody    string
	Attachments []string // 文件路径
}

// Enabled SMTP 是否已配置
func Enabled() bool {
	cfg := config.GlobalConfig
	return cfg != nil && cfg.SMTP.Host != ""
}

// Send 发送一封邮件 (HTML + 附件)。
func Send(m Mail) error {
	cfg := config.GlobalConfig
	if cfg == nil || cfg.SMTP.Host == "" {
		return fmt.Errorf("smtp not configured (config.yaml smtp.host)")
	}
	host := cfg.SMTP.Host
	port := cfg.SMTP.Port
	if port == 0 {
		port = 465
	}
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))

	from := cfg.SMTP.Sender
	if from == "" {
		from = cfg.SMTP.Username
	}
	name := cfg.SMTP.Name
	if name == "" {
		name = from
	}

	// 组装 MIME multipart (HTML + 附件)
	msg, err := buildMIME(m, name, from)
	if err != nil {
		return err
	}

	auth := smtp.PlainAuth("", cfg.SMTP.Username, cfg.SMTP.Password, host)

	if cfg.SMTP.SSL && port != 587 {
		// 隐式 TLS (465)
		return sendTLS(addr, host, cfg.SMTP.Username, auth, m.To, msg)
	}
	if cfg.SMTP.SSL && port == 587 {
		// STARTTLS (587)
		return smtp.SendMail(addr, auth, from, []string{m.To}, msg)
	}
	// 明文 (smtp_ssl=false; 不推荐)
	return smtp.SendMail(addr, auth, from, []string{m.To}, msg)
}

func sendTLS(addr, host, user string, auth smtp.Auth, to string, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
	if err != nil {
		return fmt.Errorf("smtp dial tls: %w", err)
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close() //nolint:errcheck

	if err = c.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}
	if err = c.Mail(user); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err = c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err = w.Write(msg); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err = w.Close(); err != nil {
		return fmt.Errorf("smtp close data: %w", err)
	}
	return c.Quit()
}

// buildMIME 组装 multipart/mixed (text/html + 附件)
func buildMIME(m Mail, displayName, from string) ([]byte, error) {
	var b strings.Builder
	b.WriteString("From: " + displayName + " <" + from + ">\r\n")
	b.WriteString("To: " + m.To + "\r\n")
	b.WriteString("Subject: =?UTF-8?B?" + b64(m.Subject) + "?=\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/mixed; boundary=SPANELGO\r\n\r\n")

	// HTML 正文
	b.WriteString("--SPANELGO\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	b.WriteString(wrap76(b64(m.HTMLBody)) + "\r\n")

	// 附件
	for _, path := range m.Attachments {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read attachment %s: %w", path, err)
		}
		name := path
		if i := strings.LastIndexByte(name, '/'); i >= 0 {
			name = name[i+1:]
		}
		b.WriteString("--SPANELGO\r\n")
		b.WriteString("Content-Type: application/octet-stream; name=\"=?UTF-8?B?" + b64(name) + "?=\"\r\n")
		b.WriteString("Content-Transfer-Encoding: base64\r\n")
		b.WriteString("Content-Disposition: attachment; filename=\"=?UTF-8?B?" + b64(name) + "?=\"\r\n\r\n")
		b.WriteString(wrap76(b64(string(data))) + "\r\n")
	}
	b.WriteString("--SPANELGO--\r\n")
	return []byte(b.String()), nil
}
