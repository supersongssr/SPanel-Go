package handler

import (
	"crypto/md5"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/netutil"
	"spanel-go/internal/pkg/timex"
	"spanel-go/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ClonepayHandler clonepay 支付同步回调 (对齐 PHP ApiController::clonepay, sdo 2022-03-18)
//
// 响应语义完全复刻 PHP:
//   - 所有 exit() 路径   → HTTP 200 空 body (静默拒绝, 不给攻击者任何信息)
//   - 业务错误           → HTTP 200 body "&error=xxx"
//   - 成功               → HTTP 200 空 body
type ClonepayHandler struct{}

var DefaultClonepayHandler = &ClonepayHandler{}

// silentExit 静默拒绝 (对齐 PHP exit(): 200 空 body)
func silentExit(c *gin.Context) {
	c.String(http.StatusOK, "")
}

// Callback POST /api/clonepay
func (h *ClonepayHandler) Callback(c *gin.Context) {
	// param 取 GET/POST 参数 (对齐 Slim getParam)
	param := func(name string) string {
		if v := c.Query(name); v != "" {
			return v
		}
		return c.PostForm(name)
	}

	// 1. 验证是否开启 clonepay
	cfg := config.GlobalConfig
	if cfg == nil || cfg.Payment.System != "clonepay" {
		silentExit(c)
		return
	}

	// 2. clonepay 来源站点判定
	from := param("from")
	if from == "" {
		silentExit(c)
		return
	}
	apiConf, ok := cfg.Payment.ClonepayAPIs[from]
	if !ok {
		silentExit(c)
		return
	}

	// 3. 验证 IP: 直连地址 (对齐 PHP REMOTE_ADDR, 绝不信任 XFF)
	ip := netutil.RemoteIP(c.Request)
	if ip != apiConf.SafeIP && ip != apiConf.SafeIPV6 {
		silentExit(c)
		return
	}

	// 4. 验证签名: md5(salt & Ymd & order & money原文 & paytoken)
	//    注意 money 用原始字符串参与拼接 (非归一化 float)
	order := param("order")
	moneyRaw := param("money")
	signStr := param("salt") + "&" + timex.Ymd() + "&" + order + "&" + moneyRaw + "&" + apiConf.PayToken
	want := fmt.Sprintf("%x", md5.Sum([]byte(signStr)))
	if want != param("sign") {
		silentExit(c)
		return
	}

	// 5. 金额正值校验: 零/负金额拒绝入账 (防可信回调方异常数据倒扣用户余额)
	money, err := strconv.ParseFloat(moneyRaw, 64)
	if err != nil || money <= 0 {
		c.String(http.StatusOK, "&error=非法金额")
		return
	}

	// 6. 验证用户
	email := param("email")
	if email == "" {
		silentExit(c)
		return
	}
	if database.DB == nil {
		silentExit(c)
		return
	}
	var user model.User
	if err := database.DB.Where("email = ?", email).First(&user).Error; err != nil {
		silentExit(c)
		return
	}

	// 7. 幂等: 验证是否已存在订单号 (旧格式 "cp-{order}" 历史兼容 + 新格式 "cp-{from}{order}")
	var cnt int64
	database.DB.Model(&model.Code{}).Where("code = ?", "cp-"+order).Count(&cnt)
	if cnt > 0 {
		c.String(http.StatusOK, "&error=订单已存在")
		return
	}
	newOrder := "cp-" + from + order
	database.DB.Model(&model.Code{}).Where("code = ?", newOrder).Count(&cnt)
	if cnt > 0 {
		c.String(http.StatusOK, "&error=订单已存在")
		return
	}

	// 8. 事务: 记录订单 + 入账 + 返利 (并发安全由 uk_code_code 唯一索引兜底)
	tsUnix, _ := strconv.ParseInt(param("time"), 10, 64)
	if tsUnix <= 0 {
		tsUnix = time.Now().Unix()
	}

	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		codeRow := model.Code{
			Code:        newOrder,
			Type:        -1,
			Number:      service.FormatMoney(money),
			IsUsed:      1,
			UserID:      int64(user.ID),
			UseDatetime: time.Unix(tsUnix, 0),
		}
		if err := tx.Create(&codeRow).Error; err != nil {
			return err // 含唯一索引冲突
		}
		// 给用户加上余额 (原子 increment, 防并发读-改-写覆盖)
		if err := tx.Exec("UPDATE user SET money = money + ? WHERE id = ?", money, user.ID).Error; err != nil {
			return err
		}
		// 返利走统一服务入口 (v5 方案; 幂等由唯一索引 + 事务保证; 随本事务提交/回滚)
		service.OnRechargeTx(tx, int64(user.ID), money)
		return nil
	})

	if txErr != nil {
		// 唯一索引冲突 = 并发重复订单, 幂等返回 (与前置检查同口径)
		if isDuplicateEntry(txErr) {
			c.String(http.StatusOK, "&error=订单已存在")
			return
		}
		log.Printf("[clonepay] transaction failed user=%d err=%v", user.ID, txErr)
		silentExit(c)
		return
	}
	// 成功: 空 body (PHP 无任何输出)
	silentExit(c)
}

func isDuplicateEntry(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "1062") || strings.Contains(err.Error(), "Duplicate entry"))
}
