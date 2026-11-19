package adminv2

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

/* 商品/购买 — 对齐 PHP Admin\ShopController (CRUD/下架语义/中止续费/周期销量) */

var shopSortable = map[string]string{
	"id": "id", "name": "name", "price": "price", "auto_renew": "auto_renew",
	"auto_reset_bandwidth": "auto_reset_bandwidth", "status": "status",
}

// shopContentText PHP Shop::content() 等价: content JSON → 可读文本
func shopContentText(content string) string {
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(content), &m); err != nil {
		return ""
	}
	// PHP content 各分项为字符串数字 (表单原样入库), 需同时兼容 float64 与 string
	g := func(k string) float64 {
		switch v := m[k].(type) {
		case float64:
			return v
		case string:
			f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
			return f
		}
		return 0
	}
	parts := make([]string, 0, 8)
	if v := g("bandwidth"); v != 0 {
		parts = append(parts, "添加流量 "+trimF(v)+" G")
	}
	if v := g("expire"); v != 0 {
		parts = append(parts, "为账号的有效期添加 "+trimF(v)+" 天")
	}
	if v := g("class"); v != 0 {
		parts = append(parts, "为账号升级为等级 "+trimF(v)+" , 有效期 "+trimF(g("class_expire"))+" 天")
	}
	if v := g("reset"); v != 0 {
		parts = append(parts, "在 "+trimF(g("reset_exp"))+" 天内 ，每 "+trimF(v)+" 天重置流量为 "+trimF(g("reset_value"))+" G")
	}
	if v := g("speedlimit"); v != 0 {
		parts = append(parts, "设置用户限速为 "+trimF(v)+" Mbps")
	} else if _, ok := m["speedlimit"]; ok {
		parts = append(parts, "用户端口不限速") // PHP Shop::content(): speedlimit=0 语义
	}
	if v := g("connector"); v != 0 {
		parts = append(parts, "设置用户连接数为 "+trimF(v))
	} else if _, ok := m["connector"]; ok {
		parts = append(parts, "用户IP不限制") // PHP Shop::content(): connector=0 语义
	}
	if v, ok := m["content_extra"].(string); ok && v != "" {
		parts = append(parts, v)
	}
	return strings.Join(parts, " ; ")
}

func trimF(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// ListShops 商品列表 (PHP ajax_shop: 含 content 可读文本 + 周期销量)
func (h *Handler) ListShops(c *gin.Context) {
	q := ParseListQuery(c, shopSortable, "id", "asc")
	base := func() *gorm.DB {
		db := database.DB.Model(&model.Shop{})
		if q.Search != "" {
			like := "%" + q.Search + "%"
			db = db.Where("name LIKE ?", like)
		}
		return db
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	var shops []model.Shop
	dbq := base().Order(q.OrderBy + " " + q.OrderDir)
	if q.Limit >= 0 {
		dbq = dbq.Offset(q.Offset).Limit(q.Limit)
	}
	if err := dbq.Find(&shops).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}

	// 周期销量 (sales_period 默认 30 天; PHP period=expire 时取商品 class_expire)
	type item struct {
		ID                 int64  `json:"id"`
		Name               string `json:"name"`
		Price              string `json:"price"`
		ContentText        string `json:"content_text"`
		Content            string `json:"content"` // 原始 JSON (编辑用)
		AutoRenew          int32  `json:"auto_renew"`
		AutoResetBandwidth int32  `json:"auto_reset_bandwidth"`
		Status             int32  `json:"status"`
		PeriodSales        int64  `json:"period_sales"`
	}
	items := make([]item, 0, len(shops))
	now := time.Now().Unix()
	for _, s := range shops {
		period := int64(30 * 24 * 3600) // PHP sales_period 默认 '30' (固定 30 天窗口)
		var cm map[string]interface{}
		_ = json.Unmarshal([]byte(s.Content), &cm)
		var sales int64
		database.DB.Model(&model.Bought{}).
			Where("shopid = ? AND datetime > ?", s.ID, now-period).
			Count(&sales)
		items = append(items, item{
			ID: s.ID, Name: s.Name, Price: s.Price,
			ContentText: shopContentText(s.Content), Content: s.Content,
			AutoRenew: s.AutoRenew, AutoResetBandwidth: s.AutoResetBandwidth,
			Status: s.Status, PeriodSales: sales,
		})
	}
	RespondList(c, items, total)
}

// shopForm PHP add/update 参数 (content 各分项 → JSON)
type shopForm struct {
	Name               string  `json:"name"`
	Price              string  `json:"price"`
	AutoRenew          int32   `json:"auto_renew"`
	AutoResetBandwidth int32   `json:"auto_reset_bandwidth"`
	Bandwidth          float64 `json:"bandwidth"`
	Expire             float64 `json:"expire"`
	Class              float64 `json:"class"`
	ClassExpire        float64 `json:"class_expire"`
	Reset              float64 `json:"reset"`
	ResetValue         float64 `json:"reset_value"`
	ResetExp           float64 `json:"reset_exp"`
	Speedlimit         float64 `json:"speedlimit"`
	Connector          float64 `json:"connector"`
	ContentExtra       string  `json:"content_extra"`
	Status             int32   `json:"status"`
}

func (f *shopForm) buildContent() string {
	m := map[string]interface{}{}
	if f.Bandwidth != 0 {
		m["bandwidth"] = trimF(f.Bandwidth)
	}
	if f.Expire != 0 {
		m["expire"] = trimF(f.Expire)
	}
	if f.Class != 0 {
		m["class"] = trimF(f.Class)
	}
	if f.ClassExpire != 0 {
		m["class_expire"] = trimF(f.ClassExpire)
	}
	if f.Reset != 0 {
		m["reset"] = trimF(f.Reset)
	}
	if f.ResetValue != 0 {
		m["reset_value"] = trimF(f.ResetValue)
	}
	if f.ResetExp != 0 {
		m["reset_exp"] = trimF(f.ResetExp)
	}
	m["speedlimit"] = trimF(f.Speedlimit)
	m["connector"] = trimF(f.Connector)
	extra := strings.TrimSpace(f.ContentExtra)
	if extra == "" || extra == "-" {
		// PHP create/edit: 空或 '-' 时填默认服务标签, 避免清空用户侧展示
		extra = "check-全球节点分布;check-快速客服响应;check-全平台客户端"
	}
	m["content_extra"] = extra
	b, _ := json.Marshal(m)
	return string(b)
}

// GetShop 编辑页数据 (content JSON 拆开返回)
func (h *Handler) GetShop(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "商品ID无效")
		return
	}
	var s model.Shop
	if err := database.DB.First(&s, id).Error; err != nil {
		RespondErr(c, "商品不存在")
		return
	}
	var m map[string]interface{}
	_ = json.Unmarshal([]byte(s.Content), &m)
	g := func(k string) float64 {
		if v, ok := m[k].(float64); ok {
			return v
		}
		// PHP content 存的是字符串数字
		if v, ok := m[k].(string); ok {
			var f float64
			f, _ = strconv.ParseFloat(v, 64)
			return f
		}
		return 0
	}
	extra := ""
	if v, ok := m["content_extra"].(string); ok {
		extra = v
	}
	RespondData(c, "获取成功", gin.H{
		"id": s.ID, "name": s.Name, "price": s.Price,
		"auto_renew": s.AutoRenew, "auto_reset_bandwidth": s.AutoResetBandwidth,
		"status":    s.Status,
		"bandwidth": g("bandwidth"), "expire": g("expire"),
		"class": g("class"), "class_expire": g("class_expire"),
		"reset": g("reset"), "reset_value": g("reset_value"), "reset_exp": g("reset_exp"),
		"speedlimit": g("speedlimit"), "connector": g("connector"),
		"content_extra": extra,
	})
}

// CreateShop 添加商品
func (h *Handler) CreateShop(c *gin.Context) {
	var f shopForm
	if err := c.ShouldBindJSON(&f); err != nil {
		RespondErr(c, "参数有误: "+err.Error())
		return
	}
	s := model.Shop{
		Name: f.Name, Price: f.Price, Content: f.buildContent(),
		AutoRenew: f.AutoRenew, AutoResetBandwidth: f.AutoResetBandwidth,
		Status: 1,
	}
	if err := database.DB.Create(&s).Error; err != nil {
		RespondErr(c, "添加失败: "+err.Error())
		return
	}
	RespondData(c, "商品添加成功", gin.H{"id": s.ID})
}

// UpdateShop 编辑商品
func (h *Handler) UpdateShop(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "商品ID无效")
		return
	}
	var s model.Shop
	if err := database.DB.First(&s, id).Error; err != nil {
		RespondErr(c, "商品不存在")
		return
	}
	var f shopForm
	if err := c.ShouldBindJSON(&f); err != nil {
		RespondErr(c, "参数有误: "+err.Error())
		return
	}
	s.Name = f.Name
	s.Price = f.Price
	s.Content = f.buildContent()
	s.AutoRenew = f.AutoRenew
	// PHP update(): auto_reset_bandwidth 由 1→0 时, 该商品全部 bought 停止续费
	if s.AutoResetBandwidth == 1 && f.AutoResetBandwidth == 0 {
		database.DB.Model(&model.Bought{}).Where("shopid = ?", s.ID).Update("renew", 0)
	}
	s.AutoResetBandwidth = f.AutoResetBandwidth
	s.Status = 1 // PHP update(): 保存即重新上架 (编辑页无上架开关, 唯一恢复上架途径)
	if err := database.DB.Save(&s).Error; err != nil {
		RespondErr(c, "修改失败: "+err.Error())
		return
	}
	RespondOK(c, "修改成功")
}

// DeleteShop 下架商品 (PHP deleteGet: status=0 + 该商品全部 bought renew=0)
func (h *Handler) DeleteShop(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "商品ID无效")
		return
	}
	var s model.Shop
	if err := database.DB.First(&s, id).Error; err != nil {
		RespondErr(c, "商品不存在")
		return
	}
	s.Status = 0
	if err := database.DB.Save(&s).Error; err != nil {
		RespondErr(c, "下架失败")
		return
	}
	database.DB.Model(&model.Bought{}).Where("shopid = ?", s.ID).Update("renew", 0)
	RespondOK(c, "下架成功")
}

var boughtSortable = map[string]string{
	"id": "bought.id", "datetime": "bought.datetime", "price": "bought.price",
	"userid": "bought.userid", "user_name": "user.user_name", "renew": "bought.renew",
	"shopid":  "bought.shopid",
	"user_id": "user.id", // 页面列 key
}

// ListBought 购买记录 (PHP ajax_bought: JOIN user/shop)
func (h *Handler) ListBought(c *gin.Context) {
	q := ParseListQuery(c, boughtSortable, "id", "desc")
	base := func() *gorm.DB {
		db := database.DB.Table("bought").
			Select("bought.id, bought.datetime, bought.shopid, bought.price, bought.renew, bought.coupon, user.id AS user_id, user.user_name, shop.name AS shop_name, shop.content AS shop_content, shop.auto_reset_bandwidth").
			Joins("JOIN user ON user.id = bought.userid").
			Joins("JOIN shop ON shop.id = bought.shopid")
		if q.Search != "" {
			like := "%" + q.Search + "%"
			db = db.Where("user.user_name LIKE ? OR user.id LIKE ? OR bought.price LIKE ?", like, like, like)
		}
		return db
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	type row struct {
		ID                 int64  `json:"id" gorm:"column:id"`
		DateTime           int64  `json:"datetime" gorm:"column:datetime"`
		ShopID             int64  `json:"shopid" gorm:"column:shopid"`
		ShopName           string `json:"shop_name" gorm:"column:shop_name"`
		ShopContent        string `json:"-" gorm:"column:shop_content"`
		ContentText        string `json:"content_text"`
		Price              string `json:"price" gorm:"column:price"`
		UserID             int64  `json:"user_id" gorm:"column:user_id"`
		UserName           string `json:"user_name" gorm:"column:user_name"`
		Renew              int64  `json:"renew" gorm:"column:renew"`
		Coupon             string `json:"coupon" gorm:"column:coupon"`
		AutoResetBandwidth int32  `json:"auto_reset_bandwidth" gorm:"column:auto_reset_bandwidth"`
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
	for i := range rows {
		rows[i].ContentText = shopContentText(rows[i].ShopContent)
	}
	RespondList(c, rows, total)
}

// DeleteBought 中止续费 (PHP deleteBoughtGet: renew=0)
func (h *Handler) DeleteBought(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "记录ID无效")
		return
	}
	if err := database.DB.Model(&model.Bought{}).Where("id = ?", id).Update("renew", 0).Error; err != nil {
		RespondErr(c, "退订失败")
		return
	}
	RespondOK(c, "退订成功")
}
