// Package jobs 后台任务框架 (方案 .plans/2026-11-13_后台定时任务与命令移植方案.md)。
//
// 一份注册表三种触发: serve 进程内嵌调度器 / CLI 单跑 / (P3) admin API 手动触发。
// 业务编排放本包, 业务规则下沉 internal/service (与 HTTP handler 同源)。
package jobs

import (
	"context"
	"fmt"
	"time"
)

// Job 单个后台任务的注册信息
type Job struct {
	Name     string        // 任务名 (同时是 ./spanel job <name> 的子命令名)
	Title    string        // 中文说明 (job list 展示)
	Schedule string        // cron 5 段表达式; "" = 不排程, 仅手动
	Timeout  time.Duration // 单次执行上限 (0 = 30m 默认)
	Fn       func(ctx context.Context, rc *RunContext) error
}

// RunContext 单次执行的上下文: dry-run 旁路 + 摘要收集 + 输出。
// 任务内所有输出走 Logf (CLI 可见 / serve 落 spanel.log), 结构化计数走 SetSummary。
type RunContext struct {
	DryRun  bool
	Confirm bool // 危险任务实际执行闸 (pruneDeadNodes, 对齐 PHP --confirm)
	summary map[string]any
}

// NewRunContext 构造执行上下文
func NewRunContext(dryRun bool) *RunContext {
	return &RunContext{DryRun: dryRun, summary: map[string]any{}}
}

// SetSummary 记录结构化摘要 (序列化进 job_run_log.summary)
func (rc *RunContext) SetSummary(key string, value any) {
	if rc.summary == nil {
		rc.summary = map[string]any{}
	}
	rc.summary[key] = value
}

// Summary 返回摘要副本 (runner 序列化用)
func (rc *RunContext) Summary() map[string]any { return rc.summary }

// Logf 任务内输出 (对齐 PHP echo: CLI 直接可见, serve 进 spanel.log)
func (rc *RunContext) Logf(format string, args ...any) {
	prefix := ""
	if rc.DryRun {
		prefix = "[DRY-RUN] "
	}
	fmt.Printf(prefix+format+"\n", args...)
}

// ── 注册表 ─────────────────────────────────────────────────────────────────────

var (
	registryOrder []string
	registryMap   = map[string]*Job{}
)

// Register 注册任务 (all.go 显式调用; 重名 panic 防拼写覆盖)
func Register(j *Job) {
	if j == nil || j.Name == "" || j.Fn == nil {
		panic("jobs.Register: invalid job")
	}
	if _, dup := registryMap[j.Name]; dup {
		panic("jobs.Register: duplicate name " + j.Name)
	}
	registryMap[j.Name] = j
	registryOrder = append(registryOrder, j.Name)
}

// All 按注册顺序返回全部任务
func All() []*Job {
	out := make([]*Job, 0, len(registryOrder))
	for _, name := range registryOrder {
		out = append(out, registryMap[name])
	}
	return out
}

// Find 按名查找
func Find(name string) *Job { return registryMap[name] }

// timeoutOf 解析任务超时 (0 → 30m 默认)
func (j *Job) timeoutOf() time.Duration {
	if j.Timeout > 0 {
		return j.Timeout
	}
	return 30 * time.Minute
}

// TriggerSource 触发方式常量 (job_run_log.trigger)
const (
	TriggerCron  = "cron"
	TriggerCLI   = "cli"
	TriggerAdmin = "admin"
)
