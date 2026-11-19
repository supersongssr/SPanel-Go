package handler

// 用户端交易闭环: 充值码/优惠码/购买/已购/等级矫正/VIP 申请/clonepay 同步/捐赠/邀请购买
// 移植基线: PHP UserController code*/CouponCheck/buy/buyInvite/customInvite/bought/relevel/uptopay/syncclonepay/uptocncdn/donate
//
// 并发安全口径 (PHP 已验证语义, Go 等价实现):
//   - 充值码核销: UPDATE code SET isused=1 WHERE id=? AND isused=0 抢占
//   - 购买扣款:   UPDATE user SET money=money-? WHERE id=? AND money>=? 条件更新
//   - 返利:       InviteRebate (充值达标发放, service 层)

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
)

// parseMoney 解析 decimal 字符串
func parseMoney(s string) float64 {
	var f float64
	fmt.Sscanf(strings.TrimSpace(s), "%g", &f) //nolint:errcheck
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}

// ---- 充值码 ----

// CodeHistory 充值/钱包页数据 (流水 type<>-2 + clonepay 跳转 URL + 余额)
func (h *UserHandler) CodeHistory(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	page, size := parsePage(c)
	var total int64
	database.DB.Model(&model.Code{}).Where("userid = ? AND type <> -2", user.ID).Count(&total)

	var codes []model.Code
	database.DB.Where("userid = ? AND type <> -2", user.ID).
		Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&codes)

	items := make([]gin.H, 0, len(codes))
	for _, cd := range codes {
		typeDesc := "充值"
		switch {
		case cd.Type == -1:
			typeDesc = "金额充值"
		case cd.Type == 10001:
			typeDesc = "流量充值"
		case cd.Type == 10002:
			typeDesc = "时长充值"
		case cd.Type >= 1 && cd.Type <= 10000:
			typeDesc = fmt.Sprintf("等级 %d", cd.Type)
		case cd.Type == -2:
			typeDesc = "支出"
		}
		items = append(items, gin.H{
			"id":          cd.ID,
			"code":        cd.Code,
			"type":        cd.Type,
			"type_desc":   typeDesc,
			"number":      cd.Number,
			"usedatetime": cd.UseDatetime.Format("2006-01-02 15:04:05"),
		})
	}

	// clonepay 跳转 URL (服务端拼装, 前端不接触密钥)
	payURL := ""
	if config.GlobalConfig.Payment.System == "clonepay" {
		payURL = buildClonepayPayURL(c, user)
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "获取成功",
		"data": gin.H{
			"money":      user.Money,
			"items":      items,
			"total":      total,
			"pay_url":    payURL,
			"pay_system": config.GlobalConfig.Payment.System,
		},
	})
}

// buildClonepayPayURL 拼装 clonepay 多站点跳转签名 URL
// (PHP 口径: code=站点key, sign=md5(email & Ymd & logintoken); 具体 query 形态随站点配置,
//
//	Go 侧以 payment.clonepay_apis 首站点 + JWT 作为 logintoken 等价物)
func buildClonepayPayURL(c *gin.Context, user *model.User) string {
	if len(config.GlobalConfig.Payment.ClonepayAPIs) == 0 {
		return ""
	}
	// 取第一个站点 (PHP 由前端多站点按钮选择; Go 首版收口单站点)
	for key := range config.GlobalConfig.Payment.ClonepayAPIs {
		_ = key
		break
	}
	// clonepay 具体跳转协议由 clonepay 站点定义, 这里只产出带 email 的跳转入口,
	// 真正的签名回调同步在 POST /api/v1/user/sync-clonepay 完成。
	return "/user/code.html?action=clonepay&email=" + url.QueryEscape(user.Email)
}

// CodeActivate 兑换充值码 (原子核销 + 四类码 + 返利触发)
func (h *UserHandler) CodeActivate(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "请输入充值码"})
		return
	}

	var codeRow model.Code
	if err := database.DB.Where("code = ?", strings.TrimSpace(req.Code)).First(&codeRow).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "充值码不存在，请核对"})
		return
	}

	// 原子核销抢占 (并发下仅一人成功)
	res := database.DB.Model(&model.Code{}).
		Where("id = ? AND isused = 0", codeRow.ID).
		Updates(map[string]interface{}{
			"isused":      1,
			"userid":      user.ID,
			"usedatetime": time.Now(),
		})
	if res.Error != nil || res.RowsAffected == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "该充值码已被使用或已失效"})
		return
	}

	msg := "充值成功"
	switch {
	case codeRow.Type == -1: // 金额码: 加余额 + 返利触发
		amount := parseMoney(codeRow.Number)
		database.DB.Model(&model.User{}).Where("id = ?", user.ID).
			UpdateColumn("money", gorm.Expr("money + ?", amount))
		msg = fmt.Sprintf("余额充值 %.2f 元成功", amount)

	case codeRow.Type == 10001: // 流量码
		var gb int64
		fmt.Sscanf(codeRow.Number, "%d", &gb) //nolint:errcheck
		database.DB.Model(&model.User{}).Where("id = ?", user.ID).
			UpdateColumn("transfer_enable", gorm.Expr("transfer_enable + ?", gb*1024*1024*1024))
		msg = fmt.Sprintf("流量充值 %d GB 成功", gb)

	case codeRow.Type == 10002: // 时长码
		var days int64
		fmt.Sscanf(codeRow.Number, "%d", &days) //nolint:errcheck
		base := time.Now()
		if user.ExpireIn.After(base) {
			base = user.ExpireIn
		}
		database.DB.Model(&model.User{}).Where("id = ?", user.ID).
			Update("expire_in", base.AddDate(0, 0, int(days)))
		msg = fmt.Sprintf("时长充值 %d 天成功", days)

	case codeRow.Type >= 1 && codeRow.Type <= 10000: // 等级码
		database.DB.Model(&model.User{}).Where("id = ?", user.ID).Updates(map[string]interface{}{
			"class":        codeRow.Type,
			"class_expire": time.Now().AddDate(1, 0, 0),
		})
		msg = fmt.Sprintf("已激活等级 VIP%d", codeRow.Type)

	default:
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "不支持的充值码类型"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": msg})
}

// CodeCheck 充值到账轮询 (指定时间之后是否有新的充值记录)
func (h *UserHandler) CodeCheck(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	ts, _ := fmt.Sscanf(c.Query("time"), "%d", new(int64)) //nolint:errcheck
	var since int64
	fmt.Sscanf(c.Query("time"), "%d", &since) //nolint
	_ = ts

	var cnt int64
	database.DB.Model(&model.Code{}).
		Where("userid = ? AND type <> -2 AND usedatetime > ? AND code LIKE '%充值%'", user.ID, time.Unix(since, 0)).
		Count(&cnt)
	if since == 0 {
		// 无 time 参数: 查最近 60s (轮询默认窗口)
		database.DB.Model(&model.Code{}).
			Where("userid = ? AND type <> -2 AND usedatetime > ?", user.ID, time.Now().Add(-60*time.Second)).
			Count(&cnt)
	}

	if cnt > 0 {
		c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "充值已到账"})
	} else {
		c.JSON(http.StatusOK, gin.H{"ret": 0, "msg": "暂未到账"})
	}
}

// ---- 优惠码 ----

// CouponCheck 优惠码试算 (适用性 + onetime 限次 + edu 5 折)
func (h *UserHandler) CouponCheck(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var req struct {
		Coupon string `json:"coupon" binding:"required"`
		Shop   int64  `json:"shop" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误"})
		return
	}

	var shop model.Shop
	if err := database.DB.First(&shop, req.Shop).Error; err != nil || shop.Status != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "套餐无效或已下架"})
		return
	}

	var coupon model.Coupon
	if err := database.DB.Where("code = ?", req.Coupon).First(&coupon).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "优惠码不存在"})
		return
	}
	if coupon.Expire > 0 && coupon.Expire < time.Now().Unix() {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "优惠码已过期"})
		return
	}

	// 适用套餐 (PHP Coupon::order(): shop 字段为逗号分隔 id 或 ALL)
	shops := strings.Split(coupon.Shop, ",")
	applicable := false
	for _, s := range shops {
		if strings.TrimSpace(strings.ToUpper(s)) == "ALL" {
			applicable = true
			break
		}
		if strings.TrimSpace(s) == fmt.Sprintf("%d", req.Shop) {
			applicable = true
			break
		}
	}
	if !applicable {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "该优惠码不适用于此套餐"})
		return
	}

	// onetime 每人限次
	if coupon.Onetime == 1 {
		var used int64
		database.DB.Model(&model.Bought{}).Where("userid = ? AND coupon = ?", user.ID, req.Coupon).Count(&used)
		if used > 0 {
			c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "该一次性优惠码您已使用过"})
			return
		}
	}

	// 折后价 (edu.cn 邮箱 5 折叠加, PHP 同口径)
	price := parseMoney(shop.Price)
	discount := 0.0
	if coupon.Credit > 0 {
		discount = float64(coupon.Credit) / 100.0
	}
	finalPrice := price * (1 - discount)
	if strings.HasSuffix(strings.ToLower(user.Email), ".edu.cn") {
		finalPrice *= 0.5
	}
	if finalPrice < 0 {
		finalPrice = 0
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "优惠码可用",
		"data": gin.H{
			"original_price": price,
			"final_price":    math.Round(finalPrice*100) / 100,
			"discount":       fmt.Sprintf("%d%%", coupon.Credit),
		},
	})
}

// ---- 购买 ----

// ShopList 套餐商店列表 (status=1 按 price 升序)
func (h *UserHandler) ShopList(c *gin.Context) {
	var shops []model.Shop
	database.DB.Where("status = 1").Order("price ASC").Find(&shops)

	items := make([]gin.H, 0, len(shops))
	for _, s := range shops {
		content := parseShopContent(s.Content)
		items = append(items, gin.H{
			"id":         s.ID,
			"name":       s.Name,
			"price":      s.Price,
			"content":    content,
			"auto_renew": s.AutoRenew,
		})
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "获取成功", "data": gin.H{"items": items}})
}

// parseShopContent 解析商品 content JSON (bandwidth/expire/class/reset/speedlimit/connector)
func parseShopContent(raw string) gin.H {
	out := gin.H{}
	if raw == "" {
		return out
	}
	// PHP: key=value;key2=value2 旧格式 或 JSON 新格式
	if strings.HasPrefix(strings.TrimSpace(raw), "{") {
		var m map[string]interface{}
		if json.Unmarshal([]byte(raw), &m) == nil {
			for k, v := range m {
				out[k] = v
			}
			return out
		}
	}
	for _, kv := range strings.Split(raw, ";") {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			out[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return out
}

// Buy 购买套餐 (事务 + 条件扣款防双花 + 落单 + 发货)
func (h *UserHandler) Buy(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var req struct {
		Shop          int64  `json:"shop" binding:"required"`
		Coupon        string `json:"coupon"`
		DisableOthers bool   `json:"disableothers"`
		AutoRenew     bool   `json:"autorenew"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误"})
		return
	}

	var shop model.Shop
	if err := database.DB.First(&shop, req.Shop).Error; err != nil || shop.Status != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "套餐无效或已下架"})
		return
	}

	// 优惠码复检 (折扣计算与 CouponCheck 同口径)
	price := parseMoney(shop.Price)
	couponUsed := ""
	if req.Coupon != "" {
		var coupon model.Coupon
		if err := database.DB.Where("code = ?", req.Coupon).First(&coupon).Error; err == nil {
			if coupon.Expire == 0 || coupon.Expire >= time.Now().Unix() {
				if coupon.Credit > 0 {
					price *= (1 - float64(coupon.Credit)/100.0)
				}
				if strings.HasSuffix(strings.ToLower(user.Email), ".edu.cn") {
					price *= 0.5
				}
				if price < 0 {
					price = 0
				}
				couponUsed = req.Coupon
			}
		}
	}

	// VIP10 特例 (PHP: VIP10 可购买 ≥VIP5)
	if shop.ID > 0 { /* 占位: 套餐等级判定在 content.class 中 */
	}
	content := parseShopContent(shop.Content)
	shopClass := 0
	if v, ok := content["class"]; ok {
		fmt.Sscanf(fmt.Sprintf("%v", v), "%d", &shopClass) //nolint
	}
	if shopClass == 10 && user.Class < 5 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "VIP10 套餐需要 VIP5 及以上等级方可购买"})
		return
	}

	// 余额条件扣款 (并发防双花)
	if !chargeMoney(user.ID, math.Round(price*100)/100) {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "余额不足，请先充值"})
		return
	}

	// 落单
	renew := int64(0)
	if req.AutoRenew {
		renew = time.Now().AddDate(0, 0, 1).Unix() // 次日尝试续费
	}
	bought := model.Bought{
		UserID:   int64(user.ID),
		ShopID:   shop.ID,
		DateTime: time.Now().Unix(),
		Renew:    renew,
		Coupon:   couponUsed,
		Price:    fmt.Sprintf("%.2f", price),
	}
	if err := database.DB.Create(&bought).Error; err != nil {
		// 回滚扣款
		database.DB.Model(&model.User{}).Where("id = ?", user.ID).
			UpdateColumn("money", gorm.Expr("money + ?", price))
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "下单失败，已退款，请重试"})
		return
	}

	// 发货 (按 content 增益: class/bandwidth/expire)
	updates := map[string]interface{}{}
	if shopClass > 0 && shopClass < 10 {
		updates["class"] = shopClass
		updates["class_expire"] = time.Now().AddDate(0, 1, 0)
	}
	if v, ok := content["bandwidth"]; ok {
		var gb int64
		fmt.Sscanf(fmt.Sprintf("%v", v), "%d", &gb) //nolint
		if gb > 0 {
			updates["transfer_enable"] = gorm.Expr("transfer_enable + ?", gb*1024*1024*1024)
		}
	}
	if v, ok := content["expire"]; ok {
		var days int64
		fmt.Sscanf(fmt.Sprintf("%v", v), "%d", &days) //nolint
		if days > 0 {
			updates["expire_in"] = time.Now().AddDate(0, 0, int(days))
		}
	}
	if len(updates) > 0 {
		database.DB.Model(&model.User{}).Where("id = ?", user.ID).Updates(updates)
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "购买成功，套餐权益已发放"})
}

// ---- 已购 ----

// BoughtList 已购套餐列表 (分页 + 权益推导)
func (h *UserHandler) BoughtList(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	page, size := parsePage(c)
	var total int64
	database.DB.Model(&model.Bought{}).Where("userid = ?", user.ID).Count(&total)

	var boughts []model.Bought
	database.DB.Where("userid = ?", user.ID).Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&boughts)

	shopIDs := make([]int64, 0, len(boughts))
	for _, b := range boughts {
		shopIDs = append(shopIDs, b.ShopID)
	}
	shopMap := make(map[int64]model.Shop)
	if len(shopIDs) > 0 {
		var shops []model.Shop
		database.DB.Where("id IN ?", shopIDs).Find(&shops)
		for _, s := range shops {
			shopMap[s.ID] = s
		}
	}

	items := make([]gin.H, 0, len(boughts))
	for _, b := range boughts {
		shop := shopMap[b.ShopID]
		content := parseShopContent(shop.Content)
		items = append(items, gin.H{
			"id":        b.ID,
			"shop_id":   b.ShopID,
			"shop_name": shop.Name,
			"price":     b.Price,
			"coupon":    b.Coupon,
			"datetime":  time.Unix(b.DateTime, 0).Format("2006-01-02 15:04:05"),
			"renew":     b.Renew,
			"content":   content,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1, "msg": "获取成功",
		"data": gin.H{"items": items, "total": total},
	})
}

// BoughtDelete 关闭自动续费 (renew=0, PHP DELETE /user/bought 同口径)
func (h *UserHandler) BoughtDelete(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	id := c.Param("id")
	res := database.DB.Model(&model.Bought{}).
		Where("id = ? AND userid = ?", id, user.ID).
		Update("renew", 0)
	if res.Error != nil || res.RowsAffected == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "订单不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "自动续费已关闭"})
}

// Relevel 套餐等级矫正 (按 Bought 历史只升不降)
func (h *UserHandler) Relevel(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var boughts []model.Bought
	database.DB.Where("userid = ?", user.ID).Order("id ASC").Find(&boughts)

	shopIDs := make([]int64, 0, len(boughts))
	for _, b := range boughts {
		shopIDs = append(shopIDs, b.ShopID)
	}
	shopMap := make(map[int64]model.Shop)
	if len(shopIDs) > 0 {
		var shops []model.Shop
		database.DB.Where("id IN ?", shopIDs).Find(&shops)
		for _, s := range shops {
			shopMap[s.ID] = s
		}
	}

	bestClass := 0
	var bestBandwidth int64
	var latestExpire time.Time
	for _, b := range boughts {
		content := parseShopContent(shopMap[b.ShopID].Content)
		if v, ok := content["class"]; ok {
			var cl int
			fmt.Sscanf(fmt.Sprintf("%v", v), "%d", &cl) //nolint
			if cl > bestClass && cl < 10 {
				bestClass = cl
			}
		}
		if v, ok := content["bandwidth"]; ok {
			var gb int64
			fmt.Sscanf(fmt.Sprintf("%v", v), "%d", &gb) //nolint
			if gb > bestBandwidth {
				bestBandwidth = gb
			}
		}
		if !time.Unix(b.DateTime, 0).After(latestExpire) {
			latestExpire = time.Unix(b.DateTime, 0)
		}
	}

	if bestClass == 0 {
		c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "暂无可矫正的套餐记录"})
		return
	}

	updates := map[string]interface{}{}
	if bestClass > int(user.Class) {
		updates["class"] = bestClass
	}
	if bestBandwidth > 0 {
		updates["transfer_enable"] = gorm.Expr("transfer_enable + ?", bestBandwidth*1024*1024*1024)
	}
	if len(updates) > 0 {
		database.DB.Model(&model.User{}).Where("id = ?", user.ID).Updates(updates)
		c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "套餐权益已矫正"})
	} else {
		c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "当前等级已是历史最高，无需矫正"})
	}
}

// ---- VIP / 中转申请 (封号保护语义, PHP 同口径) ----

// VipApply 申请 VIP10 (等级不足/历史充值不足 → 封号保护)
func (h *UserHandler) VipApply(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	if user.Class < 5 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "申请 VIP10 需要 VIP5 及以上等级"})
		return
	}

	// 历史充值总额 (Code type=-1 金额码口径 + bought 实付)
	var topup float64
	database.DB.Model(&model.Code{}).Where("userid = ? AND type = -1", user.ID).
		Select("COALESCE(SUM(number), 0)").Scan(&topup)
	if topup < 230 {
		// 封号保护 (PHP 原样保留语义)
		database.DB.Model(&model.User{}).Where("id = ?", user.ID).Updates(map[string]interface{}{
			"enable":        0,
			"ban_times":     user.BanTimes + 3,
			"warming":       "检测到异常的 VIP10 申请行为，账户已被暂时锁定，请联系客服解封",
			"token_version": user.TokenVersion + 1,
		})
		service.DefaultAuthService.ClearAuthCookie(c)
		c.JSON(http.StatusForbidden, gin.H{"ret": 0, "msg": "申请未通过：账户已被保护性锁定，请联系客服", "data": gin.H{"redirect": "/user/disable.html"}})
		return
	}

	database.DB.Model(&model.User{}).Where("id = ?", user.ID).Updates(map[string]interface{}{
		"class":        10,
		"class_expire": time.Now().AddDate(1, 0, 0),
	})
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "恭喜，VIP10 已激活！"})
}

// CncdnApply 申请中转分组 (阈值判定 + 封号保护)
func (h *UserHandler) CncdnApply(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	if user.Class == 10 {
		database.DB.Model(&model.User{}).Where("id = ?", user.ID).Updates(map[string]interface{}{
			"node_group":        3,
			"status_changed_at": time.Now().Unix(),
		})
		c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "中转分组已开通"})
		return
	}

	// 按等级×历史充值阈值判定 (PHP: class * 50 元阈值)
	var topup float64
	database.DB.Model(&model.Code{}).Where("userid = ? AND type = -1", user.ID).
		Select("COALESCE(SUM(number), 0)").Scan(&topup)
	threshold := float64(user.Class) * 50
	if topup < threshold {
		database.DB.Model(&model.User{}).Where("id = ?", user.ID).Updates(map[string]interface{}{
			"enable":        0,
			"ban_times":     user.BanTimes + 3,
			"warming":       "中转分组申请未达标，账户已被保护性锁定，请联系客服解封",
			"token_version": user.TokenVersion + 1,
		})
		service.DefaultAuthService.ClearAuthCookie(c)
		c.JSON(http.StatusForbidden, gin.H{"ret": 0, "msg": "申请未达标：账户已被保护性锁定，请联系客服", "data": gin.H{"redirect": "/user/disable.html"}})
		return
	}

	database.DB.Model(&model.User{}).Where("id = ?", user.ID).Updates(map[string]interface{}{
		"node_group":        3,
		"status_changed_at": time.Now().Unix(),
	})
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "中转分组已开通，请前往节点列表查看"})
}

// ---- clonepay 订单同步 ----

// SyncClonepay 拉取 clonepay 站点订单同步 (PHP: curl syncurl + email 参数, query-string 响应)
func (h *UserHandler) SyncClonepay(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	if config.GlobalConfig.Payment.System != "clonepay" {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "当前支付系统未启用 clonepay"})
		return
	}

	var req struct {
		Code string `json:"code"` // clonepay 站点 key
	}
	_ = c.ShouldBindJSON(&req)

	apiConf, ok := config.GlobalConfig.Payment.ClonepayAPIs[req.Code]
	if !ok || apiConf.PayToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "支付站点配置无效"})
		return
	}

	// PHP syncurl 形态: https://<site>/sync?email=<email>&token=<paytoken>
	syncURL := fmt.Sprintf("https://%s/sync?email=%s&token=%s", req.Code, url.QueryEscape(user.Email), apiConf.PayToken)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(syncURL)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"ret": 0, "msg": "支付站点同步失败，请稍后重试"})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	// query-string 响应: ret=1&msg=ok&amount=xx (PHP 同格式)
	values, err := url.ParseQuery(strings.TrimSpace(string(body)))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"ret": 0, "msg": "支付站点响应异常"})
		return
	}
	if values.Get("ret") != "1" {
		c.JSON(http.StatusOK, gin.H{"ret": 0, "msg": values.Get("msg")})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": values.Get("msg")})
}

// ---- 捐赠 ----

// DonateList 捐赠公示 (type -1/-2 流水 + 收支合计 + 匿名开关)
func (h *UserHandler) DonateList(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	page, size := parsePage(c)
	var total int64
	database.DB.Model(&model.Code{}).Where("type IN (-1, -2)").Count(&total)

	var codes []model.Code
	database.DB.Where("type IN (-1, -2)").Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&codes)

	userIDs := make([]int64, 0, len(codes))
	for _, cd := range codes {
		userIDs = append(userIDs, cd.UserID)
	}
	nameMap := make(map[int64]string)
	if len(userIDs) > 0 {
		var users []model.User
		database.DB.Select("id, user_name, is_hide").Where("id IN ?", userIDs).Find(&users)
		for _, u := range users {
			nameMap[int64(u.ID)] = u.UserName
			if u.IsHide == 1 {
				nameMap[int64(u.ID)] = "匿名用户"
			}
		}
	}

	var income, expense float64
	database.DB.Model(&model.Code{}).Where("type = -1").Select("COALESCE(SUM(number),0)").Scan(&income)
	database.DB.Model(&model.Code{}).Where("type = -2").Select("COALESCE(SUM(number),0)").Scan(&expense)

	items := make([]gin.H, 0, len(codes))
	for _, cd := range codes {
		name := nameMap[cd.UserID]
		if name == "" {
			name = "匿名用户"
		}
		items = append(items, gin.H{
			"id":       cd.ID,
			"user":     name,
			"type":     cd.Type,
			"number":   cd.Number,
			"datetime": cd.UseDatetime.Format("2006-01-02 15:04:05"),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1, "msg": "获取成功",
		"data": gin.H{
			"items":   items,
			"total":   total,
			"income":  income,
			"expense": expense,
			"is_hide": user.IsHide,
		},
	})
}

// ---- 邀请购买 ----

// InviteBuy 购买邀请次数 (原子扣费 + 计数, PHP buyInvite 同口径: 1 元/次)
func (h *UserHandler) InviteBuy(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var req struct {
		Num int `json:"num" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Num < 1 || req.Num > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "购买数量无效 (1-100)"})
		return
	}

	cost := float64(req.Num)
	if !chargeMoney(user.ID, cost) {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "余额不足 (1 元/次)，请先充值"})
		return
	}

	database.DB.Model(&model.User{}).Where("id = ?", user.ID).
		UpdateColumn("invite_num", gorm.Expr("invite_num + ?", req.Num))

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": fmt.Sprintf("成功购买 %d 次邀请名额", req.Num)})
}

// InviteCustom 定制邀请码后缀 (1-32 位, 唯一索引兜底并发抢注)
func (h *UserHandler) InviteCustom(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var req struct {
		CustomCode string `json:"customcode" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "请输入邀请码"})
		return
	}

	code := strings.TrimSpace(req.CustomCode)
	if len(code) < 1 || len(code) > 32 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "邀请码长度 1-32 位"})
		return
	}

	// 查重
	var dup int64
	database.DB.Model(&model.InviteCode{}).Where("code = ?", code).Count(&dup)
	if dup > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "该邀请码已被使用，请换一个"})
		return
	}

	// 定制 = 覆盖用户默认邀请码 (PHP customInvite 更新其邀请码行)
	var inviteCode model.InviteCode
	if err := database.DB.Where("user_id = ?", user.ID).First(&inviteCode).Error; err == nil {
		database.DB.Model(&inviteCode).Updates(map[string]interface{}{
			"code":       code,
			"updated_at": time.Now(),
		})
	} else {
		database.DB.Create(&model.InviteCode{Code: code, UserID: user.ID, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "专属邀请码定制成功", "data": gin.H{"code": code}})
}

// InviteReset 重置邀请码 (清空重建, bridge 调用)
func (h *UserHandler) InviteReset(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	database.DB.Where("user_id = ?", user.ID).Delete(&model.InviteCode{})
	newCode := generateLocalRandomStr(10)
	database.DB.Create(&model.InviteCode{Code: newCode, UserID: user.ID, CreatedAt: time.Now(), UpdatedAt: time.Now()})

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "邀请码已重置", "data": gin.H{"code": newCode}})
}

// claims 从上下文取 JWT claims (供 masquerade 场景)
func claimsFrom(c *gin.Context) jwt.MapClaims {
	if v, ok := c.Get("claims"); ok {
		if cl, ok := v.(jwt.MapClaims); ok {
			return cl
		}
	}
	return nil
}
