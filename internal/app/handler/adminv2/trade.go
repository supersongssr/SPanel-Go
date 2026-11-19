package adminv2

import (
	"math/rand"
	"strconv"
	"strings"
	"time"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

/* 充值码/捐赠/优惠码/邀请返利 — 对齐 PHP Admin\CodeController + AdminController(coupon/invite) */

var codeSortable = map[string]string{
	"id": "code.id", "code": "code.code", "type": "code.type", "number": "code.number",
	"isused": "code.isused", "userid": "code.userid", "usedatetime": "code.usedatetime",
	"user_name": "user.user_name",
}

// ListCodes 充值码/捐赠记录列表 (PHP ajax_code: JOIN user)
func (h *Handler) ListCodes(c *gin.Context) {
	q := ParseListQuery(c, codeSortable, "id", "desc")
	base := func() *gorm.DB {
		db := database.DB.Table("code").
			Select("code.id, code.code, code.type, code.number, code.isused, code.userid, code.usedatetime, user.user_name").
			Joins("LEFT JOIN user ON user.id = code.userid")
		if q.Search != "" {
			like := "%" + q.Search + "%"
			db = db.Where("code.code LIKE ? OR user.user_name LIKE ? OR code.userid LIKE ?", like, like, like)
		}
		if t := c.Query("type"); t != "" {
			db = db.Where("code.type = ?", t)
		}
		return db
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	type row struct {
		ID          int64  `json:"id" gorm:"column:id"`
		Code        string `json:"code" gorm:"column:code"`
		Type        int32  `json:"type" gorm:"column:type"`
		Number      string `json:"number" gorm:"column:number"`
		IsUsed      int32  `json:"isused" gorm:"column:isused"`
		UserID      int64  `json:"userid" gorm:"column:userid"`
		UserName    string `json:"user_name" gorm:"column:user_name"`
		UseDatetime string `json:"usedatetime"`
	}
	var raw []struct {
		row
		RawDT time.Time `gorm:"column:usedatetime"`
	}
	dbq := base().Order(q.OrderBy + " " + q.OrderDir)
	if q.Limit >= 0 {
		dbq = dbq.Offset(q.Offset).Limit(q.Limit)
	}
	if err := dbq.Scan(&raw).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	rows := make([]row, 0, len(raw))
	for _, r := range raw {
		r.row.UseDatetime = fmtDT(r.RawDT)
		rows = append(rows, r.row)
	}
	RespondList(c, rows, total)
}

func genCodeChars(n int) string {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}

// CreateCodes 批量生成充值码 (PHP add: code = timestamp + 8随机位 + 金额*100)
func (h *Handler) CreateCodes(c *gin.Context) {
	var req struct {
		Amount int32   `json:"amount"` // 生成个数
		Number float64 `json:"number"` // 面额 (元)
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Amount <= 0 || req.Amount > 10000 {
		RespondErr(c, "非法请求")
		return
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	tail := strconv.FormatInt(int64(req.Number*100), 10)
	for i := int32(0); i < req.Amount; i++ {
		code := model.Code{
			Code:   ts + genCodeChars(8) + tail,
			Type:   -1,
			Number: strconv.FormatFloat(req.Number, 'f', -1, 64),
		}
		database.DB.Create(&code)
	}
	RespondOK(c, "充值码添加成功")
}

// CreateDonate 添加捐赠/支出记录 (PHP donate_add: type=-2 支出; userid=当前管理员, isused=1)
func (h *Handler) CreateDonate(c *gin.Context) {
	var req struct {
		Amount float64 `json:"amount"`
		Type   int32   `json:"type"` // -2 支出
		Code   string  `json:"code"` // 备注/摘要文本
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Amount == 0 {
		RespondErr(c, "非法请求")
		return
	}
	adminUser := c.MustGet("user").(*model.User)
	code := model.Code{
		Code:        req.Code,
		Type:        req.Type,
		Number:      strconv.FormatFloat(req.Amount, 'f', -1, 64),
		UserID:      int64(adminUser.ID),
		IsUsed:      1,
		UseDatetime: time.Now(),
	}
	if err := database.DB.Create(&code).Error; err != nil {
		RespondErr(c, "添加失败: "+err.Error())
		return
	}
	RespondOK(c, "添加成功")
}

var couponSortable = map[string]string{
	"id": "id", "code": "code", "expire": "expire", "shop": "shop", "credit": "credit", "onetime": "onetime",
}

// ListCoupons 优惠码列表
func (h *Handler) ListCoupons(c *gin.Context) {
	q := ParseListQuery(c, couponSortable, "id", "desc")
	base := func() *gorm.DB {
		db := database.DB.Model(&model.Coupon{})
		if q.Search != "" {
			db = db.Where("code LIKE ?", "%"+q.Search+"%")
		}
		return db
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	var coupons []model.Coupon
	dbq := base().Order(q.OrderBy + " " + q.OrderDir)
	if q.Limit >= 0 {
		dbq = dbq.Offset(q.Offset).Limit(q.Limit)
	}
	if err := dbq.Find(&coupons).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	type item struct {
		ID      int64  `json:"id"`
		Code    string `json:"code"`
		Expire  string `json:"expire"`
		Shop    string `json:"shop"`
		Credit  int32  `json:"credit"`
		Onetime int32  `json:"onetime"`
		Expired bool   `json:"expired"`
	}
	items := make([]item, 0, len(coupons))
	now := time.Now().Unix()
	for _, cp := range coupons {
		items = append(items, item{
			ID: cp.ID, Code: cp.Code,
			Expire: time.Unix(cp.Expire, 0).Format("2006-01-02 15:04:05"),
			Shop:   cp.Shop, Credit: cp.Credit, Onetime: cp.Onetime,
			Expired: cp.Expire < now,
		})
	}
	RespondList(c, items, total)
}

// CreateCoupon 生成优惠码 (PHP addCoupon: 1 定制 / 2 随机 8 位 / 3 前缀+随机 8 位)
func (h *Handler) CreateCoupon(c *gin.Context) {
	var req struct {
		GenerateType int32   `json:"generate_type"`
		Prefix       string  `json:"prefix"`
		Expire       float64 `json:"expire"` // 小时
		Shop         string  `json:"shop"`
		Credit       int32   `json:"credit"`
		Onetime      int32   `json:"onetime"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespondErr(c, "参数有误: "+err.Error())
		return
	}
	finalCode := req.Prefix
	if finalCode == "" && (req.GenerateType == 1 || req.GenerateType == 3) {
		RespondErr(c, "优惠码不能为空")
		return
	}
	if req.GenerateType != 1 {
		for i := 0; i < 1000; i++ { // 冲突重试上限, 撞库概率极低
			var temp string
			if req.GenerateType == 2 {
				temp = genCodeChars(8)
			} else {
				temp = finalCode + genCodeChars(8)
			}
			var count int64
			database.DB.Model(&model.Coupon{}).Where("code = ?", temp).Count(&count)
			if count == 0 {
				finalCode = temp
				break
			}
		}
	}
	coupon := model.Coupon{
		Code:    finalCode,
		Onetime: req.Onetime,
		Expire:  time.Now().Unix() + int64(req.Expire*3600),
		Shop:    req.Shop,
		Credit:  req.Credit,
	}
	if err := database.DB.Create(&coupon).Error; err != nil {
		RespondErr(c, "添加失败: "+err.Error())
		return
	}
	RespondData(c, "优惠码添加成功", gin.H{"code": finalCode})
}

// DeleteCoupon 删除优惠码 (Go 增补, PHP 无)
func (h *Handler) DeleteCoupon(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "优惠码ID无效")
		return
	}
	if err := database.DB.Delete(&model.Coupon{}, id).Error; err != nil {
		RespondErr(c, "删除失败")
		return
	}
	RespondOK(c, "删除成功")
}

var inviteSortable = map[string]string{
	"id": "payback.id", "total": "payback.total", "userid": "payback.userid",
	"ref_by": "payback.ref_by", "ref_get": "payback.ref_get", "datetime": "payback.datetime",
}

// ListInvites 返利流水 (PHP ajax_payback: payback JOIN user×2)
func (h *Handler) ListInvites(c *gin.Context) {
	q := ParseListQuery(c, inviteSortable, "id", "desc")
	base := func() *gorm.DB {
		db := database.DB.Table("payback").
			Select("payback.id, payback.total, payback.userid AS event_user_id, eu.user_name AS event_user_name, payback.ref_by AS ref_user_id, ru.user_name AS ref_user_name, payback.ref_get, payback.datetime").
			Joins("JOIN user eu ON eu.id = payback.userid").
			Joins("JOIN user ru ON ru.id = payback.ref_by")
		if q.Search != "" {
			like := "%" + q.Search + "%"
			db = db.Where("eu.user_name LIKE ? OR ru.user_name LIKE ? OR payback.userid LIKE ?", like, like, like)
		}
		return db
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	type row struct {
		ID            int64  `json:"id" gorm:"column:id"`
		Total         string `json:"total" gorm:"column:total"`
		EventUserID   int64  `json:"event_user_id" gorm:"column:event_user_id"`
		EventUserName string `json:"event_user_name" gorm:"column:event_user_name"`
		RefUserID     int64  `json:"ref_user_id" gorm:"column:ref_user_id"`
		RefUserName   string `json:"ref_user_name" gorm:"column:ref_user_name"`
		RefGet        string `json:"ref_get" gorm:"column:ref_get"`
		DateTime      int64  `json:"datetime" gorm:"column:datetime"`
	}
	var rows []row
	dbq := base().Order(q.OrderBy + " " + q.OrderDir)
	if q.Limit >= 0 {
		dbq = dbq.Offset(q.Offset).Limit(q.Limit)
	}
	if err := dbq.Scan(&rows).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	RespondList(c, rows, total)
}

// AddInviteQuota 加邀请次数 (PHP addInvite; 修 uid=0 null 崩溃 bug: uid 为空/0 → 明确报错)
func (h *Handler) AddInviteQuota(c *gin.Context) {
	var req struct {
		UID string `json:"uid"` // 用户 ID 或邮箱
		Num int32  `json:"num"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Num == 0 {
		RespondErr(c, "非法请求")
		return
	}
	uid := strings.TrimSpace(req.UID)
	if uid == "" || uid == "0" {
		RespondErr(c, "请输入用户 ID 或邮箱")
		return
	}
	var user model.User
	if strings.Contains(uid, "@") {
		database.DB.Where("email = ?", uid).First(&user)
	} else {
		database.DB.Where("id = ?", uid).First(&user)
	}
	if user.ID == 0 {
		RespondErr(c, "邀请次数添加失败，检查用户id或者用户邮箱是否输入正确")
		return
	}
	user.InviteNum += req.Num
	if err := database.DB.Save(&user).Error; err != nil {
		RespondErr(c, "添加失败")
		return
	}
	RespondOK(c, "邀请次数添加成功")
}
