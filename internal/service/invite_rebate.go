// Package service 核心业务逻辑
//
// InviteRebate 邀请返利统一服务 (对齐 PHP app/Services/InviteRebate.php v5 方案)。
//
// 两套口径并存, 靠 user.ref_rebate_status 值域互斥解耦:
//
//	0 = 旧口径 (部署前注册) → 只参与比例返利, 永不在充值时发放达标奖励
//	1 = 新口径待达标         → 累计充值越过门槛时一次性发放
//	2 = 新口径已发放         → 幂等闩锁, 永不重复发放
//
// 安全定理: 门槛 R > 2G 且比例返利率 p1+p2 ≤ 50%
// ⟹ 总发放 G + (p1+p2)R < R/2 + R/2 = R, 返利永久小于充值。
//
// 所有写操作经传入的 *gorm.DB (tx 或全局句柄) 执行 —— clonepay 在事务内调用
// OnRechargeTx, 订单插入失败时返利一并回滚, 与 PHP 事务语义一致。
package service

import (
	"fmt"
	"log"
	"math"
	"time"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"

	"gorm.io/gorm"
)

// inviteRebateThresholdMult 达标门槛倍数: 累计充值 R > invite_gift_money × 2 (站长定 2 倍线)
const inviteRebateThresholdMult = 2

// OnRechargeTx 充值金额入账后调用 —— 全站唯一返利发放入口 (事务内)。
// db 传事务句柄则全部写随事务提交/回滚; 传 database.DB 即独立执行。
func OnRechargeTx(db *gorm.DB, userID int64, amount float64) {
	if db == nil || amount <= 0 {
		return
	}

	var user model.User
	if err := db.First(&user, userID).Error; err != nil {
		return
	}

	cfg := config.GlobalConfig
	if cfg == nil {
		return
	}
	p1 := cfg.Invite.CodePayback / 100
	p2 := cfg.Invite.CodePayback2 / 100

	// ---- A. 比例返利 (每笔充值都有, 新旧口径一致) ----
	if user.RefBy != 0 {
		var giftUser model.User
		if err := db.First(&giftUser, user.RefBy).Error; err == nil {
			// 一级返利: 直接邀请人
			l1 := RoundAmount(amount * p1)
			if l1 > 0 {
				insertPayback(db, amount, int64(user.ID), int64(user.RefBy), l1)
				incrementUserMoney(db, int64(giftUser.ID), l1)
			}
			// 二级返利: 邀请人的邀请人 (不存在时跳过, 不中断充值流程)
			if giftUser.RefBy != 0 {
				var giftUser2 model.User
				if err := db.First(&giftUser2, giftUser.RefBy).Error; err == nil {
					l2 := RoundAmount(amount * p2)
					if l2 > 0 {
						insertPayback(db, amount, int64(giftUser.ID), int64(giftUser.RefBy), l2)
						incrementUserMoney(db, int64(giftUser2.ID), l2)
					}
				}
			}
		}
	}

	// ---- B. 达标奖励 (仅新口径 ref_rebate_status=1, 一次性) ----
	giftMoney := cfg.Invite.InviteGiftMoney
	if giftMoney <= 0 {
		return // 奖励已关闭
	}
	// 卫语句①: 旧口径用户永不在此发放 (防双发)
	if user.RefRebateStatus != 1 {
		return
	}
	r := TotalRecharge(db, userID)
	// 门槛①: R > 2×G
	if r <= giftMoney*inviteRebateThresholdMult {
		return
	}
	// 门槛②(防御断言): R×(1−p1−p2) > G —— 宁可不发不可超发
	if r*(1-p1-p2) <= giftMoney {
		return
	}
	// 卫语句②(双保险): 已写过注册奖励记录 (total=-1) 的绝不重复发放
	var cnt int64
	if err := db.Model(&model.Payback{}).
		Where("total = -1 AND userid = ?", userID).Count(&cnt).Error; err == nil && cnt > 0 {
		return
	}
	// 原子抢占 1→2: 并发/重复充值只有一笔能成功 (一次性触发闩锁)
	res := db.Model(&model.User{}).
		Where("id = ? AND ref_rebate_status = 1", userID).
		Update("ref_rebate_status", 2)
	if res.Error != nil || res.RowsAffected == 0 {
		return
	}
	// 发放: 邀请人不存在 (如已被删除) 则放弃发放, 闩锁已置 2 终结
	var inviter model.User
	if err := db.First(&inviter, user.RefBy).Error; err != nil {
		return
	}
	incrementUserMoney(db, int64(inviter.ID), giftMoney)
	if incGB := int64(cfg.Invite.InviteGift * 1024 * 1024 * 1024); incGB > 0 {
		db.Exec("UPDATE user SET transfer_enable = transfer_enable + ? WHERE id = ?", incGB, inviter.ID)
	}
	insertPayback(db, -1, userID, int64(user.RefBy), giftMoney) // total=-1 = 注册奖励 (新口径)
	log.Printf("[InviteRebate] threshold grant user_id=%d ref_by=%d total_recharge=%.2f gift=%.2f",
		userID, user.RefBy, r, giftMoney)
}

// TotalRecharge 累计充值 (严格口径, 全站唯一取数函数)
//
// 仅统计真金白银: type=-1 (金额码/clonepay 订单) 且 isused=1 的 Code 净充值,
// 走 idx_code_userid_type 覆盖索引, 单用户毫秒级。
func TotalRecharge(db *gorm.DB, userID int64) float64 {
	var total struct{ Total float64 }
	if err := db.Table("code").
		Select("COALESCE(SUM(number), 0) AS total").
		Where("userid = ? AND type = -1 AND isused = 1", userID).
		Scan(&total).Error; err != nil {
		log.Printf("[InviteRebate] totalRecharge query failed user_id=%d err=%v", userID, err)
		return 0
	}
	return total.Total
}

// insertPayback 写返利记录 (失败只记日志, 不中断)
func insertPayback(db *gorm.DB, total float64, userID, refBy int64, refGet float64) {
	pb := model.Payback{
		Total:    FormatMoney(total),
		UserID:   userID,
		RefBy:    refBy,
		RefGet:   FormatMoney(refGet),
		DateTime: time.Now().Unix(),
	}
	if err := db.Create(&pb).Error; err != nil {
		log.Printf("[InviteRebate] payback insert failed user=%d ref=%d err=%v", userID, refBy, err)
	}
}

// incrementUserMoney SQL 原子累加余额 (防并发读-改-写覆盖)
func incrementUserMoney(db *gorm.DB, userID int64, amount float64) {
	if amount <= 0 {
		return
	}
	if err := db.Exec("UPDATE user SET money = money + ? WHERE id = ?", amount, userID).Error; err != nil {
		log.Printf("[InviteRebate] money increment failed user=%d err=%v", userID, err)
	}
}

// RoundAmount PHP round(x, 2) 语义 (四舍五入到分)
func RoundAmount(v float64) float64 { return math.Round(v*100) / 100 }

// FormatMoney decimal 列字符串化 (两位小数, 对齐 PHP float → decimal 隐式转换)
func FormatMoney(v float64) string { return fmt.Sprintf("%.2f", RoundAmount(v)) }

// UseDatabase 默认句柄入口 (独立执行, 不在事务内)
func UseDatabase() *gorm.DB { return database.DB }
