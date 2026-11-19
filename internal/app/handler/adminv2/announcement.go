package adminv2

import (
	"net/mail"
	"strconv"
	"time"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/mailer"
	"spanel-go/internal/pkg/notify"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

/* 公告管理 — 对齐 PHP Admin\AnnController (CRUD + 邮件游标断点续发 ret=2) */

var annSortable = map[string]string{"id": "id", "date": "date"}

// ListAnnouncements 公告列表
func (h *Handler) ListAnnouncements(c *gin.Context) {
	q := ParseListQuery(c, annSortable, "id", "desc")
	base := func() *gorm.DB {
		db := database.DB.Model(&model.Announcement{})
		if q.Search != "" {
			db = db.Where("content LIKE ? OR markdown LIKE ?", "%"+q.Search+"%", "%"+q.Search+"%")
		}
		return db
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	var anns []model.Announcement
	dbq := base().Order(q.OrderBy + " " + q.OrderDir)
	if q.Limit >= 0 {
		dbq = dbq.Offset(q.Offset).Limit(q.Limit)
	}
	if err := dbq.Find(&anns).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	type item struct {
		ID       int32  `json:"id"`
		Date     string `json:"date"`
		Content  string `json:"content"`
		Markdown string `json:"markdown"`
	}
	items := make([]item, 0, len(anns))
	for _, a := range anns {
		items = append(items, item{
			ID: a.ID, Date: fmtDT(a.Date), Content: a.Content, Markdown: a.Markdown,
		})
	}
	RespondList(c, items, total)
}

// GetAnnouncement 编辑页数据
func (h *Handler) GetAnnouncement(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "公告ID无效")
		return
	}
	var a model.Announcement
	if err := database.DB.First(&a, id).Error; err != nil {
		RespondErr(c, "公告不存在")
		return
	}
	RespondData(c, "获取成功", gin.H{
		"id": a.ID, "date": fmtDT(a.Date), "content": a.Content, "markdown": a.Markdown,
	})
}

// telegramBroadcastAll 对齐 PHP TelegramUser::SendMarkdown: 推送给全部绑定 TG 的用户
func telegramBroadcastAll(text string) {
	var chatIDs []int64
	database.DB.Model(&model.User{}).Where("telegram_id > 0").Pluck("telegram_id", &chatIDs)
	for _, id := range chatIDs {
		_ = notify.SendToChat(id, text)
	}
}

// CreateAnnouncement 添加公告 (PHP add: issend=1 分批邮件游标续发, ret=2 未完成)
func (h *Handler) CreateAnnouncement(c *gin.Context) {
	var req struct {
		Content  string `json:"content"`
		Markdown string `json:"markdown"`
		Issend   int32  `json:"issend"`
		Vip      int32  `json:"vip"`
		LastID   int64  `json:"last_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespondErr(c, "参数有误: "+err.Error())
		return
	}
	lastID := req.LastID
	if lastID < 0 {
		lastID = 0
	}

	if lastID == 0 {
		ann := model.Announcement{
			Date:     time.Now(),
			Content:  req.Content,
			Markdown: req.Markdown,
		}
		if err := database.DB.Create(&ann).Error; err != nil {
			RespondErr(c, "添加失败")
			return
		}
		if req.Issend != 1 {
			notify.SendMarkdown("新公告：\n" + req.Markdown)
			go telegramBroadcastAll("新公告：\n" + req.Markdown)
			RespondOK(c, "公告添加成功")
			return
		}
	}

	if req.Issend == 1 {
		const sendPageLimit = 50   // 每批用户数
		const sendTimeBudget = 55e9 // 单请求发信时间预算 (55s, ns)

		var users []model.User
		database.DB.Where("class >= ? AND id > ?", req.Vip, lastID).
			Order("id ASC").Limit(sendPageLimit).Find(&users)

		interrupted := false
		start := time.Now()
		appName := config.GlobalConfig.App.Name
		for _, u := range users {
			lastID = int64(u.ID) // 游标先推进 (无效邮箱跳过同样计入, 不漏发不重发)
			if _, err := mail.ParseAddress(u.Email); err != nil {
				continue
			}
			m := mailer.Mail{
				To:      u.Email,
				Subject: appName + "-公告",
				HTMLBody: "<p>" + req.Content + "</p>",
			}
			_ = mailer.Send(m) // 失败跳过 (PHP try/catch continue 同口径)
			if time.Since(start) >= sendTimeBudget {
				interrupted = true
				break
			}
		}

		if interrupted || len(users) == sendPageLimit {
			c.JSON(200, gin.H{"ret": 2, "msg": strconv.FormatInt(lastID, 10)})
			return
		}
		notify.SendMarkdown("新公告：\n" + req.Markdown)
		go telegramBroadcastAll("新公告：\n" + req.Markdown)
		RespondOK(c, "公告添加成功，邮件发送成功")
		return
	}
	RespondOK(c, "公告添加成功")
}

// UpdateAnnouncement 编辑公告
func (h *Handler) UpdateAnnouncement(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "公告ID无效")
		return
	}
	var a model.Announcement
	if err := database.DB.First(&a, id).Error; err != nil {
		RespondErr(c, "公告不存在")
		return
	}
	var req struct {
		Content  string `json:"content"`
		Markdown string `json:"markdown"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespondErr(c, "参数有误")
		return
	}
	a.Content = req.Content
	a.Markdown = req.Markdown
	a.Date = time.Now()
	if err := database.DB.Save(&a).Error; err != nil {
		RespondErr(c, "修改失败")
		return
	}
	RespondOK(c, "修改成功")
}

// DeleteAnnouncement 删除公告
func (h *Handler) DeleteAnnouncement(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "公告ID无效")
		return
	}
	if err := database.DB.Delete(&model.Announcement{}, id).Error; err != nil {
		RespondErr(c, "删除失败")
		return
	}
	RespondOK(c, "删除成功")
}

