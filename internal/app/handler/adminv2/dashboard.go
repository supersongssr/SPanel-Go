package adminv2

import (
	"net/http"
	"time"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/tools"

	"github.com/gin-gonic/gin"
)

// Dashboard 系统概览 4 卡片 — 逐项对齐 PHP AdminController:index + Analytics
// (总用户 / 30 分钟在线 / 今日流量 / 真实节点)
func (h *Handler) Dashboard(c *gin.Context) {
	var totalUsers int64
	database.DB.Model(&model.User{}).Count(&totalUsers)

	// Analytics::getLiveUserAllIn30Min: enable=1 且 t > now-1800
	var online30m int64
	database.DB.Model(&model.User{}).
		Where("enable = 1 AND t > ?", time.Now().Unix()-1800).
		Count(&online30m)

	// Analytics::getTodayTrafficUsage: SUM(u)+SUM(d)-SUM(last_day_t)
	var sums struct {
		U    int64
		D    int64
		Last int64
	}
	database.DB.Model(&model.User{}).
		Select("COALESCE(SUM(u),0) AS u, COALESCE(SUM(d),0) AS d, COALESCE(SUM(last_day_t),0) AS last").
		Scan(&sums)
	todayBytes := sums.U + sums.D - sums.Last

	// AdminController::index: 真实节点 = is_clone=0 AND type=1
	var realNodes int64
	database.DB.Model(&model.Node{}).Where("is_clone = 0 AND type = 1").Count(&realNodes)

	c.JSON(http.StatusOK, gin.H{
		"ret": 1, "msg": "获取成功",
		"data": gin.H{
			"total_users":         totalUsers,
			"online_30m":          online30m,
			"today_traffic_bytes": todayBytes,
			"today_traffic":       tools.FlowAutoShow(float64(todayBytes)),
			"real_nodes":          realNodes,
		},
	})
}
