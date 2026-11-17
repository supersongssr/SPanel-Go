# SPanel-Go

SPanel（PHP 版，位于 `/var/www/SPanel`）的 Go 重构版。基于 Gin + GORM + Redis 的前后端分离代理服务管理面板。

---

## 架构总览：全功能单体

本项目编译为**单一部署单元** = **后端 Go API + 前端纯静态 HTML**，一个进程承载全部功能：

| 组成 | 内容 |
|---|---|
| 二进制 | `spanel`（唯一入口 `cmd/spanel`） |
| 后端路由 | auth / user / admin / 节点通信 (node+mu) / 订阅 / `/api/*` 对外 API (30 条) |
| 前端静态页 | `public/user` `public/auth` `public/admin` `public/assets` |
| 默认端口 | `app.port`（可 `-port` 参数覆盖） |

### 设计要点

1. **单一产物**：一次构建、一份二进制、一条流水线，无版本漂移问题。
2. **路由分层即可控**：`/api/v1/admin/*` 经 Auth + Admin 双重中间件保护；对外机器 API 经 muKey/node token 鉴权。
3. **网关层叠加管控**：admin 页面与路由建议在 nginx/网关叠加 IP 白名单 (见部署建议)。
4. **代码组织函数化**：路由注册函数化（`RegisterUserRoutes` / `RegisterAdminRoutes` / `RegisterApiRoutes`），结构清晰、无重复维护。

---

## 目录结构

```text
SPanel-Go/
├── cmd/
│   └── spanel/main.go       # 唯一入口：SetupRouter() 组装全部功能
├── internal/
│   ├── app/
│   │   ├── bootstrap/        # 服务启动引导 (配置/DB/Redis/HTTP/优雅退出)
│   │   ├── router/
│   │   │   ├── router.go     # 基础引擎 + SetupRouter 组装
│   │   │   ├── user_routes.go    # RegisterUserRoutes + 用户端静态页挂载
│   │   │   ├── admin_routes.go   # RegisterAdminRoutes + admin 静态页挂载
│   │   │   └── api_routes.go     # RegisterApiRoutes (/api/* 30 条)
│   │   ├── handler/          # 接口处理器 (public/user/ticket/admin/node/sub)
│   │   └── middleware/       # Auth / Admin / NodeAuth / Cors
│   ├── service/              # 核心业务逻辑
│   ├── model/                # GORM 领域模型
│   ├── config/               # YAML 配置加载
│   └── pkg/                  # 公共库 (database / captcha)
├── public/
│   ├── user/  auth/  admin/  assets/   # 前端纯静态 HTML
├── config.yaml               # 全局默认配置 (进 git, 勿放隐私信息)
├── .config.yaml.example     # 隐私覆盖配置模板 (复制为 .config.yaml, 已 gitignore)
└── run.sh                    # 菜单式构建/启停/接管 /api/ 管理脚本
```

---

## 构建与运行

```bash
./run.sh               # 无参数 → 交互菜单 (一键部署/编译/启停/接管/回滚)
./run.sh build         # 编译 → .bin/spanel
./run.sh start         # 启动 (默认端口 8090, 避开被占用的 8080)
./run.sh restart       # 重启
./run.sh stop|status|logs # 停止 / 状态 / 日志
./run.sh deploy        # nginx 接管 test-spanel.freessr.bid 的 /api/ → SPanel-Go
./run.sh rollback      # /api/ 交还给 PHP 面板
```

### 完整测试站 (推荐)

`https://test-spanel-go.freessr.bid:8443/` —— 独立域名全站由 SPanel-Go 提供
(静态页 + `/api/*` + `/mu` + `/link` + `/rss`), 不与 PHP 面板共享路由, 可完整测试:

- DNS: Cloudflare `test-spanel-go` A → 151.245.106.151 (DNS only)
- nginx: `/etc/nginx/conf.d/test-spanel-go.freessr.bid.conf` (80 → 301 → 8443 TLS → 127.0.0.1:8090)
- 证书: `/etc/ssl/freessr.bid.pem` (Let's Encrypt 通配符 `*.freessr.bid`)
- 入口示例: `/auth/login.html` · `/api/v1/auth/captcha` · `/api/v2/backend/healthz` · `/ping`

端口可通过 `-port` 参数覆盖：`.bin/spanel -port 9000`（run.sh 用 `APP_PORT` 环境变量覆盖）。

---

## 配置说明

配置分两层：

| 文件 | 作用 | 是否进 git |
|------|------|-----------|
| `config.yaml` | 默认配置（端口、开关、任务排程等非敏感项） | ✅ 提交 |
| `.config.yaml` | 用户隐私覆盖配置（密码、密钥等敏感项） | ❌ 已 gitignore |

`.config.yaml` 中的同名键会**深度覆盖** `config.yaml`，只需写需要覆盖的部分，例如：

```yaml
# .config.yaml
app:
  key: "your-random-key"
  muKey: "your-mu-key"
database:
  username: "xxx"
  password: "xxx"
jwt:
  secret: "random-secret"
```

模板参见 `.config.yaml.example`。

`config.yaml` 中端口相关字段：

```yaml
app:
  port: 8080        # 监听端口
```

---

## 部署建议 (安全)

单域名部署即可服务全部功能；管理面建议在网关层叠加 IP 白名单，nginx 示例：

```nginx
# 面板：正常公网服务
server {
    server_name www.xxx.com;

    # 管理端路由与页面：仅允许管理 IP
    location /api/v1/admin/ {
        allow <管理IP>;
        deny all;
        proxy_pass http://127.0.0.1:8080;
    }
    location /admin/ {
        allow <管理IP>;
        deny all;
        proxy_pass http://127.0.0.1:8080;
    }

    location / {
        proxy_pass http://127.0.0.1:8080;
    }
}
```

叠加建议：管理账号强制 2FA、管理端登录限流 + 验证码、JWT 过期时间管理端单独调短、保留审计日志并配置登录失败告警。

---

## 实施路线 (自旧版 PHP 项目迁移)

旧版 `/var/www/SPanel` 天然分为三部分：**user**（用户面板）、**admin**（管理后台）、**api**（给节点程序和其他后端程序调用的接口）。为避免迁移期间影响线上正常用户，按以下顺序推进：

1. **API 部分（优先）** —— `/link/:token` 订阅分发、`/mu/*` 与 `/api/node/*` 节点通信。节点与用户客户端依赖此入口，最先完成可保证存量服务不受影响。状态：✅ 初版完成。
2. **user 部分（其次）** —— auth 登录注册、用户中心（仪表盘/节点/签到/邀请/工单/安全设置）及 `public/user` 静态页。状态：开发中。
3. **admin 部分（最后）** —— 管理后台全部路由及 `public/admin` 静态页。状态：开发中。

---

## 用户端登录态 (2026-11-16 定案, 方案 §4.0)

**JWT + HttpOnly Cookie + token_version 吊销**：

- 登录成功 → `Set-Cookie: spanel_token=<jwt>; HttpOnly; Secure; SameSite=Lax`（remember_me 两态 Max-Age 1d/32d）
- JWT 载荷含 `tv`(user.token_version)；改密/重置/踢下线 → `tv+1` → 全端旧凭证即时 401
- 凭据优先级：HttpOnly Cookie → `Authorization: Bearer`（调试兜底）→ `?token=`（iosconf 直链）
- PoW 已移除（Go 高性能 + Redis 失败锁定 5 次/15min + 验证码 + IP 限频等价替代）
- 旧 URL 兼容：`/auth/login`、`/auth/register?code=`、`/auth/password/token/{t}`、`/user/<旧路径>` 等 302 → 新 `.html` 页面（灰度期 302，稳定后切 301）

## `/api/v1` 用户端路由表（auth 7 + user 59）

### auth 组（公开）
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | /api/v1/auth/captcha | 图形验证码 (svg+id) |
| POST | /api/v1/auth/login | 登录 (验证码+失败锁定+2FA TOTP+LoginIp 审计+Set-Cookie) |
| POST | /api/v1/auth/register | 注册 (register_mode/EV 验证码/IP 限频/邀请返利 v5/自动登录) |
| POST | /api/v1/auth/send-code | EV 验证码真实发送 (EmailVerify+SMTP, 限频+失败回滚) |
| POST | /api/v1/auth/password/reset | 发起密码重置 (24h 一次, 邮件链接) |
| POST | /api/v1/auth/password/token/:token | 执行重置 (关 2FA+tv+1+清订阅) |
| POST | /api/v1/auth/logout | 登出 (过期 Cookie) |

### user 组（Auth 中间件，cookie/Bearer/`?token=`）
| 功能域 | 端点 |
|---|---|
| 只读数据 | GET profile / dashboard / node / node/unlock / node/:id/daily / shop / invite / announcements[/:id] / donate / lookingglass / cncdn+cfcdn looking / detect / detect/log / disable / trafficlog / code/history / code/check / bought / rss / iosconf(下载) / relay / ticket / ticket/open[/:id] |
| edit 页动作 (16) | POST password / sspwd / method / port/reset / port/specify / wechat / ssr / theme / group / mail / sublimit / cncdn / cfcdn / hide / pacset / unblock |
| 2FA | POST ga/check / ga/set / ga/disable(二次校验) / ga/reset / telegram/reset |
| 账户 | POST checkin(原子防双签) / kill(W9 守卫+开关) / reactive / logout / back-to-admin(act_as_admin claim 还原) |
| 交易 | POST code/activate(原子核销) / coupon/check / buy(防双花) / invite/buy / invite/custom / invite/reset / relevel / vip/apply / cncdn/apply(封号保护) / sync-clonepay；DELETE bought/:id |
| 中转 CRUD | GET/POST relay；PUT/DELETE relay/:id (冲突检测) |
| 工单 | GET/POST ticket；GET/PUT/DELETE ticket/:id（限频 3/h、10/h，sort+=class） |
| 订阅管理 | POST rss/create / rss/delete / rss/reset |
| 订阅下发 | GET /link/:token 与 /rss/:token 双轨（Clash YAML） |

## `/api/` 对外 API 路由表（30 条）

> 完整方案见 `.plans/2026-11-12_api-routes-all.md`；契约文档对齐 PHP `docs/api2/`。
> 鉴权：`muKey` = 全局面板密钥（Bearer / X-API-Token / token 参数三态）；`node token` = 每节点独立 `ss_node.api_token`。

### v2 backend 组（生产核心，节点插件 ↔ 面板，`{"data"}`/`{"error"}` 信封）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v2/backend/users` | 本节点有效用户全量快照（node token） |
| GET | `/api/v2/backend/users/delta?since=` | 增量用户同步 v6.1（单水位线） |
| POST | `/api/v2/backend/traffic` | 批量流量上报（原始字节，倍率在此乘） |
| POST | `/api/v2/backend/status` | 节点状态心跳（uptime/load/在线数） |
| GET | `/api/v2/backend/healthz` | 健康检查（无鉴权） |

### v2 节点生命周期族（muKey 三态，`status/message` 信封，节点开通脚本契约）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/node/apply_id` | 申请节点 ID（恒新建不复用 + IP 登记） |
| POST | `/api/node/register` | 节点注册（node_id 可省；DNS/clone/协议预设） |
| POST | `/api/node/status` | 状态/流量心跳（`traffic_used` 唯一权威写入方，NIC 口径） |
| POST | `/api/node/config` | 生成 xray 配置（raw JSON + `X-Config-Hash`） |
| POST | `/api/node/nginx_config` | 渲染 nginx 配置（text/plain，HTTP 状态码信封） |
| POST | `/api/node/resolve_dns` | 整组 DNS 对账（CF + DB） |
| POST | `/api/node/unlock_check` | 流媒体解锁检测上报 |
| POST | `/api/node/edit` | 节点字段编辑（EDITABLE_FIELDS 白名单） |
| POST | `/api/node/delete` | 节点直接删除通道（全量级联，不可逆） |
| GET | `/api/nodes` | 全量节点导出（`ret/data` 旧信封，raw 全列输出） |

### v2 治理组（muKey，`{"data"}`/`{"error"}` 信封）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v2/user` | 按邮箱/ID 查用户（仅 Bearer；含流量汇总） |
| POST | `/api/v2/node/purge_dns` | 临时节点 DNS 秒级注销（整组，幂等） |
| POST | `/api/v2/node/offline` | 节点下架（整组置 type=0，幂等） |

### v1 保留（生产在用）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/clonepay` | 支付同步回调（IP 白名单 + MD5 签名 + 幂等入账 + 返利） |

### v0 / v1 已停用（对齐 PHP 2026-11-12 版本清点决议）

- v0 死路由 ×5（`/api/token*`、`GET /api/node`、`/api/user/:id`、`/api/sublink`）→ **410** + 迁移指引头
- v1 弃用族 ×6（`node/new`、`simple_api_tools`、`getNodeDomain`、`node/proxy`、`ssn_sub`、`ssn_v2`）→ **401** 直接拒绝（Go 版不移植）
