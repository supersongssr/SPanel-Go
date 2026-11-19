package adminv2

import (
	"time"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"

	"github.com/gin-gonic/gin"
)

// GetProfile 个人资料 (对齐 PHP UserController::profile: 返利 15/页 + 5min 在线 IP + 最近 10 登录)
func (h *Handler) GetProfile(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	// 返利流水 (payback.ref_by = 我, 15/页; PHP 按atetime desc)
	q := ParseListQuery(c, map[string]string{
		"id": "id", "userid": "userid", "ref_get": "ref_get", "datetime": "datetime",
	}, "datetime", "desc")
	if q.PageSize == 25 {
		q.PageSize = 15 // PHP 默认 15/页
		if q.Limit >= 0 {
			q.Offset, q.Limit = (q.Page-1)*15, 15
		}
	}

	var total int64
	database.DB.Model(&model.Payback{}).Where("ref_by = ?", user.ID).Count(&total)
	var paybacks []model.Payback
	dbq := database.DB.Where("ref_by = ?", user.ID).Order(q.OrderBy + " " + q.OrderDir)
	if q.Limit >= 0 {
		dbq = dbq.Offset(q.Offset).Limit(q.Limit)
	}
	dbq.Find(&paybacks)

	// 消费者用户名映射
	ids := make([]int64, 0, len(paybacks))
	for _, p := range paybacks {
		ids = append(ids, p.UserID)
	}
	nameMap := map[int64]string{}
	if len(ids) > 0 {
		var rows []struct {
			ID       int32
			UserName string
		}
		database.DB.Model(&model.User{}).Select("id, user_name").Where("id IN ?", ids).Scan(&rows)
		for _, r := range rows {
			nameMap[int64(r.ID)] = r.UserName
		}
	}
	type rebate struct {
		ID       int64  `json:"id"`
		UserID   int64  `json:"userid"`
		UserName string `json:"user_name"`
		Total    string `json:"total"`   // 订单金额
		RefGet   string `json:"ref_get"` // 返利金额
		DateTime string `json:"datetime"`
		Callback int32  `json:"callback"`
	}
	rebates := make([]rebate, 0, len(paybacks))
	for _, p := range paybacks {
		rebates = append(rebates, rebate{
			ID: p.ID, UserID: p.UserID, UserName: nameMap[p.UserID],
			Total: p.Total, RefGet: p.RefGet,
			DateTime: time.Unix(p.DateTime, 0).Format("2006-01-02 15:04:05"),
			Callback: p.Callback,
		})
	}

	// 5 分钟内活跃 IP (排除节点 IP, PHP 同口径)
	var aliveIPs []model.AliveIP
	database.DB.Where("userid = ? AND datetime >= ?", user.ID, time.Now().Unix()-300).
		Order("datetime DESC").Find(&aliveIPs)
	nodeIPSet := map[string]bool{}
	var nodeIPs []string
	database.DB.Model(&model.Node{}).Where("node_ip != ''").Pluck("node_ip", &nodeIPs)
	for _, ip := range nodeIPs {
		nodeIPSet[ip] = true
	}
	seenIP := map[string]bool{}
	onlineIPs := make([]string, 0)
	for _, a := range aliveIPs {
		if nodeIPSet[a.IP] || seenIP[a.IP] {
			continue
		}
		seenIP[a.IP] = true
		onlineIPs = append(onlineIPs, a.IP)
	}

	// 最近 10 次成功登录
	var loginIPs []model.LoginIP
	database.DB.Where("userid = ? AND type = 0", user.ID).
		Order("datetime DESC").Limit(10).Find(&loginIPs)
	type loginIP struct {
		IP       string `json:"ip"`
		DateTime string `json:"datetime"`
	}
	logins := make([]loginIP, 0, len(loginIPs))
	seenLogin := map[string]bool{}
	for _, l := range loginIPs {
		if seenLogin[l.IP] {
			continue
		}
		seenLogin[l.IP] = true
		logins = append(logins, loginIP{IP: l.IP, DateTime: time.Unix(l.DateTime, 0).Format("2006-01-02 15:04:05")})
	}

	RespondData(c, "获取成功", gin.H{
		"user": gin.H{
			"id": user.ID, "user_name": user.UserName, "email": user.Email,
			"class": user.Class, "class_expire": fmtDT(user.ClassExpire),
			"money": user.Money, "invite_num": user.InviteNum, "ref_by": user.RefBy,
		},
		"rebates":      rebates,
		"rebate_total": total,
		"online_ips":   onlineIPs,
		"logins":       logins,
	})
}
