# SPanel-Go 后端路由与 API 设计规范文档

为将 SPanel 改造为纯净的前后端分离系统，后端路由将全面采用 RESTful API 设计规范，全部输入与输出均采用 JSON 格式，废弃所有原版的 `.tpl` 模板渲染路由。本文档详细阐述了 User 面板、Admin 面板以及面向节点 API 的接口设计。

---

## 1. 路由组与中间件设计 (Route Groups & Middleware)

Go 后端（以 Gin 为例）路由结构将划分为四大核心路由组，并绑定对应的中间件进行权限校验：

```mermaid
graph TD
    Router[SPanel-Go Router] --> PublicGroup[/api/v1/public]
    Router --> AuthUserGroup[/api/v1/user]
    Router --> AuthAdminGroup[/api/v1/admin]
    Router --> NodeGroup[/api/node /mu /mod_mu]
    
    PublicGroup --> GuestMiddleware[GuestMiddleware: 限制未登录访问]
    AuthUserGroup --> UserMiddleware[AuthMiddleware: 验证JWT / 限流]
    AuthAdminGroup --> AdminMiddleware[AdminMiddleware: 校验IsAdmin=1]
    NodeGroup --> NodeMiddleware[NodeMiddleware: 校验muKey / Token]
```

### 1.1 核心中间件设计

1. **JWT 校验中间件 (`AuthMiddleware`)**
   - 提取请求头 `Authorization: Bearer <JWT_TOKEN>` 或 URL 参数 `token`。
   - 验证 Signature，解析出 `UserID`。
   - 检验 Token 是否在 Redis 的活跃白名单中（若开启登录防异地/防IP滥用）。
   - **安全红线**：根据旧版 TODO 的安全升级，限制单账号 10 分钟内活跃 IP 数量不得超过 5 个。若超过，则在 Redis 中拉黑所有当前 Token，强制用户掉线。

2. **管理员校验中间件 (`AdminMiddleware`)**
   - 依赖 `AuthMiddleware` 解析出的 `UserID` 查询数据库，判断 `user.is_admin == 1`。如果不成立，返回 `403 Forbidden`。

3. **节点验证中间件 (`NodeMiddleware` / `MuMiddleware`)**
   - 提取请求头或 query 中的 `key` 或 `token`。
   - 与 `config.yaml` 中配置的 `muKey` 进行恒等比对。
   - 对于 Node V2 API，则需要提取 Token 进行基于数据库的 `user_token` 或 `ss_node` 表校验。

4. **PoW 防 CC 中间件 (`PowMiddleware`)**
   - 针对登录 (`/login`)、注册 (`/register`)、验证码发送 (`/send`) 等高敏感、重消耗的接口。
   - 后端生成并下发包含时间戳、难度、盐值的签名证明，前端通过浏览器原生 Web Crypto API 计算符合难度的 Hash。
   - **防重放**：后端校验成功后，将解题结果存入 Redis 锁定 15 分钟，防止多次重放攻击。

---

## 2. API 路由设计详情

### 2.1 公共与身份验证路由 (`/api/v1/public`)

本组路由不需要登录即可访问，但需要受 PoW 保护：

| 路由路径 | 请求方法 | 对应旧版 Controller | 功能描述 |
| :--- | :--- | :--- | :--- |
| `/auth/pow_challenge` | POST | `AuthController:powChallenge` | 下发无状态的 PoW 挑战题目与难度 |
| `/auth/login` | POST | `AuthController:loginHandle` | 用户登录，成功后分发 JWT |
| `/auth/register` | POST | `AuthController:registerHandle` | 用户注册 |
| `/auth/send_code` | POST | `AuthController:sendVerify` | 发送邮箱验证码 (白名单检查/频率限制) |
| `/auth/reset_password`| POST | `AuthController:handleReset` | 发起密码重置申请 |
| `/auth/reset_token` | POST | `AuthController:handleToken` | 使用 Token 完成密码重置 |
| `/res/captcha/:id` | GET | `ResController:captcha` | 图形验证码生成 (如极验 Geetest 异步校验) |

---

### 2.2 用户中心 API 路由 (`/api/v1/user`)

本组路由必须经过 `AuthMiddleware` 拦截，用于用户日常消费与配置管理：

| 路由路径 | 请求方法 | 对应旧版 Controller | 功能描述 |
| :--- | :--- | :--- | :--- |
| `/profile` | GET | `UserController:profile` | 获取当前用户的基本属性、套餐状态、流量报表 |
| `/checkin` | POST | `UserController:doCheckin` | 签到，获取随机流量奖励 |
| `/nodes` | GET | `UserController:node` | 获取可用节点列表，自动过滤无权限等级的节点 |
| `/nodes/unlock` | GET | `UserController:nodeUnlock` | 获取流媒体解锁信息 (Netflix, ChatGPT 等) |
| `/nodes/:id/ajax` | GET | `UserController:nodeAjax` | 获取单个节点的实时测速、负载与延时折线图数据 |
| `/shop/goods` | GET | `UserController:shop` | 浏览商品列表 (套餐时长、流量包) |
| `/shop/coupon` | POST | `UserController:CouponCheck` | 校验优惠券是否可用与计算折扣 |
| `/shop/buy` | POST | `UserController:buy` | 订购商品 (支持余额扣减) |
| `/tickets` | GET | `UserController:ticket` | 我的工单列表 |
| `/tickets` | POST | `UserController:ticket_add` | 创建新工单 |
| `/tickets/:id` | GET | `UserController:ticket_view` | 查看工单对话详情 |
| `/tickets/:id` | PUT | `UserController:ticket_update` | 回复/关闭工单 |
| `/invite` | GET | `UserController:invite` | 获取我的邀请码、返利记录、定制邀请码 |
| `/invite/buy` | POST | `UserController:buyInvite` | 购买额外的普通邀请次数 |
| `/invite/custom`| POST | `UserController:customInvite`| 定制个性化邀请链接 (扣减余额) |
| `/traffic` | GET | `UserController:trafficLog` | 获取详细的每日流量消耗日志 |
| `/security/ga` | POST | `UserController:GaSet` | 绑定 Google 二步验证密钥 |
| `/security/ga` | DELETE | `UserController:GaReset` | 重置 Google 二步验证 |
| `/config/password`| POST | `UserController:updatePassword`| 修改登录密码 |
| `/config/ssr` | POST | `UserController:updateSSR` | 修改连接密码、加密方式和混淆参数 |
| `/config/port` | POST | `UserController:ResetPort` | 随机重置连接端口 (根据配置扣减余额) |
| `/config/port_spec`| POST| `UserController:SpecifyPort`| 钦定/订制特定端口 |
| `/sub/reset` | GET | `UserController:resetURL` | 重置订阅 Token，使旧的所有订阅连接失效 |
| `/payment/pay` | POST | `Payment:purchase` | 调起第三方支付，生成支付链接/收银台页面 |

---

### 2.3 管理员面板 API 路由 (`/api/v1/admin`)

本组路由必须经过 `AuthMiddleware` 和 `AdminMiddleware` 双重拦截：

| 路由路径 | 请求方法 | 对应旧版 Controller | 功能描述 |
| :--- | :--- | :--- | :--- |
| `/overview` | GET | `AdminController:index` | 获取后台总览数据 (总用户数、今日流水、流量走势) |
| `/nodes` | GET | `Admin\NodeController:index` | 节点列表 (支持分页、筛选、状态展示) |
| `/nodes` | POST | `Admin\NodeController:add` | 新增节点 |
| `/nodes/:id` | PUT | `Admin\NodeController:update`| 编辑节点信息与参数 |
| `/nodes/:id` | DELETE| `Admin\NodeController:delete`| 删除节点 (同时清理关联的 DNS 解析) |
| `/users` | GET | `Admin\UserController:index` | 用户列表 (支持分页、筛选) |
| `/users/:id` | PUT | `Admin\UserController:update`| 精细修改用户参数 (等级、余额、限速等) |
| `/users/:id` | DELETE| `Admin\UserController:delete`| 物理注销并删除用户 |
| `/users/switch` | POST | `Admin\UserController:changetouser`| 快捷切换/模拟该用户身份登录面板 |
| `/tickets` | GET | `Admin\TicketController:index`| 待处理工单列表 |
| `/tickets/:id` | PUT | `Admin\TicketController:update`| 管理员回复或结案工单 |
| `/shops` | GET | `Admin\ShopController:index` | 商品档案列表 |
| `/shops` | POST | `Admin\ShopController:add` | 发布新商品 |
| `/orders` | GET | `Admin\ShopController:bought`| 全站商品订购与续费订单明细 |
| `/coupons` | GET | `AdminController:coupon` | 优惠券明细 |
| `/coupons` | POST | `AdminController:addCoupon` | 生成新的充值卡/折扣券 |
| `/announcements`| GET | `Admin\AnnController:index` | 公告发布与归档管理 |
| `/ips/block` | GET | `Admin\IpController:block` | 全局拦截与审计的 IP 列表 |
| `/ips/unblock` | POST | `Admin\IpController:doUnblock`| 手动移出 IP 阻断 |

---

## 2.4 面向节点的核心 API 路由 (`/api/node` & `/mu` & `/mod_mu`)

本组路由用于跟分布式代理节点通信，采用无状态的高性能路由设计，需严格遵循已有的协议定义：

#### 2.4.1 Mu API 接口 (兼容旧版节点端)
- `GET /mu/users`：拉取活跃用户列表（端口、密码、加密、混淆、UUID、流量配额）。
- `POST /mu/users/:id/traffic`：上报用户消耗的流量（上行 `u` / 下行 `d`）。
- `POST /mu/nodes/:id/online_count`：上报节点当前实时在线人数。
- `POST /mu/nodes/:id/info`：上报节点服务器的 CPU 负载、物理内存等系统状态。

#### 2.4.2 Node API V2 接口 (新版高内聚通信)
- `POST /api/node/register`：节点在面板完成自动注册登记，上报网卡带宽。
- `POST /api/node/config`：节点获取最新的动态运行配置（包括分流解锁配置 `node_unlock`）。
- `POST /api/node/status`：上报心跳包，并返回当前节点的流量配额状态（`traffic_used`, `traffic_left`, `traffic_used_daily`, `traffic_left_daily` 等）。
- `POST /api/node/nginx_config`：拉取该节点定制的 Nginx 反代配置。

---

### 2.5 客户端订阅生成路由 (`/link`)

本接口属于全站并发压力最高、吞吐量最大的核心网关：

- **路由路径**：`GET /link/:token`
- **处理流程**：
  1. 根据 `token` 查找 `user`，校验用户状态（`enable == 1` 且未过期）。
  2. 识别请求头 `User-Agent`，判断客户端类型（Clash, Surge, Sing-box, Quantumult X, Shadowrocket）。
  3. 读取数据库/缓存中用户所属 `node_group` 与等级 `class` 能够访问的合法节点集合。
  4. 读取 `subUrls` 多域名分发策略，根据不同的 host 使用对应的 `baseUrl` 替换邀请/订阅连接。
  5. 经过 Go 后端的 `subscription/service` 高效组装，最终渲染出适配该客户端的代理配置文档（YAML 或 Base64 纯文本），极速下发。

---

## 3. 数据类型与接口协议红线规范

前后端在数据传递与响应结构上，必须严格遵守以下原则：

### 3.1 统一 JSON 响应格式
所有用户与管理员 API 统一采用以下 JSON 承载格式：
```json
{
  "ret": 1, 
  "msg": "操作成功",
  "data": {
    "list": [],
    "total": 100
  }
}
```
- `ret == 1` 表示业务处理成功。
- `ret == 0` 表示业务失败，此时 `msg` 必须携带详细的简体中文错误提示（前端直接弹窗展示此错误信息）。
- **特例**：对于部分底层节点 API（如 `ssn_v2`），当验证 Token 失败时，需按照历史约定进行 **Silent Exit (直接断开不返回任何数据)**，以阻断探测。

### 3.2 字段类型对齐红线
1. **流量字段**：全站所有流量字段（`u`, `d`, `transfer_enable`, `traffic_used`）后端在序列化为 JSON时，必须保证为 **Number (Golang 的 int64 字节单位)**。
2. **金额与财务**：
   - 用户的钱包余额 `money` 采用 **String** 类型输出到前端展示（例如 `"10.00"`），避免浮点数在 JS 端处理时发生多位小数的精度溢出。
   - 后端数据库和订单事务中，涉及的 `bought.price` 必须使用 **Golang `int64` (分/Cents)** 进行原子锁计算。
3. **时间戳**：所有的操作日志与签到时间，后端统一以 **Unix 时间戳 (秒/int64)** 的数字格式输出，由前端负责本地化时间的格式化显示。
