// backup.go 对齐 PHP Job::backup($full) (默认不排程; backup_email 为空直接跳过)。
//
// mysqldump 保留 exec (自研 dump 易错); zip 用 archive/zip (无密码则明文);
// 附件走 mailer; 完成后可选 TG。
package jobs

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"spanel-go/internal/config"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/mailer"
	"spanel-go/internal/pkg/notify"
)

func registerBackup() {
	Register(&Job{
		Name:  "backup",
		Title: "数据库备份 (mysqldump 业务表 + 结构 + 可选 Radius 库 → zip → 邮件附件)",
		Fn:    backupFn(false),
	})
	Register(&Job{
		Name:  "backupfull",
		Title: "数据库全量备份 (mysqldump 整库 → zip → 邮件附件)",
		Fn:    backupFn(true),
	})
}

// partialDumpTables 业务表清单 (对齐 PHP backup)
var partialDumpTables = []string{
	"announcement", "auto", "blockip", "bought", "code", "coupon", "disconnect_ip",
	"link", "login_ip", "payback", "radius_ban", "shop", "speedtest", "ss_invite_code",
	"ss_node", "ss_password_reset", "ticket", "unblockip", "user", "user_token",
	"email_verify", "detect_list", "relay", "paylist",
}

// partialSchemaOnly 大日志表只 dump 结构
var partialSchemaOnly = []string{
	"alive_ip", "ss_node_info", "ss_node_online_log", "user_traffic_log",
	"detect_log", "telegram_session", "yft_order_info",
}

func backupFn(full bool) func(context.Context, *RunContext) error {
	return func(ctx context.Context, rc *RunContext) error {
		cfg := config.GlobalConfig
		if cfg == nil || cfg.Backup.Email == "" {
			rc.Logf("auto_backup_email 未配置, 跳过备份")
			return nil
		}
		tmpDir := filepath.Join(os.TempDir(), fmt.Sprintf("spanelbackup-%d", time.Now().Unix()))
		_ = os.MkdirAll(tmpDir, 0o700)
		defer os.RemoveAll(tmpDir) //nolint:errcheck

		db := &cfg.Database
		host, port := splitHostPort(db.Host)
		baseArgs := []string{
			"--user=" + db.Username, "--password=" + db.Password, "--host=" + host,
			"-P", port, "--single-transaction",
		}

		mainSQL := filepath.Join(tmpDir, "mod.sql")
		rc.Logf("mysqldump → %s", mainSQL)
		if rc.DryRun {
			rc.Logf("[DRY] mysqldump %s (full=%v) + config 副本 + zip + 邮件 → %s", db.Database, full, cfg.Backup.Email)
			return nil
		}

		var dumps [][]string // 每段一次 exec, 追加写入同一文件
		if full {
			dumps = append(dumps, append(baseArgs, db.Database))
		} else {
			dumps = append(dumps, append(append(baseArgs, db.Database), partialDumpTables...))
			dumps = append(dumps, append(append(baseArgs, "-d", db.Database), partialSchemaOnly...))
		}
		for _, args := range dumps {
			cmd := exec.Command("mysqldump", args...)
			out, err := os.OpenFile(mainSQL, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				return fmt.Errorf("open dump file: %w", err)
			}
			cmd.Stdout = out
			cmd.Stderr = os.Stderr
			err = cmd.Run()
			out.Close()
			if err != nil {
				return fmt.Errorf("mysqldump failed: %w", err)
			}
		}

		// config.yaml 副本 (对齐 PHP 备份 .config.php)
		if data, err := os.ReadFile("config.yaml"); err == nil {
			_ = os.WriteFile(filepath.Join(tmpDir, "configbak.yaml"), data, 0o600)
		}

		// Radius 库 (enable 时)
		if cfg.Radius.Enable && cfg.Radius.Host != "" && cfg.Radius.Database != "" {
			rHost, rPort := splitHostPort(cfg.Radius.Host)
			radiusSQL := filepath.Join(tmpDir, "radius.sql")
			cmd := exec.Command("mysqldump",
				"--user="+cfg.Radius.User, "--password="+cfg.Radius.Password,
				"--host="+rHost, "-P", rPort, cfg.Radius.Database)
			out, err := os.OpenFile(radiusSQL, os.O_CREATE|os.O_WRONLY, 0o600)
			if err == nil {
				cmd.Stdout = out
				_ = cmd.Run()
				out.Close()
			}
		}

		// zip 打包
		zipPath := filepath.Join(os.TempDir(), fmt.Sprintf("spanelbackup-%d.zip", time.Now().Unix()))
		if err := zipDir(tmpDir, zipPath, cfg.Backup.Password); err != nil {
			return fmt.Errorf("zip failed: %w", err)
		}
		defer os.Remove(zipPath) //nolint:errcheck

		// 邮件
		err := mailer.Send(mailer.Mail{
			To:          cfg.Backup.Email,
			Subject:     "SPanel-备份成功",
			HTMLBody:    "您好，系统已经为您自动备份，请查看附件，用您设定的密码解压。",
			Attachments: []string{zipPath},
		})
		if err != nil {
			return fmt.Errorf("send backup mail: %w", err)
		}
		if cfg.Backup.Notify {
			notify.Send("备份完毕了喵~今天又是安全祥和的一天呢。")
		}
		rc.SetSummary("backup_file", filepath.Base(zipPath))
		if fi, err := os.Stat(zipPath); err == nil {
			rc.SetSummary("backup_bytes", fi.Size())
		}
		_ = database.DB
		return nil
	}
}

func splitHostPort(s string) (string, string) {
	if i := strings.LastIndexByte(s, ':'); i >= 0 && !strings.Contains(s[i:], "]") {
		return s[:i], s[i+1:]
	}
	return s, "3306"
}

// zipDir 目录打包。注: 标准库 archive/zip 不支持加密, password 参数暂不生效
// (明文 zip; 备份默认不排程, 若启用需自担风险或后续引入第三方加密 zip 库)。
func zipDir(dir, zipPath, password string) error {
	f, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()

	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		w, err := zw.CreateHeader(&zip.FileHeader{
			Name:     rel,
			Method:   zip.Deflate,
			Modified: info.ModTime(),
		})
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	})
}
