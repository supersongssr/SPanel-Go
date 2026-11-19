package adminv2

import (
	"net/http"
	"strconv"
	"time"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/radius"
	"spanel-go/internal/service"

	"github.com/gin-gonic/gin"
)

// ==================== 用户编辑 (GET) ====================

// GetUser 编辑页数据: 28 可编辑字段 + 页头统计 (对齐 PHP Admin\UserController::edit)
func (h *Handler) GetUser(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "用户ID无效")
		return
	}
	var user model.User
	if err := database.DB.First(&user, id).Error; err != nil {
		RespondErr(c, "用户不存在")
		return
	}

	// 累计充值 (v5 口径: code type=-1 isused=1, 与返利判定同源)
	var topUp string
	database.DB.Model(&model.Code{}).
		Select("COALESCE(SUM(number),0)").
		Where("userid = ? AND type = -1 AND isused = 1", user.ID).
		Scan(&topUp)
	if topUp == "" {
		topUp = "0.00"
	}

	gb := 1024.0 * 1024 * 1024
	RespondData(c, "获取成功", gin.H{
		// 编辑表单字段 (与 PUT 参数一一对应)
		"id": user.ID, "email": user.Email, "remark": user.Remark,
		"enable": user.Enable, "ga_enable": user.GaEnable, "money": user.Money,
		"is_multi_user": user.IsMultiUser, "port": user.Port, "passwd": user.Passwd,
		"method": user.Method, "protocol": user.Protocol, "protocol_param": user.ProtocolParam,
		"obfs": user.Obfs, "obfs_param": user.ObfsParam,
		"transfer_enable_gb": float64(user.TransferEnable) / gb, // 表单单位 GB (PHP enableTrafficInGB)
		"transfer_limit":     user.TransferLimit,
		"auto_reset_day":     user.AutoResetDay, "auto_reset_bandwidth": user.AutoResetBandwidth,
		"invite_num": user.InviteNum, "ref_by": user.RefBy,
		"node_group": user.NodeGroup, "class": user.Class,
		"class_expire": fmtDT(user.ClassExpire), "expire_in": fmtDT(user.ExpireIn),
		"node_speedlimit": user.NodeSpeedlimit, "node_connector": user.NodeConnector,
		"forbidden_ip": user.ForbiddenIP, "forbidden_port": user.ForbiddenPort,
		// 页头统计 (PHP edit.tpl content-heading 下方裸统计)
		"rss_count": user.RssCount, "rss_count_lastday": user.RssCountLastday,
		"ban_times": user.BanTimes, "warming": user.Warming, "score": user.Score,
		"rss_ips_count": user.RssIpsCount, "rss_ips_lastday": user.RssIpsLastday, "rss_ip": user.RssIP,
		"t": user.T, "u": user.U, "d": user.D, "last_day_t": user.LastDayT,
		"top_up": topUp, "v2ray_uuid": user.V2rayUUID,
		"user_name": user.UserName,
	})
}

func fmtDT(t time.Time) string {
	if t.IsZero() || t.Year() < 2001 {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}

// RespondData 带数据的成功响应
func RespondData(c *gin.Context, msg string, data gin.H) {
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": msg, "data": data})
}

// ==================== 用户更新 (PUT) ====================

// updateAuditFields 审计字段清单 (对齐 PHP $auditFields)
var updateAuditFields = []string{
	"email", "pass", "auto_reset_day", "auto_reset_bandwidth", "port", "passwd",
	"protocol", "protocol_param", "obfs", "obfs_param", "is_multi_user",
	"transfer_enable", "transfer_limit", "invite_num", "method", "node_speedlimit",
	"node_connector", "enable", "ga_enable", "node_group", "ref_by", "remark",
	"money", "class", "class_expire", "expire_in", "forbidden_ip", "forbidden_port",
}

// UpdateUser 28 参数全量更新 (对齐 PHP Admin\UserController::update)
func (h *Handler) UpdateUser(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "用户ID无效")
		return
	}
	var user model.User
	if err := database.DB.First(&user, id).Error; err != nil {
		RespondErr(c, "用户不存在")
		return
	}

	var req struct {
		Email              string  `json:"email"`
		Pass               string  `json:"pass"` // 不修改留空
		AutoResetDay       int32   `json:"auto_reset_day"`
		AutoResetBandwidth string  `json:"auto_reset_bandwidth"`
		Port               int32   `json:"port"`
		Passwd             string  `json:"passwd"`
		Protocol           string  `json:"protocol"`
		ProtocolParam      string  `json:"protocol_param"`
		Obfs               string  `json:"obfs"`
		ObfsParam          string  `json:"obfs_param"`
		IsMultiUser        int32   `json:"is_multi_user"`
		TransferEnableGB   float64 `json:"transfer_enable"` // 单位 GB (PHP Tools::toGB)
		TransferLimit      int64   `json:"transfer_limit"`
		InviteNum          int32   `json:"invite_num"`
		Method             string  `json:"method"`
		NodeSpeedlimit     string  `json:"node_speedlimit"`
		NodeConnector      int32   `json:"node_connector"`
		Enable             int32   `json:"enable"`
		GaEnable           int32   `json:"ga_enable"`
		NodeGroup          int32   `json:"group"` // PHP 参数名 group
		RefBy              int32   `json:"ref_by"`
		Remark             string  `json:"remark"`
		Money              string  `json:"money"`
		Class              int32   `json:"class"`
		ClassExpire        string  `json:"class_expire"` // "2006-01-02 15:04:05"
		ExpireIn           string  `json:"expire_in"`
		ForbiddenIP        string  `json:"forbidden_ip"`   // 多行, 存时转逗号
		ForbiddenPort      string  `json:"forbidden_port"` // 多行, 存时转逗号
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespondErr(c, "参数有误: "+err.Error())
		return
	}

	// 变更前快照 (审计 diff 用)
	before := map[string]interface{}{
		"email": user.Email, "pass": user.Pass, "auto_reset_day": user.AutoResetDay,
		"auto_reset_bandwidth": user.AutoResetBandwidth, "port": user.Port, "passwd": user.Passwd,
		"protocol": user.Protocol, "protocol_param": user.ProtocolParam,
		"obfs": user.Obfs, "obfs_param": user.ObfsParam, "is_multi_user": user.IsMultiUser,
		"transfer_enable": user.TransferEnable, "transfer_limit": user.TransferLimit,
		"invite_num": user.InviteNum, "method": user.Method, "node_speedlimit": user.NodeSpeedlimit,
		"node_connector": user.NodeConnector, "enable": user.Enable, "ga_enable": user.GaEnable,
		"node_group": user.NodeGroup, "ref_by": user.RefBy, "remark": user.Remark,
		"money": user.Money, "class": user.Class, "class_expire": fmtDT(user.ClassExpire),
		"expire_in": fmtDT(user.ExpireIn), "forbidden_ip": user.ForbiddenIP, "forbidden_port": user.ForbiddenPort,
	}

	oldEnable := strconv.Itoa(int(user.Enable))
	gb := int64(1024 * 1024 * 1024)
	originEmail := user.Email

	user.Email = req.Email
	if req.Pass != "" {
		user.Pass = service.DefaultAuthService.SHA256Hash(req.Pass, config.GlobalConfig.App.Salt)
		// 密码修改 → 吊销旧登录态 (JWT tv 比对, 中间件即时失效)
		user.TokenVersion++
		// PHP update: 密码变更同时清订阅链接 (用户中心重新生成, 防止密码泄露后旧订阅仍有效)
		database.DB.Where("userid = ?", user.ID).Delete(&model.Link{})
	}
	// PHP update: 邮箱变更同步 Radius 改名/重建 (旧邮箱凭据不再可用)
	if originEmail != "" && originEmail != req.Email {
		radius.ChangeUserName(c.Request.Context(), user.ID, originEmail, req.Email, user.Passwd)
	}
	user.AutoResetDay = req.AutoResetDay
	user.AutoResetBandwidth = req.AutoResetBandwidth
	originPort := user.Port
	user.Port = req.Port
	// 端口联动: 用户中转规则跟随 (PHP update 语义)
	if originPort != req.Port {
		database.DB.Model(&model.Relay{}).
			Where("user_id = ? AND port = ?", user.ID, originPort).
			Update("port", req.Port)
	}
	user.Passwd = req.Passwd
	user.Protocol = req.Protocol
	user.ProtocolParam = req.ProtocolParam
	user.Obfs = req.Obfs
	user.ObfsParam = req.ObfsParam
	user.IsMultiUser = req.IsMultiUser
	user.TransferEnable = int64(req.TransferEnableGB * float64(gb))
	user.TransferLimit = req.TransferLimit
	user.InviteNum = req.InviteNum
	user.Method = req.Method
	user.NodeSpeedlimit = req.NodeSpeedlimit
	user.NodeConnector = req.NodeConnector
	user.Enable = int8(req.Enable)
	user.GaEnable = req.GaEnable
	user.NodeGroup = req.NodeGroup
	user.RefBy = req.RefBy
	user.Remark = req.Remark
	user.Money = req.Money
	user.Class = req.Class
	// 日期解析: 空 = 保持原值 (零日期用户编辑页本就回传空); 非空且非法 → 明确报错,
	// 避免旧版静默吞掉造成「改了却没生效」的陷阱
	parseDT := func(s string, field string) (time.Time, bool) {
		if s == "" {
			return time.Time{}, true
		}
		for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02"} {
			if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
				return t, true
			}
		}
		RespondErr(c, field+" 日期格式无效: "+s+" (应为 YYYY-MM-DD 或 YYYY-MM-DD HH:MM:SS)")
		return time.Time{}, false
	}
	if t, ok := parseDT(req.ClassExpire, "等级有效期"); !ok {
		return
	} else if req.ClassExpire != "" {
		user.ClassExpire = t
	}
	if t, ok := parseDT(req.ExpireIn, "账号过期时间"); !ok {
		return
	} else if req.ExpireIn != "" {
		user.ExpireIn = t
	}
	user.ForbiddenIP = linesToCSV(req.ForbiddenIP)
	user.ForbiddenPort = linesToCSV(req.ForbiddenPort)

	if err := database.DB.Save(&user).Error; err != nil {
		RespondErr(c, "修改失败: "+err.Error())
		return
	}

	// 审计 diff (保存后与快照对比)
	after := map[string]interface{}{
		"email": user.Email, "pass": user.Pass, "auto_reset_day": user.AutoResetDay,
		"auto_reset_bandwidth": user.AutoResetBandwidth, "port": user.Port, "passwd": user.Passwd,
		"protocol": user.Protocol, "protocol_param": user.ProtocolParam,
		"obfs": user.Obfs, "obfs_param": user.ObfsParam, "is_multi_user": user.IsMultiUser,
		"transfer_enable": user.TransferEnable, "transfer_limit": user.TransferLimit,
		"invite_num": user.InviteNum, "method": user.Method, "node_speedlimit": user.NodeSpeedlimit,
		"node_connector": user.NodeConnector, "enable": user.Enable, "ga_enable": user.GaEnable,
		"node_group": user.NodeGroup, "ref_by": user.RefBy, "remark": user.Remark,
		"money": user.Money, "class": user.Class, "class_expire": fmtDT(user.ClassExpire),
		"expire_in": fmtDT(user.ExpireIn), "forbidden_ip": user.ForbiddenIP, "forbidden_port": user.ForbiddenPort,
	}
	uaAuditUpdate(c, int64(user.ID), oldEnable, strconv.Itoa(int(user.Enable)), uaDiff(before, after, updateAuditFields))

	RespondOK(c, "修改成功")
}

// ==================== 用户删除 (DELETE) ====================

// DeleteUser 删除用户 (对齐 PHP: 返利回收 → kill_user 幽灵保护 → 级联删除 → 审计 error)
func (h *Handler) DeleteUser(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "用户ID无效")
		return
	}
	var user model.User
	if err := database.DB.First(&user, id).Error; err != nil {
		RespondErr(c, "用户不存在")
		return
	}

	// v5 注册返利回收 (仅旧口径 ref_rebate_status=0 且从未充值; 失败不阻断删除)
	_, _ = service.ClawbackLegacy(c.Request.Context(), id)

	// kill_user 幽灵保护: 未过期且配额有余量 → 拒绝 (节点上会留幽灵账号)
	now := time.Now()
	if user.ExpireIn.After(now) && user.TransferEnable > user.U+user.D && user.Enable == 1 {
		RespondErr(c, "删除失败：用户仍在服务中（未过期且配额有余量），直接删除会在节点上留下幽灵账号。请先禁用或等待其到期/流量耗尽，等待 ≥1 分钟后再删除")
		return
	}

	// 级联删除 (对齐 jobs killUser / PHP kill_user: 保留 Ip/DetectLog 等审计表)
	ctx := c.Request.Context()
	email := user.Email
	uid := int64(user.ID)
	radius.Delete(ctx, email)                                                        // PHP: Radius::Delete
	_ = database.DB.Exec("DELETE FROM user_action_log WHERE user_id = ?", uid).Error // PHP kill_user 同步删除
	_ = database.DB.Exec("DELETE FROM radius_ban WHERE userid = ?", uid).Error
	_ = database.DB.Exec("DELETE FROM disconnect_ip WHERE userid = ?", uid).Error
	_ = database.DB.Exec("DELETE FROM bought WHERE userid = ?", uid).Error
	_ = database.DB.Exec("DELETE FROM code WHERE userid = ?", uid).Error
	_ = database.DB.Exec("DELETE FROM link WHERE userid = ?", uid).Error
	_ = database.DB.Exec("DELETE FROM login_ip WHERE userid = ?", uid).Error
	_ = database.DB.Exec("DELETE FROM ss_invite_code WHERE user_id = ?", uid).Error
	_ = database.DB.Exec("DELETE FROM user_token WHERE user_id = ?", uid).Error
	_ = database.DB.Exec("DELETE FROM ss_password_reset WHERE email = ?", email).Error
	_ = database.DB.Exec(
		"DELETE FROM ticket WHERE rootid IN (SELECT t.id FROM (SELECT id FROM ticket WHERE userid = ? AND rootid = 0) t)", uid).Error
	_ = database.DB.Exec(
		"DELETE FROM ticket WHERE id IN (SELECT t.id FROM (SELECT id FROM ticket WHERE userid = ? AND rootid = 0) t)", uid).Error
	if err := database.DB.Delete(&model.User{}, uid).Error; err != nil {
		RespondErr(c, "删除失败: "+err.Error())
		return
	}

	// 审计: 删除账号 (error 级高危)
	uaLog(c, uid, "error", "admin", "user.delete", "管理员删除账号", gin.H{
		"email": email, "remark": user.Remark,
	})

	RespondOK(c, "删除成功")
}

// ==================== 切换身份 (POST masquerade) ====================

// Masquerade 管理员切换为目标用户 — 平移 v1 实现 (act_as_admin claim + cookie)
func (h *Handler) Masquerade(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErr(c, "用户ID无效")
		return
	}
	var user model.User
	if err := database.DB.First(&user, id).Error; err != nil {
		RespondErr(c, "目标用户不存在")
		return
	}
	adminUser := c.MustGet("user").(*model.User)
	ttl := int64(24 * 3600)
	tokenString, err := service.DefaultAuthService.GenerateTokenEx(&user, ttl, adminUser.ID)
	if err != nil {
		RespondErr(c, "生成Token失败")
		return
	}
	service.DefaultAuthService.SetAuthCookie(c, tokenString, ttl)
	RespondData(c, "切换成功", gin.H{
		"token":     tokenString,
		"user_name": user.UserName,
		"email":     user.Email,
	})
}
