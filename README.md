# SPanel-Go

SPanel（PHP 版，位于 `/var/www/SPanel`）的 Go 重构版。基于 Gin + GORM + Redis 的前后端分离代理服务管理面板。

---

## 架构总览：双实例 (Dual-Build) 架构

本项目编译为**两个独立部署单元**，来自同一份代码库（共享 `internal/`），每个单元 = **后端 Go API + 前端纯静态 HTML**：

| | 用户端 `cmd/user` (minimal) | 全功能端 `cmd/full` (含 admin) |
|---|---|---|
| 二进制 | `spanel-user` | `spanel-full` |
| 定位 | 面向公网的最小实例 | 管理端，全功能实例 |
| 后端路由 | auth / user / 节点通信 (node+mu) / 订阅 (link) | 以上全部 + `/api/v1/admin/*` |
| 前端静态页 | `public/user` `public/auth` `public/assets` | 以上全部 + `public/admin` |
| 部署位置 | 公网域名（如 `www.xxx.com`） | 内网 / 网关白名单之后（如 `admin.xxx.com` 仅允许管理 IP） |
| 默认端口 | `app.userPort`（回落 `port`） | `app.fullPort`（回落 `port+1`） |
| 数据库/Redis/JWT | 共享同一份 `config.yaml`，同库同密钥 | 同左 |

### 设计动机

1. **公网最小暴露面**：user 实例不注册任何 admin 路由，`/api/v1/admin/*` 是真实 404（路由表中不存在），而非中间件拦截。
2. **独立部署更新**：admin 变更只需重编译并重启 `spanel-full`，不影响线上 user 实例；反之亦然。
3. **代码只有一份**：路由注册函数化（`RegisterUserRoutes` / `RegisterAdminRoutes`），两个入口共用，无重复维护。

---

## 目录结构

```text
SPanel-Go/
├── cmd/
│   ├── user/main.go          # 用户端入口 (minimal)：SetupUserRouter()
│   └── full/main.go          # 全功能端入口：SetupRouter()
├── internal/
│   ├── app/
│   │   ├── bootstrap/        # 服务实例启动引导 (配置/DB/Redis/HTTP/优雅退出)
│   │   ├── router/
│   │   │   ├── router.go     # 基础引擎 + SetupUserRouter / SetupRouter 组装
│   │   │   ├── user_routes.go    # RegisterUserRoutes + 用户端静态页挂载
│   │   │   └── admin_routes.go   # RegisterAdminRoutes + admin 静态页挂载
│   │   ├── handler/          # 接口处理器 (public/user/ticket/admin/node/sub)
│   │   └── middleware/       # Auth / Admin / NodeAuth / Cors
│   ├── service/              # 核心业务逻辑
│   ├── model/                # GORM 领域模型
│   ├── config/               # YAML 配置加载
│   └── pkg/                  # 公共库 (database / captcha)
├── public/
│   ├── user/  auth/  admin/  assets/   # 前端纯静态 HTML
├── config.yaml               # 全局配置 (两个实例共用)
└── run.sh                    # 双实例构建/启动/停止/状态管理脚本
```

---

## 构建与运行

```bash
./run.sh build            # 编译 spanel-user + spanel-full
./run.sh start            # 启动全部实例
./run.sh start user       # 仅启动用户端
./run.sh restart full     # 仅重启全功能端 (不影响用户端)
./run.sh stop|status|logs # 停止 / 状态 / 日志 (logs 需指定 user|full)
```

端口可通过 `-port` 参数覆盖：`./spanel-user -port 9000`。

---

## 配置说明

`config.yaml` 中端口相关字段：

```yaml
app:
  port: 8080        # 基础端口
  userPort: 8080    # 用户端实例 (cmd/user)，缺省回落 port
  fullPort: 8081    # 全功能实例 (cmd/full)，缺省回落 port+1
```

两个实例共享同一份 `config.yaml`（同数据库、同 Redis、同 JWT secret）。JWT 密钥共享使 full 端的 Masquerade（管理员模拟用户）签发的 token 可直接用于 user 端，无需额外打通。

---

## 部署建议 (安全)

user 域名走公网；full 域名务必叠加网络层控制，nginx 示例：

```nginx
# 管理端：仅允许管理 IP + 内网穿透场景
server {
    server_name admin.xxx.com;
    allow <管理IP>;
    deny all;
    location / { proxy_pass http://127.0.0.1:8081; }
}

# 用户端：正常公网服务
server {
    server_name www.xxx.com;
    location / { proxy_pass http://127.0.0.1:8080; }
}
```

叠加建议：管理账号强制 2FA、管理端登录限流 + 验证码、JWT 过期时间管理端单独调短、保留审计日志并配置登录失败告警。

---

## 实施路线 (自旧版 PHP 项目迁移)

旧版 `/var/www/SPanel` 天然分为三部分：**user**（用户面板）、**admin**（管理后台）、**api**（给节点程序和其他后端程序调用的接口）。为避免迁移期间影响线上正常用户，按以下顺序推进：

1. **API 部分（优先）** —— `/link/:token` 订阅分发、`/mu/*` 与 `/api/node/*` 节点通信。节点与用户客户端依赖此入口，最先完成可保证存量服务不受影响。状态：✅ 初版完成。
2. **user 部分（其次）** —— auth 登录注册、用户中心（仪表盘/节点/签到/邀请/工单/安全设置）及 `public/user` 静态页。状态：开发中。
3. **admin 部分（最后）** —— 管理后台全部路由及 `public/admin` 静态页，仅在 full 实例中注册。状态：开发中。

> 实施顺序的意义：公网 user 实例承载 API + user 功能即可稳定服务存量用户；admin 功能独立在 full 实例中迭代，随时可部署或暂停，互不干扰。
