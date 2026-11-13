package handler

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"spanel-go/internal/app/middleware"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/timex"
	"spanel-go/internal/pkg/tools"
	"spanel-go/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// APIv2BackendHandler v2 后端节点 API (/api/v2/backend/*)
//
// 对齐 PHP app/Controllers/Backend/NodeController.php。契约: xray-plugin-api
// docs/openapi.yaml; 每节点独立 Bearer token (ss_node.api_token)。
//
// 语义红线 (违反 = 生产事故):
//   - GET /users 是全量快照, 名单里没有的用户就是该删的用户;
//   - 倍率只在这里乘: user.u/d 累加乘 node.traffic_rate, traffic_log 存原始字节;
//   - 不写节点用量: traffic_used 唯一权威写入方是 /api/node/status (NIC 口径),
//     用户层字节与 NIC 口径叠加 = 双重计费 (2026-10 事故);
//   - 全部批量一次性写入, 禁止循环逐条 INSERT, 禁止 ORM 读-改-写累加;
//   - 信封: 成功 {"data":{...}}, 失败 {"error":{...}} (不用 ret/msg)。
type APIv2BackendHandler struct{}

var DefaultAPIv2BackendHandler = &APIv2BackendHandler{}

const (
	// backendMaxItemBytes 单条字节上限 (1<<60 ≈ 1.15EB): 超过必为恶意/损坏数据
	backendMaxItemBytes = int64(1) << 60
	// backendUpdateBatchSize CASE WHEN 累加每条 UPDATE 的用户数
	// (单条巨型语句数万用户时超 max_allowed_packet 默认 4MB → 500 → 插件无限重试卡死)
	backendUpdateBatchSize = 500
	// backendDeltaOverlapSecs 增量水位线重叠窗 (秒): 吸收轮询间隙内的写入竞态
	backendDeltaOverlapSecs = 5
	// backendDeltaMaxRows 增量响应两桶行数上限: 超过退回全量
	backendDeltaMaxRows = 4096
)

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/v2/backend/healthz — 健康检查 (无鉴权, 给 nginx/监控探活)
// ─────────────────────────────────────────────────────────────────────────────

// Healthz GET /api/v2/backend/healthz
func (h *APIv2BackendHandler) Healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"status": "ok"}})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/v2/backend/users — 本节点有效用户全量快照
// ─────────────────────────────────────────────────────────────────────────────

// backendUserRow 快照行 (只取三列, 万级用户下不 hydrate 全模型)
type backendUserRow struct {
	ID        int32  `gorm:"column:id"`
	Email     string `gorm:"column:email"`
	V2rayUUID string `gorm:"column:v2ray_uuid"`
}

// Users GET /api/v2/backend/users
func (h *APIv2BackendHandler) Users(c *gin.Context) {
	node := middleware.NodeFromContext(c)
	if node == nil || database.DB == nil {
		errEnvelope(c, http.StatusInternalServerError, "INTERNAL", "context missing node")
		return
	}

	// 心跳 (与 Mu 路径同字段)
	database.DB.Model(&model.Node{}).Where("id = ?", node.ID).Update("node_heartbeat", timex.Unix())

	// 节点流量耗尽: 200 + 空列表 (插件据此删光用户)
	if nodeIsTrafficOut(node) {
		dataOK(c, gin.H{"users": []any{}})
		return
	}

	users, err := effectiveUsers(node)
	if err != nil {
		logApiV2("backend users query failed node=%d err=%v", node.ID, err)
		errEnvelope(c, http.StatusInternalServerError, "INTERNAL", "users query failed")
		return
	}
	dataOK(c, gin.H{"users": users})
}

// nodeIsTrafficOut 节点流量耗尽判定 (对齐 PHP Node::isNodeTrafficOut)
func nodeIsTrafficOut(n *model.Node) bool {
	return n.TrafficLimit != 0 && n.TrafficUsed >= n.TrafficLimit
}

// effectiveUsers 本节点有效用户名单 (users() 与 usersDelta 的 mode=full 共用, 单一事实源)
//
// 过滤清单 (与旧 Mu\UserController::index 口径一致):
//  1. enable=1  2. 未过期 (expire_in > now, 本地时区时间串)
//  3. 有余量 (transfer_enable > u+d; decimal 语境: u/d 可被 traffic() 的 LEAST
//     饱和到 bigint max, 裸 bigint 相加会溢出抛错打挂分发)
//  4. 等级达标 (class >= node_class; 管理员 is_admin=1 无条件放行)
//  5. 分组匹配 (node_group=0 不限)
func effectiveUsers(node *model.Node) ([]gin.H, error) {
	rows := make([]backendUserRow, 0, 256)
	q := database.DB.Table("user").
		Select("id, email, v2ray_uuid").
		Where("enable = 1").
		Where("expire_in > ?", timex.DateStrNow()).
		Where("transfer_enable > CAST(u AS DECIMAL(30,0)) + CAST(d AS DECIMAL(30,0))")

	if node.NodeGroup != 0 {
		// 有分组: (等级达标 且 分组匹配) 或 管理员
		q = q.Where("((class >= ? AND node_group = ?) OR is_admin = 1)", node.NodeClass, node.NodeGroup)
	} else {
		// 不分组: 等级达标 或 管理员
		q = q.Where("(class >= ? OR is_admin = 1)", node.NodeClass)
	}

	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	users := make([]gin.H, 0, len(rows))
	for _, u := range rows {
		users = append(users, gin.H{"id": u.ID, "email": u.Email, "uuid": u.V2rayUUID})
	}
	return users, nil
}

// eligibleRawSQL 资格谓词内联裸 SQL (与 effectiveUsers WHERE 同口径, 单一事实源)
// 供 usersDelta 作 elig 计算列: 变更行按此分 add/del 两桶。
func eligibleRawSQL(node *model.Node) string {
	now := timex.DateStrNow()
	if node.NodeGroup != 0 {
		return fmt.Sprintf("`enable` = 1 AND `expire_in` > '%s' AND `transfer_enable` > CAST(`u` AS DECIMAL(30,0)) + CAST(`d` AS DECIMAL(30,0)) AND ((`class` >= %d AND `node_group` = %d) OR `is_admin` = 1)",
			now, node.NodeClass, node.NodeGroup)
	}
	return fmt.Sprintf("`enable` = 1 AND `expire_in` > '%s' AND `transfer_enable` > CAST(`u` AS DECIMAL(30,0)) + CAST(`d` AS DECIMAL(30,0)) AND (`class` >= %d OR `is_admin` = 1)",
		now, node.NodeClass)
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/v2/backend/users/delta — 增量用户同步 v6.1 (单水位线)
// ─────────────────────────────────────────────────────────────────────────────

// UsersDelta GET /api/v2/backend/users/delta?since=<上次回显水位线>
func (h *APIv2BackendHandler) UsersDelta(c *gin.Context) {
	node := middleware.NodeFromContext(c)
	if node == nil || database.DB == nil {
		errEnvelope(c, http.StatusInternalServerError, "INTERNAL", "context missing node")
		return
	}

	// 心跳 (delta 模式下节点不再轮 /users, 心跳由此承接)
	database.DB.Model(&model.Node{}).Where("id = ?", node.ID).Update("node_heartbeat", timex.Unix())

	// 新水位线: DB 时钟 (与触发器同钟) - 重叠窗
	w := time.Now().Unix() - backendDeltaOverlapSecs
	var dbNow struct{ T int64 }
	if err := database.DB.Raw("SELECT UNIX_TIMESTAMP() AS t").Scan(&dbNow).Error; err == nil && dbNow.T > 0 {
		w = dbNow.T - backendDeltaOverlapSecs
	}

	// 节点流量耗尽: full + 空名单 (节点据此删光)
	if nodeIsTrafficOut(node) {
		deltaFull(c, nil, w)
		return
	}

	since, _ := parseInt64(c.Query("since"))
	if since <= 0 {
		// 首次 (缺省/0): full + 权威名单 (与 GET /users 同构)
		users, err := effectiveUsers(node)
		if err != nil {
			logApiV2("delta full query failed node=%d err=%v", node.ID, err)
			errEnvelope(c, http.StatusInternalServerError, "INTERNAL", "users query failed")
			return
		}
		deltaFull(c, users, w)
		return
	}

	// 一条查询出两桶 (单语句即快照, 无需显式事务): elig 计算列 = effectiveUsers 的 WHERE 原样内联
	rows := make([]struct {
		ID        int32  `gorm:"column:id"`
		Email     string `gorm:"column:email"`
		V2rayUUID string `gorm:"column:v2ray_uuid"`
		Elig      int    `gorm:"column:elig"`
	}, 0, 128)
	if err := database.DB.Table("user").
		Select("id, email, v2ray_uuid, ("+eligibleRawSQL(node)+") AS elig").
		Where("status_changed_at > ?", since).
		Find(&rows).Error; err != nil {
		logApiV2("delta query failed node=%d err=%v", node.ID, err)
		errEnvelope(c, http.StatusInternalServerError, "INTERNAL", "delta query failed")
		return
	}

	users := make([]gin.H, 0, len(rows))
	removedSet := map[string]bool{}
	removed := make([]string, 0, 8)
	for _, u := range rows {
		if u.Elig == 1 {
			users = append(users, gin.H{"id": u.ID, "email": u.Email, "uuid": u.V2rayUUID})
		} else if !removedSet[u.Email] {
			removedSet[u.Email] = true
			removed = append(removed, u.Email)
		}
	}

	// 变更超阈值 (变更风暴/全表打戳波): 退回全量, 语义等价且一次到位
	if len(users)+len(removed) > backendDeltaMaxRows {
		all, err := effectiveUsers(node)
		if err != nil {
			logApiV2("delta fallback query failed node=%d err=%v", node.ID, err)
			errEnvelope(c, http.StatusInternalServerError, "INTERNAL", "users query failed")
			return
		}
		deltaFull(c, all, w)
		return
	}

	dataOK(c, gin.H{
		"mode":    "delta",
		"since":   w,
		"users":   users,
		"removed": removed,
	})
}

func deltaFull(c *gin.Context, users []gin.H, w int64) {
	if users == nil {
		users = []gin.H{}
	}
	dataOK(c, gin.H{"mode": "full", "since": w, "users": users})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/v2/backend/traffic — 批量上报流量增量 (原始字节)
// ─────────────────────────────────────────────────────────────────────────────

// backendTrafficItem 单条上报
type backendTrafficItem struct {
	UserID        json.Number `json:"userId"`
	UplinkBytes   json.Number `json:"uplinkBytes"`
	DownlinkBytes json.Number `json:"downlinkBytes"`
}

// strictInt64 严格整数校验: 拒绝浮点/字符串/越界 (PHP is_int 同口径 —— 超范围数字
// 解码成 float 会强转出垃圾正数直接污染计费)
func strictInt64(n json.Number) (int64, bool) {
	if n == "" {
		return 0, false
	}
	if v, err := n.Int64(); err == nil {
		return v, true
	}
	return 0, false
}

// Traffic POST /api/v2/backend/traffic
func (h *APIv2BackendHandler) Traffic(c *gin.Context) {
	node := middleware.NodeFromContext(c)
	if node == nil || database.DB == nil {
		errEnvelope(c, http.StatusInternalServerError, "INTERNAL", "context missing node")
		return
	}

	var body struct {
		Data struct {
			Items []backendTrafficItem `json:"items"`
		} `json:"data"`
	}
	raw, err := c.GetRawData()
	if err != nil {
		errEnvelope(c, http.StatusBadRequest, "BAD_REQUEST", "invalid body")
		return
	}
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) != nil {
		errEnvelope(c, http.StatusBadRequest, "BAD_REQUEST", "invalid body")
		return
	}
	var dataProbe struct {
		Items *json.RawMessage `json:"items"`
	}
	if dRaw, ok := probe["data"]; !ok || json.Unmarshal(dRaw, &dataProbe) != nil || dataProbe.Items == nil {
		errEnvelope(c, http.StatusBadRequest, "BAD_REQUEST", "data.items is required")
		return
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		errEnvelope(c, http.StatusBadRequest, "BAD_REQUEST", "invalid body")
		return
	}

	// 校验 + 按 userId 合并 (防重复 id 导致 CASE 覆盖); 总和防溢出
	merged := map[int64][2]int64{}
	var order []int64
	var totalRaw int64
	for _, it := range body.Data.Items {
		uid, ok1 := strictInt64(it.UserID)
		up, ok2 := strictInt64(it.UplinkBytes)
		down, ok3 := strictInt64(it.DownlinkBytes)
		if !ok1 || uid <= 0 || !ok2 || up < 0 || !ok3 || down < 0 {
			errEnvelope(c, http.StatusBadRequest, "BAD_REQUEST", "invalid item fields")
			return
		}
		if up > backendMaxItemBytes || down > backendMaxItemBytes {
			errEnvelope(c, http.StatusBadRequest, "BAD_REQUEST", "byte count exceeds sane limit")
			return
		}
		if _, exists := merged[uid]; !exists {
			order = append(order, uid)
		}
		pair := merged[uid]
		pair[0] += up
		pair[1] += down
		if pair[0] < 0 || pair[1] < 0 {
			errEnvelope(c, http.StatusBadRequest, "BAD_REQUEST", "item sum overflow")
			return
		}
		merged[uid] = pair
		totalRaw += up + down
		if totalRaw < 0 {
			errEnvelope(c, http.StatusBadRequest, "BAD_REQUEST", "batch total overflow")
			return
		}
	}

	accepted := len(merged)
	if accepted == 0 {
		dataOK(c, gin.H{"accepted": 0})
		return
	}

	now := timex.Unix()
	nodeID := int64(node.ID)
	rate := float64(node.TrafficRate)
	bigintMax := "9223372036854775807"
	// 始终用小数字面量: bigint + decimal 不触发 1690 溢出错, 配合 LEAST 封顶 → 饱和而非报错
	rateStr := strings.TrimRight(fmt.Sprintf("%.10f", rate), "0")
	if !strings.Contains(rateStr, ".") {
		rateStr += ".0"
	}

	// 事务内任何异常 → 契约信封 500 (否则插件拿到非契约响应无法区分语义)。
	// 豁免打戳: 流量上报写 u/d/t 不刷新 status_changed_at (增量同步黑名单规则,
	// user_status_changed_bu 触发器读会话变量 @spanel_skip_user_stamp)。
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET @spanel_skip_user_stamp = 1").Error; err != nil {
			return err
		}
		defer func() { _ = tx.Exec("SET @spanel_skip_user_stamp = NULL").Error }()

		// CASE WHEN 原子累加 (u/d 乘倍率 + LEAST 饱和封顶), 按固定批切分 (防 max_allowed_packet)
		for start := 0; start < len(order); start += backendUpdateBatchSize {
			end := start + backendUpdateBatchSize
			if end > len(order) {
				end = len(order)
			}
			batch := order[start:end]
			var sb strings.Builder
			sb.WriteString("UPDATE `user` SET `u` = CASE `id`")
			var sbD strings.Builder
			sbD.WriteString("`d` = CASE `id`")
			ids := make([]any, 0, len(batch)*2)
			for _, uid := range batch {
				pair := merged[uid]
				fmt.Fprintf(&sb, " WHEN %d THEN LEAST(u + %d * %s, %s)", uid, pair[0], rateStr, bigintMax)
				fmt.Fprintf(&sbD, " WHEN %d THEN LEAST(d + %d * %s, %s)", uid, pair[1], rateStr, bigintMax)
				ids = append(ids, uid)
			}
			sb.WriteString(" END, ")
			sb.WriteString(sbD.String())
			sb.WriteString(" END, `t` = ")
			fmt.Fprintf(&sb, "%d WHERE `id` IN (%s)", now, placeholders(len(batch)))
			if err := tx.Exec(sb.String(), ids...).Error; err != nil {
				return err
			}
		}

		// 流量明细: 批量 INSERT (每 500 行一批); 原始字节 + rate + 乘率后人类可读文本
		type logRow struct {
			UserID  int64   `gorm:"column:user_id"`
			U       int64   `gorm:"column:u"`
			D       int64   `gorm:"column:d"`
			NodeID  int64   `gorm:"column:node_id"`
			Rate    float64 `gorm:"column:rate"`
			Traffic string  `gorm:"column:traffic"`
			LogTime int64   `gorm:"column:log_time"`
		}
		rows := make([]logRow, 0, len(order))
		for _, uid := range order {
			pair := merged[uid]
			rows = append(rows, logRow{
				UserID:  uid,
				U:       pair[0],
				D:       pair[1],
				NodeID:  nodeID,
				Rate:    rate,
				Traffic: tools.FlowAutoShow(float64(pair[0]+pair[1]) * rate),
				LogTime: now,
			})
		}
		for start := 0; start < len(rows); start += backendUpdateBatchSize {
			end := start + backendUpdateBatchSize
			if end > len(rows) {
				end = len(rows)
			}
			if err := tx.Table("user_traffic_log").Create(rows[start:end]).Error; err != nil {
				return err
			}
		}
		return nil

		// [2026-10-29] ss_node.traffic_used 不累加用户层字节 —— 唯一权威写入方是
		// /api/node/status (NodeApiService::reportStatus, NIC 网卡计数器口径)。
		// 用户层字节不含 nginx/TLS/非用户流量, 与 NIC 口径叠加 = 双重计费。
	})
	if txErr != nil {
		logApiV2("backend traffic write failed node=%d err=%v", nodeID, txErr)
		errEnvelope(c, http.StatusInternalServerError, "INTERNAL", "traffic write failed")
		return
	}

	// 派生字段刷新 (剩余流量 left/daily + 心跳; 失败不影响已入账流量, 只记日志)。
	// 单条 UPDATE 内全部引用库内当前列值原子派生, 不基于请求开始时的内存快照,
	// 与月度重置 cron / 管理员校准 / 同节点上报重叠等并发写者无脏读窗口。
	refreshNodeDerived(node)

	dataOK(c, gin.H{"accepted": accepted})
}

// refreshNodeDerived 派生字段原子刷新 (对齐 PHP traffic() 尾部 UPDATE)
func refreshNodeDerived(node *model.Node) {
	daysElapsed := service.DaysElapsed(int(node.TrafficResetDay))
	if daysElapsed < 1 {
		daysElapsed = 1
	}
	daysRemaining := service.DaysRemaining(int(node.TrafficResetDay))
	if daysRemaining < 1 {
		daysRemaining = 1
	}
	cap := service.DailyLeftCap(node.TrafficMaxDayValue)

	sql := fmt.Sprintf(
		"UPDATE `ss_node` SET "+
			"`traffic_left` = IF(`traffic_limit` > 0, GREATEST(`traffic_limit` - `traffic_used`, 0), 0), "+
			"`traffic_used_daily` = FLOOR(`traffic_used` / %d), "+
			"`traffic_left_daily` = IF(`traffic_limit` > 0, FLOOR(GREATEST(`traffic_limit` - `traffic_used`, 0) / %d), %d), "+
			"`node_heartbeat` = %d WHERE `id` = %d",
		daysElapsed, daysRemaining, cap, timex.Unix(), node.ID)
	if err := database.DB.Exec(sql).Error; err != nil {
		logApiV2("traffic derived refresh failed node=%d err=%v", node.ID, err)
	}
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	var sb strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("?")
	}
	return sb.String()
}

func parseInt64(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/v2/backend/status — 节点状态心跳
// ─────────────────────────────────────────────────────────────────────────────

// Status POST /api/v2/backend/status
//
// 写 ss_node_info (uptime/load) + 心跳; onlineUsers>0 写 ss_node_online_log。
func (h *APIv2BackendHandler) Status(c *gin.Context) {
	node := middleware.NodeFromContext(c)
	if node == nil || database.DB == nil {
		errEnvelope(c, http.StatusInternalServerError, "INTERNAL", "context missing node")
		return
	}

	var body struct {
		Data struct {
			UptimeSeconds float64 `json:"uptimeSeconds"`
			LoadAverage   struct {
				OneMinute      float64 `json:"oneMinute"`
				FiveMinutes    float64 `json:"fiveMinutes"`
				FifteenMinutes float64 `json:"fifteenMinutes"`
			} `json:"loadAverage"`
			OnlineUsers float64 `json:"onlineUsers"`
		} `json:"data"`
	}
	raw, err := c.GetRawData()
	if err != nil {
		errEnvelope(c, http.StatusBadRequest, "BAD_REQUEST", "invalid body")
		return
	}
	var probe map[string]any
	if json.Unmarshal(raw, &probe) != nil {
		errEnvelope(c, http.StatusBadRequest, "BAD_REQUEST", "invalid body")
		return
	}
	// 必填校验: uptimeSeconds / loadAverage / onlineUsers (对齐 PHP isset)
	d, dok := probe["data"].(map[string]any)
	if !dok {
		errEnvelope(c, http.StatusBadRequest, "BAD_REQUEST", "uptimeSeconds/loadAverage/onlineUsers are required")
		return
	}
	_, ok1 := d["uptimeSeconds"]
	_, ok2 := d["loadAverage"]
	_, ok3 := d["onlineUsers"]
	if !ok1 || !ok2 || !ok3 {
		errEnvelope(c, http.StatusBadRequest, "BAD_REQUEST", "uptimeSeconds/loadAverage/onlineUsers are required")
		return
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		errEnvelope(c, http.StatusBadRequest, "BAD_REQUEST", "invalid body")
		return
	}

	uptime := math.Max(0, body.Data.UptimeSeconds)
	onlineUsers := int(math.Max(0, body.Data.OnlineUsers))
	load := fmt.Sprintf("%.2f %.2f %.2f",
		body.Data.LoadAverage.OneMinute,
		body.Data.LoadAverage.FiveMinutes,
		body.Data.LoadAverage.FifteenMinutes)

	now := timex.Unix()
	info := model.NodeInfoLog{NodeID: node.ID, Uptime: float32(uptime), Load: load, LogTime: int32(now)}
	if err := database.DB.Create(&info).Error; err != nil {
		logApiV2("backend status write failed node=%d err=%v", node.ID, err)
		errEnvelope(c, http.StatusInternalServerError, "INTERNAL", "status write failed")
		return
	}
	if onlineUsers > 0 {
		online := model.NodeOnlineLog{NodeID: node.ID, OnlineUser: int32(onlineUsers), LogTime: int32(now)}
		if err := database.DB.Create(&online).Error; err != nil {
			logApiV2("backend status online write failed node=%d err=%v", node.ID, err)
			errEnvelope(c, http.StatusInternalServerError, "INTERNAL", "status write failed")
			return
		}
	}
	if err := database.DB.Model(&model.Node{}).Where("id = ?", node.ID).
		Update("node_heartbeat", now).Error; err != nil {
		logApiV2("backend status heartbeat failed node=%d err=%v", node.ID, err)
		errEnvelope(c, http.StatusInternalServerError, "INTERNAL", "status write failed")
		return
	}

	dataOK(c, gin.H{"accepted": true})
}
