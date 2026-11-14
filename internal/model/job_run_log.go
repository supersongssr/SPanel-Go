package model

import "time"

// JobRunLog 映射数据库 job_run_log 表 (Go 侧新增, 任务执行台账)。
//
// 用途 (方案 .plans/2026-11-13_后台定时任务与命令移植方案.md §3.5):
//   - 可观测: 每次任务执行一行, 记录触发方式/状态/耗时/错误/摘要;
//   - P3 启动补偿的依据 (对比 last_ok 与表达式判断停机漏跑);
//   - dbclean 顺带按 job_run_log_keep_days 清理 (默认 90 天)。
type JobRunLog struct {
	ID         int64      `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Name       string     `gorm:"column:name;index:idx_job_run_log_name_id,priority:1" json:"name"` // 任务名 (注册表 name)
	Trigger    string     `gorm:"column:trigger_source" json:"trigger_source"`                      // cron | cli | admin
	Status     string     `gorm:"column:status" json:"status"`                                      // running | ok | fail | panic | skipped_lock
	StartedAt  time.Time  `gorm:"column:started_at;type:datetime" json:"started_at"`
	FinishedAt *time.Time `gorm:"column:finished_at;type:datetime" json:"finished_at"`
	DurationMS int64      `gorm:"column:duration_ms" json:"duration_ms"`
	Error      string     `gorm:"column:error;type:text" json:"error"`     // 失败原因 (panic 附堆栈摘要)
	Summary    string     `gorm:"column:summary;type:text" json:"summary"` // 任务输出的 JSON 摘要 (计数/受影响 id 等)
}

// TableName 设置对应的数据表名
func (JobRunLog) TableName() string { return "job_run_log" }

// JobRunLogCreateSQL 建表语句 (幂等; 启动调度器/CLI 首次执行时 Ensure)
const JobRunLogCreateSQL = `
CREATE TABLE IF NOT EXISTS job_run_log (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  name VARCHAR(64) NOT NULL COMMENT '任务名',
  trigger_source VARCHAR(16) NOT NULL COMMENT 'cron|cli|admin',
  status VARCHAR(16) NOT NULL COMMENT 'running|ok|fail|panic|skipped_lock',
  started_at DATETIME NOT NULL,
  finished_at DATETIME NULL DEFAULT NULL,
  duration_ms BIGINT NOT NULL DEFAULT 0,
  error TEXT NULL,
  summary TEXT NULL,
  PRIMARY KEY (id),
  KEY idx_job_run_log_name_id (name, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_general_ci
  COMMENT='SPanel-Go 后台任务执行台账'
`
