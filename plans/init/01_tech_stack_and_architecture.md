# SPanel-Go 架构设计与技术栈选型文档

本项目旨在将基于 PHP 7.4 (Slim 3.x + Eloquent + Smarty) 的传统代理服务管理面板 **SPanel** 重构为高性能、高内聚、前后端分离的 **SPanel-Go** 面板。本篇文档聚焦于后端技术栈选型、项目工程结构、核心模块设计与迁移路线。

---

## 1. 核心技术栈选型 (Tech Stack Selection)

为确保项目具备极高的性能、并发处理能力和长期可维护性，同时保证前后端分离的纯净性，建议采用以下技术栈：

### 1.1 后端核心框架与组件

| 组件类型 | 选用技术 / 库 | 选型理由 |
| :--- | :--- | :--- |
| **编程语言** | **Go (Golang 1.21+)** | 极佳的并发性能（协程）、静态编译、极小内存占用，非常适合高并发的节点通信与订阅分发。 |
| **Web 核心框架** | **Gin / Fiber** | **Gin** 生态极度成熟，中间件丰富，性能优秀且稳定；若追求极致吞吐量，亦可考虑基于 `fasthttp` 的 **Fiber**。此处推荐 **Gin** 作为首选以保证稳定性和开箱即用的 HTTP 标准库兼容性。 |
| **ORM (数据库对象映射)** | **GORM** | 功能强大，支持 MySQL/MariaDB。其关联查询、事务、Hooks 机制与原版 Eloquent 最为相似，能极大降低数据迁移和业务逻辑重写的门槛。 |
| **缓存与高性能存储** | **Go-Redis (v9)** | 完美替代 PHP 中的 Redis 缓存。用于用户在线 Token 存储、接口限流（Rate Limiting）、PoW 防 CC 凭证暂存及高频统计数据缓存。 |
| **API 认证机制** | **JWT (JSON Web Tokens)** | 前后端分离的核心。使用 `golang-jwt/jwt` 实现无状态/有状态混合认证，代替 PHP 原版的 Cookie / Redis Session 方案。 |
| **系统日志** | **Zap (Uber)** | 极致性能的结构化日志库，支持高并发写日志，便于生产环境下的异常追踪与运维审计。 |
| **定时任务系统** | **Robfig/Cron (v3)** | 替代 PHP 原生的外部 Crontab 触发（think CLI/xcat）。实现纯 Go 进程内的高性能常驻定时任务（如每日流量重置、死节点 DNS 解析清理、财务统计等）。 |

### 1.2 技术选型对比分析

| 维度 | 旧版 PHP 架构 (Slim 3) | 新版 Go 架构 (Gin/GORM) | 提升效果 |
| :--- | :--- | :--- | :--- |
| **并发处理** | PHP-FPM 多进程，每个请求独占进程，易导致高并发阻塞。 | Go 协程（Goroutine），单进程支撑数万级并发，非常适合应对节点大流量上报和瞬间订阅请求。 | 吞吐量提升 10x - 50x，内存占用降低 90% 以上。 |
| **接口响应延时** | 典型 API 耗时 50ms - 200ms。 | 典型 API 耗时 1ms - 15ms。 | 订阅生成与状态上报几乎零延迟，用户体验极其流畅。 |
| **定时任务** | 依赖外部 Linux Crontab，容易发生重叠执行和初始化开销。 | 内存常驻 Cron 调度，精细化控制，支持分布式锁。 | 调度精度提升，任务执行无额外 bootstrap 启动开销。 |
| **部署与维护** | 必须安装 PHP 运行环境、多组依赖包、Web 服务器（Nginx）。 | 单个二进制文件直接运行，无外部 runtime 依赖，容器化与部署极其简便。 | 简化部署流程，极大降低运维成本。 |

---

## 2. 模块化工程目录设计 (Modular Project Structure)

为了确保项目的**模块化编程方式**（Modular Programming），我们将遵循 Go 社区主流的 **Layered Clean Architecture (分层整洁架构)** 规范进行工程化布局，严禁任何业务逻辑在 Controller 中堆砌。

### 目录骨架规划：

```text
SPanel-Go/
├── cmd/
│   ├── api/                   # Web 服务入口 (main.go)
│   └── cron/                  # 常驻定时任务入口 (main.go)
├── config/
│   ├── config.go              # 配置加载与结构体定义 (映射旧版 .config.php)
│   └── config.yaml            # 配置文件 (采用 YAML 代替 PHP 数组，更加现代)
├── internal/
│   ├── app/                   # 应用层
│   │   ├── handler/           # 接口处理器 (对应 Controllers)
│   │   │   ├── admin/         # 管理员面板接口
│   │   │   ├── user/          # 用户面板接口
│   │   │   ├── nodeapi/       # 节点通信接口 (Mu/V2)
│   │   │   └── sub/           # 订阅接口 (LinkController 重构)
│   │   ├── middleware/        # 中间件 (Auth, Admin, RateLimit, Cors, Captcha)
│   │   └── router/            # 路由定义入口
│   ├── model/                 # 领域模型与 GORM 映射 (对应 app/Models)
│   ├── repository/            # 数据访问层 (DAO，将 SQL 操作与业务解耦)
│   ├── service/               # 核心业务逻辑层 (对应 app/Services)
│   │   ├── auth/              # 登录注册、双因子认证、PoW 挑战
│   │   ├── node/              # 节点配置下发、状态统计、自动心跳
│   │   ├── subscription/      # Clash/Sing-box/QX 聚合订阅流生成
│   │   ├── payment/           # 易付通、ClonePay 等支付网关接入
│   │   ├── mail/              # SMTP/Mailgun 异步邮件发送
│   │   └── telegram/          # Telegram Bot 交互与通知推送
│   └── pkg/                   # 系统级公共库 (独立于业务，可复用)
│       ├── hash/              # 密码加密 (Sha256/Argon2/Bcrypt)
│       ├── pow/               # 验证码 Proof of Work 算法实现
│       ├── qqwry/             # 纯真 IP 数据库解析库
│       ├── cloudflare/        # Cloudflare API 封装
│       └── utils/             # 时间、字符串、网络等辅助函数
├── sql/                       # 数据库变更与初始化 SQL 文件
├── go.mod                     # 依赖管理
└── README.md
```

---

## 3. 核心后端模块详细设计

在重构过程中，以下几个模块属于 SPanel 的核心命脉，需要采用严密的 Go 面向对象模式设计：

### 3.1 订阅生成服务 (Subscription Service)
- **职责**：将数据库中的节点数据转换为各大主流代理客户端（Clash, Surge, Quantumult X, Sing-box）的配置格式。
- **设计要点**：
  1. **配置模板化**：不应在 Go 代码中拼接 JSON 或 YAML，而是使用 Go 内置的 `text/template` 或 `html/template`，把基础配置文件和节点注入点彻底分离，方便管理员热更新订阅模板。
  2. **多通道并发生成**：用户请求订阅时，通过 Go 协程并发过滤节点、计算倍率和测速结果，最后进行统一编排，确保大吞吐量订阅的高速响应。

### 3.2 节点状态与上报服务 (Node API Service)
- **职责**：对接各个下属代理节点（Xray, Sing-box 等），收集实时流量数据、在线人数、系统负载，并下发最新节点配置。
- **设计要点**：
  1. **V2 Node API 100% 兼容**：严格遵循旧版中 `V2ApiController` 定义的协议（如 `/api/node/register`, `/api/node/status`），保证无需修改现有代理节点后端即可平滑迁移。
  2. **批量流量入库 (Write-back Pattern)**：不要每次节点上报流量都直接操作 MySQL。应采用 Redis 作为高频流量计数器，再由 Cron 协程每隔 1 分钟批量同步回 MySQL `user` 和 `ss_node` 表，彻底消除高并发下的数据库写瓶颈。

### 3.3 支付网关服务 (Payment Gateway Service)
- **职责**：处理在线支付发起、异步回调及订单状态同步。
- **设计要点**：
  1. **开放式网关接口**：定义统一的 `PaymentGateway` Interface：
     ```go
     type PaymentGateway interface {
         Purchase(ctx context.Context, order *model.Paylist) (payURL string, err error)
         Notify(ctx context.Context, requestParams map[string]string) (responseStr string, err error)
     }
     ```
  2. **注册模式**：各支付通道（YftPay, ClonePay, F2FPay 等）实现该接口。利用工厂模式通过配置项动态调用，提升支付扩展能力。

### 3.4 定时任务微调度器 (Cron Job Orchestrator)
- **职责**：常驻后台，调度高频和低频定时任务。
- **设计要点**：
  1. **优雅退出**：结合 Go Context 和 `os/signal`，确保系统停止时，正在运行 of 定时任务（如大批量发公告邮件）能够优雅完成当前批次，不损坏数据一致性。
  2. **心跳防解析占用**：实现针对 32 天无心跳节点的 DNS 解析清理及 Cloudflare 记录同步清理，保持云服务配额整洁。

---

## 4. 后端迁移实施路线 (Migration Strategy)

迁移不应该一蹴而就，而需要分阶段渐进式进行，以控制风险并保证系统可用性：

### 第一阶段：数据库与核心定义对齐 (DB Alignment)
1. **Schema 复刻**：在 Go 中使用 GORM 的 `AutoMigrate` 或手写对应 Schema 的 Struct，严格保证字段类型对齐。
2. **红线规则继承**：
   - 流量统一使用 `int64` (BIGINT，单位字节)。
   - 密码验证严格继承旧版 SHA256 / Argon2 盐值比对机制，保证老用户能够平稳登录，不要求用户重置密码。
   - 财务处理：余额 `user.money` 映射为 `decimal.Decimal` (Go 中推荐 `github.com/shopspring/decimal` 库)；订单价格 `bought.price` 统一使用 `int64` (单位：分)，绝不允许出现浮点精度丢失。

### 第二阶段：重构三大 API 分组与独立运行
1. **Node API 重构**：最先重构面向代理节点的 API。因为这一块逻辑相对固定，重构后可以让节点直接连接 Go 后端，大幅减轻原有 PHP 服务器的 CPU 负载。
2. **订阅系统重构**：重构订阅解析与下发接口，提供超高性能的流量网关服务。
3. **用户 & 管理员 API 重构**：重构所有的管理与消费接口，剥离 HTML 渲染，全部改为干净的 JSON API 交互，为前端单页面应用奠定基石。

---

> **结语**：
> 通过将 SPanel 重构为 Go 后端，系统将摆脱 PHP 多进程的并发局限和庞杂的 runtime 依赖。利用 Go 的强类型安全、高性能 HTTP 引擎以及纯净的模块化分层，新的 **SPanel-Go** 将能够轻松承载十倍于从前的用户量，且系统维护性将得到本质的提升。
