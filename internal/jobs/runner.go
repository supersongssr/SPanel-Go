// runner.go 任务执行器: Redis 跨进程互斥锁 + 超时 + panic 恢复 + 台账 + 失败告警;
// 以及 serve 模式的内嵌调度器 (robfig/cron, 表达式 config 可覆盖)。
//
// 互斥锁口径 (方案 §3.4): 主锁 Redis SETNX ssp:joblock:{name}, TTL = Timeout+5m。
// 与 PHP 版共用同一 Redis 实例 → 切换期 PHP cron 忘停也不会双跑 (PHP 侧无此锁,
// 依赖 Go 侧先抢锁后执行; PHP 先跑则 Go 抢锁失败 skipped, 反之 PHP 无锁照跑 ——
// 故切换 runbook 仍要求先停 PHP cron, 此锁为兜底而非替代)。
// Redis 不可用时放弃执行并记 skipped (宁可漏跑不可双跑, 方案 §6.4)。
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/notify"
	"spanel-go/internal/pkg/timex"
)

// ── 台账 ───────────────────────────────────────────────────────────────────────

var ensureTableOnce sync.Once

// EnsureJobRunLogTable 幂等建台账表 (首次执行时)
func EnsureJobRunLogTable() {
	if database.DB == nil {
		return
	}
	ensureTableOnce.Do(func() {
		if err := database.DB.Exec(model.JobRunLogCreateSQL).Error; err != nil {
			log.Printf("[jobs] job_run_log create table failed: %v", err)
		}
	})
}

// insertLedger 写入 running 行, 返回主键
func insertLedger(name, trigger string, startedAt time.Time) int64 {
	rec := model.JobRunLog{Name: name, Trigger: trigger, Status: "running", StartedAt: startedAt}
	if err := database.DB.Create(&rec).Error; err != nil {
		log.Printf("[jobs] ledger insert failed: %v", err)
		return 0
	}
	return rec.ID
}

// finishLedger 回写执行结果
func finishLedger(id int64, status string, finishedAt time.Time, durationMS int64, errMsg, summary string) {
	if id == 0 || database.DB == nil {
		return
	}
	err := database.DB.Model(&model.JobRunLog{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":      status,
			"finished_at": finishedAt,
			"duration_ms": durationMS,
			"error":       errMsg,
			"summary":     summary,
		}).Error
	if err != nil {
		log.Printf("[jobs] ledger update failed id=%d: %v", id, err)
	}
}

// ── Redis 互斥锁 ───────────────────────────────────────────────────────────────

const jobLockKeyPrefix = "ssp:joblock:"

// releaseLockLua 比对 token 再删, 防止误删他人锁 (自己的锁已过期被抢后)
const releaseLockLua = `if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("del", KEYS[1]) else return 0 end`

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// acquireJobLock 抢锁; 返回 token 与是否成功。Redis 不可用 → ok=false, unavailable=true。
func acquireJobLock(ctx context.Context, name string, ttl time.Duration) (token string, ok, unavailable bool) {
	rdb := database.Redis
	if rdb == nil {
		return "", false, true
	}
	token = randomToken()
	got, err := rdb.SetNX(ctx, jobLockKeyPrefix+name, token, ttl).Result()
	if err != nil {
		log.Printf("[jobs] lock %s acquire error: %v", name, err)
		return "", false, true
	}
	return token, got, false
}

func releaseJobLock(ctx context.Context, name, token string) {
	if database.Redis == nil || token == "" {
		return
	}
	if err := database.Redis.Eval(ctx, releaseLockLua, []string{jobLockKeyPrefix + name}, token).Err(); err != nil {
		log.Printf("[jobs] lock %s release error: %v", name, err)
	}
}

// ── 单次执行 ───────────────────────────────────────────────────────────────────

// RunOpts 单次执行选项
type RunOpts struct {
	Trigger string // cron | cli | admin
	DryRun  bool
	Confirm bool // 危险任务实际执行闸 (pruneDeadNodes --confirm)
}

// RunOnce 执行一个注册任务 (cron / CLI / admin 共用入口, 兼容封装)。
// 返回 error 仅用于 CLI 退出码; cron 侧只记日志。
func RunOnce(name, trigger string, dryRun bool) error {
	return RunOnceOpts(name, RunOpts{Trigger: trigger, DryRun: dryRun})
}

// RunOnceOpts 执行一个注册任务 (完整选项版)。
func RunOnceOpts(name string, o RunOpts) error {
	j := Find(name)
	if j == nil {
		return fmt.Errorf("unknown job: %s (see `spanel job list`)", name)
	}
	if database.DB == nil {
		return fmt.Errorf("database not initialized, cannot run job %s", name)
	}
	EnsureJobRunLogTable()

	timeout := j.timeoutOf()
	prefix := fmt.Sprintf("[jobs] %s (%s%s%s)", name, o.Trigger,
		map[bool]string{true: ",dry-run", false: ""}[o.DryRun],
		map[bool]string{true: ",confirm", false: ""}[o.Confirm])

	// 1. 跨进程互斥锁 (宁可漏跑不可双跑)
	lockCtx, lockCancel := context.WithTimeout(context.Background(), 5*time.Second)
	token, locked, redisDown := acquireJobLock(lockCtx, name, timeout+5*time.Minute)
	lockCancel()
	if redisDown {
		log.Printf("%s SKIP: redis unavailable (fail-safe, 宁可漏跑不可双跑)", prefix)
		finishLedger(insertLedger(name, o.Trigger, timex.Now()), "skipped_lock", timex.Now(), 0,
			"redis unavailable, skipped to avoid double-run", "")
		notifySkipThrottled(name, "任务 "+name+" 因 Redis 不可用放弃本轮执行 (防双跑兜底), 请检查 Redis。")
		return fmt.Errorf("job %s skipped: redis unavailable", name)
	}
	if !locked {
		log.Printf("%s SKIP: another instance holds the lock", prefix)
		finishLedger(insertLedger(name, o.Trigger, timex.Now()), "skipped_lock", timex.Now(), 0,
			"lock held by another instance", "")
		return fmt.Errorf("job %s skipped: lock held", name)
	}
	defer releaseJobLock(context.Background(), name, token)

	// 2. 台账 + 执行 (超时 ctx / panic 恢复 / defer 计时)
	started := timex.Now()
	ledgerID := insertLedger(name, o.Trigger, started)
	log.Printf("%s start", prefix)

	rc := &RunContext{DryRun: o.DryRun, Confirm: o.Confirm, summary: map[string]any{}}
	var runErr error
	var panicVal any
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicVal = r
				log.Printf("%s PANIC: %v\n%s", prefix, r, debug.Stack())
			}
		}()
		runErr = j.Fn(ctx, rc)
	}()
	cancel()

	finished := timex.Now()
	durationMS := finished.Sub(started).Milliseconds()

	summaryJSON := ""
	if s := rc.Summary(); len(s) > 0 {
		if b, err := json.Marshal(s); err == nil {
			summaryJSON = string(b)
		}
	}

	status := "ok"
	errMsg := ""
	switch {
	case panicVal != nil:
		status, errMsg = "panic", fmt.Sprintf("panic: %v", panicVal)
	case runErr != nil:
		status, errMsg = "fail", runErr.Error()
	}
	if ctx.Err() == context.DeadlineExceeded && status == "ok" {
		// 任务返回 nil 但超时先到 (未遵守 ctx), 按超时记录
		status, errMsg = "fail", "context deadline exceeded (job ignored ctx?)"
	}

	finishLedger(ledgerID, status, finished, durationMS, errMsg, summaryJSON)
	log.Printf("%s done status=%s duration=%s %s", prefix, status, finished.Sub(started).Round(time.Millisecond), errMsg)

	if status != "ok" {
		notify.Error("后台任务失败",
			fmt.Sprintf("任务 %s (%s) 执行失败: %s\n耗时 %s", name, o.Trigger, firstLine(errMsg), finished.Sub(started).Round(time.Millisecond)))
	}
	if status == "ok" {
		return nil
	}
	if panicVal != nil {
		return fmt.Errorf("job %s panic: %v", name, panicVal)
	}
	return fmt.Errorf("job %s failed: %s", name, errMsg)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ── 内嵌调度器 (serve 模式) ──────────────────────────────────────────────────

var (
	schedulerMu sync.Mutex
	scheduler   *cron.Cron
	schedWG     sync.WaitGroup
)

// entryInfo 排程条目 (启动日志 + 补偿用)
type entryInfo struct {
	name, expr string
	id         cron.EntryID
	j          *Job
}

// StartScheduler 按 config 启动内嵌调度器 (bootstrap 在 DB/Redis 就绪后调用)。
// 表达式优先级: cfg.Jobs.Schedules[name] (存在即覆盖, 空串=停用) > 注册表默认。
func StartScheduler(cfg *config.Config) {
	schedulerMu.Lock()
	defer schedulerMu.Unlock()
	if scheduler != nil {
		return
	}

	loc, err := time.LoadLocation(cfg.Jobs.Timezone)
	if err != nil {
		log.Printf("[jobs] unknown timezone %q, fallback Asia/Shanghai: %v", cfg.Jobs.Timezone, err)
		loc = timex.Loc
	}

	c := cron.New(cron.WithLocation(loc))

	var entries []entryInfo
	for _, j := range All() {
		schedule := j.Schedule
		if cfg != nil {
			if override, exists := cfg.Jobs.Schedules[j.Name]; exists {
				schedule = override
			}
		}
		if schedule == "" {
			continue // 不排程, 仅手动
		}
		jobName := j.Name
		e := entryInfo{name: jobName, expr: schedule, j: j}
		id, err := c.AddFunc(schedule, func() {
			schedWG.Add(1)
			defer schedWG.Done()
			// 进程内重叠由 Redis 锁拦下 (RunOnce → skipped_lock); 台账留痕可观测
			if err := RunOnce(jobName, TriggerCron, false); err != nil {
				log.Printf("[jobs] cron run %s: %v", jobName, err)
			}
		})
		if err != nil {
			log.Printf("[jobs] schedule %s invalid expr %q: %v (skip)", jobName, schedule, err)
			continue
		}
		e.id = id
		entries = append(entries, e)
	}

	if len(entries) == 0 {
		log.Println("[jobs] no scheduled jobs (all manual-only), scheduler idle")
		scheduler = c
		return
	}

	c.Start()
	scheduler = c
	// Start 后 Next 才被计算, 逐条打印排程表
	for _, e := range entries {
		log.Printf("[jobs] scheduled %-16s %-16s next=%s  %s",
			e.name, e.expr, c.Entry(e.id).Next.In(loc).Format("2006-01-02 15:04:05"), e.j.Title)
	}
	log.Printf("[jobs] scheduler started, %d entries, timezone=%s", len(entries), loc.String())

	// 启动补偿 (可选, jobs.catchup): 停机跨过触发点 (上次成功后有排程点已过) → 补跑一次
	if cfg != nil && cfg.Jobs.Catchup {
		go catchupMissed(c, entries, loc)
	}
}

// catchupMissed 对比台账 last_ok 与排程: schedule.Next(lastOK) <= now 即漏跑 → 补跑一次
// (trigger=cron 正常路径, 复用锁/台账; 补跑后正常排程继续)。
func catchupMissed(c *cron.Cron, entries []entryInfo, loc *time.Location) {
	if database.DB == nil {
		return
	}
	EnsureJobRunLogTable()
	now := timex.Now()
	for _, e := range entries {
		entry := c.Entry(e.id)
		if entry.Schedule == nil {
			continue
		}
		var lastOK model.JobRunLog
		err := database.DB.Where("name = ? AND status = ?", e.name, "ok").
			Order("id DESC").First(&lastOK).Error
		if err != nil { // 从未成功跑过 (新任务) → 交给正常排程, 不补
			continue
		}
		lastOKTime := lastOK.StartedAt.In(loc)
		nextAfter := entry.Schedule.Next(lastOKTime)
		if nextAfter.After(now) {
			continue // 上次成功之后无已过的触发点
		}
		log.Printf("[jobs] catchup: %s 停机期间漏跑 (last_ok=%s), 补跑一次",
			e.name, lastOKTime.Format("2006-01-02 15:04:05"))
		schedWG.Add(1)
		go func(name string) {
			defer schedWG.Done()
			if err := RunOnce(name, "catchup", false); err != nil {
				log.Printf("[jobs] catchup run %s: %v", name, err)
			}
		}(e.name)
	}
}

// skipNotifyThrottle 进程内告警节流 (Redis 不可用场景 DedupNotify 失效, 防TG刷屏)
var skipNotifyThrottle = struct {
	sync.Mutex
	last map[string]time.Time
}{last: map[string]time.Time{}}

// notifySkipThrottled 1 小时内同名任务只告警一次
func notifySkipThrottled(name, reason string) {
	skipNotifyThrottle.Lock()
	if t, ok := skipNotifyThrottle.last[name]; ok && time.Since(t) < time.Hour {
		skipNotifyThrottle.Unlock()
		return
	}
	skipNotifyThrottle.last[name] = time.Now()
	skipNotifyThrottle.Unlock()
	notify.Error("后台任务跳过", reason)
}

// StopScheduler 停止调度器并等待在跑任务收尾 (上限 wait)。
func StopScheduler(wait time.Duration) {
	schedulerMu.Lock()
	c := scheduler
	scheduler = nil
	schedulerMu.Unlock()
	if c == nil {
		return
	}
	ctx := c.Stop() // 停止新触发, 返回等待在跑 entry 的 ctx

	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		schedWG.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Println("[jobs] scheduler stopped, all jobs finished")
	case <-time.After(wait):
		log.Printf("[jobs] scheduler stop timeout after %s, %d job(s) still running", wait, len(c.Entries()))
	}
}
