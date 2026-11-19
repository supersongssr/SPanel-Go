package adminv2

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/mailer"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

/* 工单管理 — 逐项对齐 PHP Admin\TicketController (列表/查看/回复状态机/nextid 队列/邮件) */

var ticketSortable = map[string]string{
	"id": "ticket.id", "status": "ticket.status", "title": "ticket.title",
	"userid": "ticket.userid", "datetime": "ticket.datetime", "sort": "ticket.sort",
	"user_name": "user.user_name",
}

// nextOpenTicketId 下一张待处理的有效工单 (rootid=0, status!=0, 发起人存在, 按 sort desc)
func nextOpenTicketId(excludeID int64) int64 {
	var nextID int64
	database.DB.Model(&model.Ticket{}).
		Where("rootid = 0 AND id != ? AND status != 0 AND userid IN (SELECT id FROM user)", excludeID).
		Order("sort DESC, datetime ASC, id ASC"). // PHP: 同优先级 FIFO (先提交先处理)
		Limit(1).
		Pluck("id", &nextID)
	return nextID
}

// ListTickets 工单主单列表 (PHP ticket/ajax: JOIN user, rootid=0)
func (h *Handler) ListTickets(c *gin.Context) {
	q := ParseListQuery(c, ticketSortable, "sort", "desc")

	query := func() *gorm.DB {
		db := database.DB.Table("ticket").
			Select("ticket.id, ticket.status, ticket.title, ticket.userid, ticket.datetime, ticket.sort, user.user_name").
			Joins("JOIN user ON user.id = ticket.userid").
			Where("ticket.rootid = 0")
		if q.Search != "" {
			like := "%" + q.Search + "%"
			db = db.Where("ticket.title LIKE ? OR user.user_name LIKE ? OR ticket.userid LIKE ?", like, like, like)
		}
		return db
	}

	var total int64
	if err := query().Count(&total).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	type row struct {
		ID       int64  `json:"id" gorm:"column:id"`
		Status   int32  `json:"status" gorm:"column:status"`
		Title    string `json:"title" gorm:"column:title"`
		UserID   int64  `json:"userid" gorm:"column:userid"`
		DateTime int64  `json:"datetime" gorm:"column:datetime"`
		Sort     int32  `json:"sort" gorm:"column:sort"`
		UserName string `json:"user_name" gorm:"column:user_name"`
	}
	var rows []row
	order := q.OrderBy + " " + q.OrderDir
	if q.OrderBy == "ticket.sort" { // 默认序补齐 PHP DataTables 的次级键 (datetime asc, id asc)
		order += ", ticket.datetime ASC, ticket.id ASC"
	}
	dbq := query().Order(order)
	if q.Limit >= 0 {
		dbq = dbq.Offset(q.Offset).Limit(q.Limit)
	}
	if err := dbq.Scan(&rows).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	RespondList(c, rows, total)
}

// GetTicket 工单详情 (PHP show: 主单+全部回复 按时间升序 + 用户信息卡 + nextid)
func (h *Handler) GetTicket(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "工单ID无效")
		return
	}

	// 坏工单判定 (主单不存在 / 发起人已注销) → 返回 nextid 让前端跳下一张
	var root model.Ticket
	if err := database.DB.Where("id = ? AND rootid = 0", id).First(&root).Error; err != nil {
		RespondData(c, "工单不存在（可能已被清理）", gin.H{"invalid": true, "next_id": nextOpenTicketId(id)})
		return
	}
	var ticketUser model.User
	if err := database.DB.First(&ticketUser, root.UserID).Error; err != nil {
		RespondData(c, "工单发起人已注销", gin.H{"invalid": true, "next_id": nextOpenTicketId(id)})
		return
	}

	var tickets []model.Ticket
	database.DB.Where("id = ? OR rootid = ?", id, id).Order("datetime ASC").Find(&tickets)

	type item struct {
		ID       int64  `json:"id"`
		Title    string `json:"title"`
		Content  string `json:"content"`
		UserID   int64  `json:"userid"`
		IsAdmin  bool   `json:"is_admin"` // 回复者是否管理员 (userid 对应 is_admin)
		Sort     int32  `json:"sort"`
		DateTime string `json:"datetime"`
	}
	items := make([]item, 0, len(tickets))
	for _, t := range tickets {
		isAdmin := false
		if t.RootID != 0 { // 回复楼层: 判断回复者身份
			var replyUser model.User
			if err := database.DB.Select("is_admin").First(&replyUser, t.UserID).Error; err == nil {
				isAdmin = replyUser.IsAdmin == 1
			}
		}
		items = append(items, item{
			ID: t.ID, Title: t.Title, Content: t.Content, UserID: t.UserID,
			IsAdmin: isAdmin, Sort: t.Sort,
			DateTime: time.Unix(t.DateTime, 0).Format("2006-01-02 15:04:05"),
		})
	}

	nextID := nextOpenTicketId(id)
	if nextID == 0 {
		nextID = id // 队列处理完时兜底刷新当前页 (PHP 同口径)
	}

	RespondData(c, "获取成功", gin.H{
		"id":      id,
		"tickets": items,
		"next_id": nextID,
		"user": gin.H{
			"id": ticketUser.ID, "user_name": ticketUser.UserName, "email": ticketUser.Email,
			"class": ticketUser.Class, "node_group": ticketUser.NodeGroup,
			"money": ticketUser.Money, "enable": ticketUser.Enable, "remark": ticketUser.Remark,
			"used_traffic": ticketUser.U + ticketUser.D, "transfer_enable": ticketUser.TransferEnable,
			"expire_in":    fmtDT(ticketUser.ExpireIn),    // 账号过期
			"class_expire": fmtDT(ticketUser.ClassExpire), // 等级有效期
			"last_ss_time": fmtTime(ticketUser.T),         // 最后使用 (t=0 → 从未使用)
		},
	})
}

// ReplyTicket 回复/状态机 (PHP update: status 0关闭 1提交 2跳过 3回复并展示, 响应带 nextid)
func (h *Handler) ReplyTicket(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "工单ID无效")
		return
	}
	var req struct {
		Content string `json:"content"`
		Status  string `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespondErr(c, "参数有误")
		return
	}
	if req.Status == "" {
		RespondErr(c, "请填全")
		return
	}
	// 白名单拦截 (PHP 同: 防乱值搅乱状态机)
	switch req.Status {
	case "0", "1", "2", "3":
	default:
		RespondErr(c, "无效的操作类型")
		return
	}

	var root model.Ticket
	if err := database.DB.Where("id = ? AND rootid = 0", id).First(&root).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{"ret": 0, "msg": "工单不存在（可能已被清理）", "nextid": nextOpenTicketId(id)})
		return
	}
	var ticketUser model.User
	if err := database.DB.First(&ticketUser, root.UserID).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{"ret": 0, "msg": "工单发起人已注销", "nextid": nextOpenTicketId(id)})
		return
	}

	adminUser := c.MustGet("user").(*model.User)

	if req.Status == "1" || req.Status == "3" {
		// 回复动作必须有内容 (去标签判空)
		if strings.TrimSpace(stripTags(req.Content)) == "" {
			RespondErr(c, "请填写回复内容")
			return
		}
		reply := model.Ticket{
			Title:    root.Title,
			Content:  sanitizeXSS(req.Content),
			RootID:   root.ID,
			UserID:   int64(adminUser.ID),
			Sort:     0,
			DateTime: time.Now().Unix(),
		}
		if err := database.DB.Create(&reply).Error; err != nil {
			RespondErr(c, "回复失败: "+err.Error())
			return
		}
	}

	if req.Status == "3" { // 回复并展示 → 邮件通知 (失败不影响回复)
		subject := config.GlobalConfig.App.Name + "-工单被回复"
		baseURL := strings.TrimRight(requestBaseURL(c), "/")
		text := fmt.Sprintf("您好，有人回复了<a href=\"%s/user/ticket/%d/view\">工单</a>，请您查看。", baseURL, root.ID)
		m := mailer.Mail{
			To:       ticketUser.Email,
			Subject:  subject,
			HTMLBody: "<p>" + text + "</p>",
		}
		if err := mailer.Send(m); err != nil {
			fmt.Printf("[Ticket] reply mail failed ticket_id=%d to=%s: %v\n", root.ID, ticketUser.Email, err)
		}
	}

	// 主单状态机 (PHP 精确语义):
	// 2 跳过 → status=1, sort=1 (降优先级沉底); 0 关闭 → status=1, sort=0 (用户侧仍"服务中",
	// 真正完成由用户自行关闭); 1/3 → status=status, sort=0
	switch req.Status {
	case "2":
		root.Status = 1
		root.Sort = 1
	case "0":
		root.Status = 1
		root.Sort = 0
	default:
		root.Status = 1
		if req.Status == "3" {
			root.Status = 3
		}
		root.Sort = 0
	}
	if err := database.DB.Save(&root).Error; err != nil {
		RespondErr(c, "保存失败: "+err.Error())
		return
	}

	// 审计: 管理员工单操作
	uaLog(c, int64(ticketUser.ID), "info", "ticket", "admin.ticket_reply", "管理員回復工单 #"+strconv.FormatInt(root.ID, 10), gin.H{
		"status": req.Status, "ticket_id": root.ID,
	})

	c.JSON(http.StatusOK, gin.H{
		"ret": 1, "msg": "提交成功",
		"nextid": nextOpenTicketId(id), // 完成那一刻实时计算, 避免旧 nextid 过期
	})
}

// requestBaseURL 从请求推导站点根 URL (对齐 public.go baseURLFromRequest)
func requestBaseURL(c *gin.Context) string {
	scheme := "https"
	if c.Request.TLS == nil && c.GetHeader("X-Forwarded-Proto") != "https" {
		scheme = "http"
	}
	return scheme + "://" + c.Request.Host
}

// stripTags 去 HTML 标签 (判空用)
func stripTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sanitizeXSS 基础清洗 (PHP AntiXSS 语义的最小实现: 转义 + 去脚本标签)
func sanitizeXSS(s string) string {
	esc := html.EscapeString(s)
	// 允许基础的换行; 其余一律转义存储 (前端渲染时按纯文本展示)
	return esc
}
