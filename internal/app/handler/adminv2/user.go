package adminv2

import (
	"time"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/tools"

	"github.com/gin-gonic/gin"
)

// userSortable 用户列表排序列白名单 (PHP DataTables orderable 列的并集;
// 计算列 online_ip_count/last_ss_time/used_traffic/enable_traffic/last_checkin_time/
// today_traffic/ref_by_user_name/top_up 不可排序, 对齐 PHP)
var userSortable = map[string]string{
	"id": "id", "user_name": "user_name", "remark": "remark", "email": "email",
	"money": "money", "im_type": "im_type", "im_value": "im_value",
	"node_group": "node_group", "expire_in": "expire_in", "class": "class",
	"class_expire": "class_expire", "passwd": "passwd", "port": "port",
	"method": "method", "protocol": "protocol", "obfs": "obfs",
	"enable": "enable", "reg_date": "reg_date", "reg_ip": "reg_ip",
	"auto_reset_day": "auto_reset_day", "auto_reset_bandwidth": "auto_reset_bandwidth",
	"ref_by": "ref_by",
}

// userListItem 用户列表行 (31 数据列, 对齐 PHP Admin\UserController::ajax;
// im_type/im_value 返回原始值由前端渲染, 时间已格式化, 流量人性化格式)
type userListItem struct {
	ID                 int32  `json:"id"`
	UserName           string `json:"user_name"`
	Remark             string `json:"remark"`
	Email              string `json:"email"`
	Money              string `json:"money"`
	IMType             int32  `json:"im_type"`
	IMValue            string `json:"im_value"`
	NodeGroup          int32  `json:"node_group"`
	ExpireIn           string `json:"expire_in"`
	Class              int32  `json:"class"`
	ClassExpire        string `json:"class_expire"`
	Passwd             string `json:"passwd"`
	Port               int32  `json:"port"`
	Method             string `json:"method"`
	Protocol           string `json:"protocol"`
	Obfs               string `json:"obfs"`
	OnlineIPCount      int64  `json:"online_ip_count"`
	LastSSTime         string `json:"last_ss_time"`
	UsedTraffic        string `json:"used_traffic"`
	EnableTraffic      string `json:"enable_traffic"`
	LastCheckinTime    string `json:"last_checkin_time"`
	TodayTraffic       string `json:"today_traffic"`
	Enable             int32  `json:"enable"`
	RegDate            string `json:"reg_date"`
	RegIP              string `json:"reg_ip"`
	AutoResetDay       int32  `json:"auto_reset_day"`
	AutoResetBandwidth string `json:"auto_reset_bandwidth"`
	RefBy              int32  `json:"ref_by"`
	RefByUserName      string `json:"ref_by_user_name"`
	TopUp              string `json:"top_up"`
}

func fmtTime(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04:05")
}

// fmtDateTime datetime 列格式化; MySQL 零日期 (0000-00-00, driver 解析为 1989 哨兵值)
// 与 2001 年前的哨兵一律显示为空
func fmtDateTime(t time.Time) string {
	if t.IsZero() || t.Unix() <= 0 || t.Year() < 2001 {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}

// ListUsers 用户列表 — 逐列对齐 PHP Admin\UserController::ajax
func (h *Handler) ListUsers(c *gin.Context) {
	q := ParseListQuery(c, userSortable, "id", "asc")

	query := database.DB.Model(&model.User{})
	if q.Search != "" {
		like := "%" + q.Search + "%"
		// PHP 口径: id/user_name/email/im_value/port LIKE
		query = query.Where(
			"id LIKE ? OR user_name LIKE ? OR email LIKE ? OR im_value LIKE ? OR port LIKE ?",
			like, like, like, like, like,
		)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		RespondErr(c, "查询用户总数失败")
		return
	}

	order := q.OrderBy + " " + q.OrderDir
	dbq := database.DB.Model(&model.User{})
	if q.Search != "" {
		like := "%" + q.Search + "%"
		dbq = dbq.Where(
			"id LIKE ? OR user_name LIKE ? OR email LIKE ? OR im_value LIKE ? OR port LIKE ?",
			like, like, like, like, like,
		)
	}
	var users []model.User
	if q.Limit < 0 {
		dbq = dbq.Order(order)
	} else {
		dbq = dbq.Order(order).Offset(q.Offset).Limit(q.Limit)
	}
	if err := dbq.Find(&users).Error; err != nil {
		RespondErr(c, "查询用户列表失败")
		return
	}

	// —— 批量补齐计算列 (避免 N+1) ——
	ids := make([]int32, 0, len(users))
	refIDs := make([]int32, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
		if u.RefBy > 0 {
			refIDs = append(refIDs, u.RefBy)
		}
	}

	// 在线 IP 数: alive_ip 按 userid 分组计数 (PHP $user->online_ip_count())
	ipCount := map[int32]int64{}
	if len(ids) > 0 {
		var rows []struct {
			UserID int32
			Cnt    int64
		}
		database.DB.Model(&model.AliveIP{}).
			Select("userid AS user_id, COUNT(*) AS cnt").
			Where("userid IN ?", ids).Group("userid").Scan(&rows)
		for _, r := range rows {
			ipCount[r.UserID] = r.Cnt
		}
	}

	// 邀请人用户名 (PHP: ref_by=0 → 系统邀请; 邀请人被删 → 邀请人已经被删除)
	refName := map[int32]string{}
	if len(refIDs) > 0 {
		var rows []struct {
			ID       int32
			UserName string
		}
		database.DB.Model(&model.User{}).Select("id, user_name").Where("id IN ?", refIDs).Scan(&rows)
		for _, r := range rows {
			refName[r.ID] = r.UserName
		}
	}

	// 累计充值: code 表 type=-1 且 isused=1 按 userid 汇总 (PHP InviteRebate::totalRecharge)
	topUp := map[int32]string{}
	if len(ids) > 0 {
		var rows []struct {
			UserID int32
			Sum    string
		}
		database.DB.Model(&model.Code{}).
			Select("userid AS user_id, COALESCE(SUM(number),0) AS sum").
			Where("userid IN ? AND type = -1 AND isused = 1", ids).
			Group("userid").Scan(&rows)
		for _, r := range rows {
			topUp[r.UserID] = r.Sum
		}
	}

	items := make([]userListItem, 0, len(users))
	for _, u := range users {
		refNameStr := "系统邀请"
		if u.RefBy > 0 {
			if n, ok := refName[u.RefBy]; ok {
				refNameStr = n
			} else {
				refNameStr = "邀请人已经被删除"
			}
		}
		tu, ok := topUp[u.ID]
		if !ok {
			tu = "0.00"
		}
		items = append(items, userListItem{
			ID: u.ID, UserName: u.UserName, Remark: u.Remark, Email: u.Email,
			Money: u.Money, IMType: u.IMType, IMValue: u.IMValue,
			NodeGroup: u.NodeGroup, ExpireIn: fmtDateTime(u.ExpireIn),
			Class: u.Class, ClassExpire: fmtDateTime(u.ClassExpire),
			Passwd: u.Passwd, Port: u.Port, Method: u.Method,
			Protocol: u.Protocol, Obfs: u.Obfs,
			OnlineIPCount:      ipCount[u.ID],
			LastSSTime:         fmtTime(u.T),
			UsedTraffic:        tools.FlowAutoShow(float64(u.U + u.D)),
			EnableTraffic:      tools.FlowAutoShow(float64(u.TransferEnable)),
			LastCheckinTime:    fmtTime(int64(u.LastCheckInTime)),
			TodayTraffic:       tools.FlowAutoShow(float64(u.U + u.D - u.LastDayT)),
			Enable:             int32(u.Enable),
			RegDate:            fmtDateTime(u.RegDate),
			RegIP:              u.RegIP,
			AutoResetDay:       u.AutoResetDay,
			AutoResetBandwidth: u.AutoResetBandwidth,
			RefBy:              u.RefBy,
			RefByUserName:      refNameStr,
			TopUp:              tu,
		})
	}

	RespondList(c, items, total)
}
