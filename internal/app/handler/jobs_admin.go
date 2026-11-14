// jobs_admin.go 任务运维 API (方案 §3.5 P3):
//
//	GET  /api/v1/admin/jobs            注册表 + 生效表达式 + 最近一次运行
//	GET  /api/v1/admin/jobs/:name/log  最近 N 次运行台账
//	POST /api/v1/admin/jobs/:name/run  手动触发 (异步拉起, 复用 runner 锁/台账/告警)
package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"spanel-go/internal/config"
	"spanel-go/internal/jobs"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
)

// RegisterJobsAdminRoutes 任务运维 API (挂载于 admin 鉴权组内)
func RegisterJobsAdminRoutes(adminGroup *gin.RouterGroup) {
	g := adminGroup.Group("/jobs")
	{
		g.GET("", listJobs)
		g.GET("/:name/log", jobRunLog)
		g.POST("/:name/run", runJob)
	}
}

// GET /api/v1/admin/jobs
func listJobs(c *gin.Context) {
	cfg := config.GlobalConfig
	type item struct {
		Name       string           `json:"name"`
		Title      string           `json:"title"`
		Schedule   string           `json:"schedule"` // 生效表达式 ("" = 仅手动)
		Overridden bool             `json:"schedule_overridden"`
		LastRun    *model.JobRunLog `json:"last_run"`
	}
	items := make([]item, 0, 16)
	for _, j := range jobs.All() {
		expr, overridden := jobs.EffectiveSchedule(cfg, j.Name, j.Schedule)
		items = append(items, item{Name: j.Name, Title: j.Title, Schedule: expr, Overridden: overridden,
			LastRun: lastRunOf(j.Name)})
	}
	c.JSON(http.StatusOK, gin.H{"ret": 1, "data": items})
}

// GET /api/v1/admin/jobs/:name/log?limit=20
func jobRunLog(c *gin.Context) {
	name := c.Param("name")
	if jobs.Find(name) == nil {
		c.JSON(http.StatusNotFound, gin.H{"ret": 0, "msg": "unknown job: " + name})
		return
	}
	limit := 20
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 && v <= 100 {
		limit = v
	}
	var logs []model.JobRunLog
	database.DB.Where("name = ?", name).Order("id DESC").Limit(limit).Find(&logs)
	c.JSON(http.StatusOK, gin.H{"ret": 1, "data": logs})
}

// POST /api/v1/admin/jobs/:name/run   body: {"dry_run": true?}
func runJob(c *gin.Context) {
	name := c.Param("name")
	if jobs.Find(name) == nil {
		c.JSON(http.StatusNotFound, gin.H{"ret": 0, "msg": "unknown job: " + name})
		return
	}
	var body struct {
		DryRun bool `json:"dry_run"`
	}
	_ = c.ShouldBindJSON(&body)

	// 异步拉起 (HTTP 立即返回; 锁被占时立刻失败反馈)
	errCh := make(chan error, 1)
	go func() {
		errCh <- jobs.RunOnce(name, jobs.TriggerAdmin, body.DryRun)
	}()
	select {
	case err := <-errCh:
		if err != nil {
			c.JSON(http.StatusConflict, gin.H{"ret": 0, "msg": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "job " + name + " done"})
	case <-time.After(3 * time.Second):
		// 仍在跑 (长任务) — 台账可查
		c.JSON(http.StatusAccepted, gin.H{"ret": 1, "msg": "job " + name + " running, see /jobs/" + name + "/log"})
	}
}

func lastRunOf(name string) *model.JobRunLog {
	if database.DB == nil {
		return nil
	}
	var rec model.JobRunLog
	if err := database.DB.Where("name = ?", name).Order("id DESC").First(&rec).Error; err != nil {
		return nil
	}
	return &rec
}
