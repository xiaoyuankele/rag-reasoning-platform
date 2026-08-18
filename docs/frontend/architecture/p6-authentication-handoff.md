# P6 个人用户认证前端交接

> 交接日期：2026-08-17。后端基线：`bb1e456`。验证码、注册、登录、当前用户、退出、忘记/重置密码和
> 全链路 OwnerScope 隔离均已实现并通过真实 HTTP/PostgreSQL 验收。前端第一批 Auth DTO/API、Auth Store、
> 公共认证页面和受保护应用外壳已经实现。2026-08-18 已通过真实 Go 后端、PostgreSQL、Mailpit 和 Vite
> `/api` 代理认证纵向联调；双用户产品隔离仍待 F2 文档管理界面可操作后完成，不能把认证闭环通过写成整个
> P6 产品闭环已经交付。

## 1. 这一阶段的核心目的

前端要把现有工作台从“隐含本地用户”升级为“有明确登录身份的个人工作台”：

```text
浏览器启动
  → GET /users/me 恢复身份
  → 已登录：进入受保护工作台
  → 未登录：进入登录/注册/忘记密码页面
  → 所有业务请求自动携带 HttpOnly Session Cookie
```

后端负责认证真实性、密码安全、Session、事务和数据隔离。前端负责表单、流程状态、页面导航和安全错误展示，
不得在浏览器重新实现后端密码哈希或所有者判断。

## 2. 已经可用的后端积木

| 用户动作 | HTTP 接口 | 成功结果 | 前端下一状态 |
| --- | --- | --- | --- |
| 申请注册验证码 | `POST /auth/verification-codes`，`purpose=register` | `202` 和挑战元数据 | 显示验证码输入与重发倒计时 |
| 注册 | `POST /auth/register` | `201`，设置 Cookie 并返回用户 | Auth Store 进入 `authenticated` |
| 登录 | `POST /auth/login` | `200`，设置 Cookie 并返回用户 | Auth Store 进入 `authenticated` |
| 恢复登录态 | `GET /users/me` | `200` 和当前公开用户 | Auth Store 进入 `authenticated` |
| 退出 | `POST /auth/logout` | `204`，清除 Cookie | Auth Store 进入 `anonymous` |
| 申请重置验证码 | `POST /auth/verification-codes`，`purpose=password_reset` | `202` 和挑战元数据 | 显示验证码与新密码输入 |
| 重置密码 | `POST /auth/password-reset` | `204`，撤销全部旧 Session | 清空 Auth Store 并跳转登录 |

后端不会返回原始 Session Token。浏览器只能通过 `Set-Cookie` 接收 `rag_session`，JavaScript 因 `HttpOnly`
不能也不应该读取它。

## 3. 请求与响应契约

### 3.1 申请验证码

```json
{
  "channel": "email",
  "destination": "learner@example.com",
  "purpose": "register"
}
```

重置密码时只把 `purpose` 改为 `password_reset`。当前真实本地渠道是 Mailpit 邮件；短信渠道保留在契约中，
但没有生产短信 Sender，第一版前端只开放邮箱。

`202` 响应：

```json
{
  "verification_id": 21,
  "expires_at": "2026-08-17T08:10:00Z",
  "resend_after": "2026-08-17T08:01:00Z"
}
```

前端必须保存 `verification_id`，后续注册或重置请求引用它；验证码明文只来自用户邮箱，不会出现在响应中。

### 3.2 注册

```json
{
  "verification_id": 21,
  "verification_code": "483921",
  "display_name": "learner",
  "password": "Password123"
}
```

### 3.3 登录

```json
{
  "identifier": "learner@example.com",
  "password": "Password123"
}
```

注册 `201` 和登录 `200` 共用响应形状：

```json
{
  "user": {
    "id": 17,
    "email": "learner@example.com",
    "phone": null,
    "display_name": "learner",
    "status": "active",
    "created_at": "2026-08-17T08:04:16Z"
  },
  "session_expires_at": "2026-08-24T08:04:16Z"
}
```

### 3.4 忘记/重置密码

先申请 `purpose=password_reset` 的验证码，再提交：

```json
{
  "verification_id": 29,
  "verification_code": "725184",
  "new_password": "Changed123"
}
```

成功只返回 `204 No Content`，没有 JSON。后端会原子更新密码、消费验证码并撤销该用户全部旧 Session；前端必须
清空本地用户状态并跳转登录页，不能把重置成功当作自动登录。

### 3.5 当前用户与退出

`GET /users/me` 返回：

```json
{
  "user": {
    "id": 17,
    "email": "learner@example.com",
    "phone": null,
    "display_name": "learner",
    "status": "active",
    "created_at": "2026-08-17T08:04:16Z"
  }
}
```

`POST /auth/logout` 幂等返回 `204`。即使 Cookie 已缺失，前端也可以安全地把本地状态切换为匿名。

## 4. 密码与验证码界面规则

- 密码长度为 8～128 个 ASCII 字符；
- 只能包含英文字母和数字；
- 必须同时包含大写字母、小写字母和数字；
- 验证码固定为六位数字；
- 前端校验用于即时提示，后端仍会独立校验，前端规则不能被当成安全边界；
- 倒计时以服务端 `resend_after` 为准，不自行固定假设 60 秒；
- `429` 响应存在 `Retry-After` 时应优先使用该响应头控制重试提示。

## 5. 前端状态机与模块边界

全局 Auth Store 只保存公开用户与状态：

```text
unknown
  ├─ /users/me 200 → authenticated(user)
  └─ /users/me 401 → anonymous

authenticated
  ├─ logout 204 → anonymous
  ├─ password-reset 204 → anonymous
  └─ 任意受保护 API 返回 authentication_required → anonymous
```

推荐积木：

```text
entities/user
  └─ PublicUser、DTO 转换

features/auth/api
  └─ requestVerificationCode、register、login、getCurrentUser、logout、resetPassword

features/auth/store
  └─ unknown / authenticated / anonymous 与当前用户

features/auth/ui
  └─ 登录、注册、忘记密码、验证码和新密码表单

app/router
  └─ 受保护页面导航，不承担真正鉴权
```

页面和组件不得直接散落 Axios 调用；API DTO 和错误转换集中在 `features/auth/api`，跨页面身份状态进入 Pinia。

## 6. HTTP、Cookie 与错误处理

- 开发环境继续通过 `/api` Vite 代理访问后端；同源请求由浏览器自动携带 Cookie；
- 若未来改为跨域 API，Axios 必须设置 `withCredentials: true`，后端也必须使用明确允许凭据的 CORS 配置；
- 前端不提交 `user_id`，文档归属只由后端 Session 决定；
- 所有改变状态的请求应保留浏览器 `Origin`，不要用前端技巧绕过后端同源校验；
- 展示错误时按稳定 `code` 分支，并保留 `X-Request-ID` 供排障，不依赖英文 `error` 文案做程序判断。

认证界面至少处理：

| 状态 | `code` | 前端行为 |
| --- | --- | --- |
| `400` | `invalid_verification_request` | 标记邮箱/用途请求不合法 |
| `400` | `invalid_auth_request` | 标记注册请求或密码规则不合法 |
| `400` | `invalid_password_reset_request` | 标记验证码 ID、验证码或新密码不合法 |
| `400` | `verification_code_invalid` | 提示验证码错误，不推测账号是否存在 |
| `400` | `verification_code_expired` | 提示重新申请验证码 |
| `401` | `invalid_credentials` | 登录页统一提示账号或密码错误 |
| `401` | `authentication_required` | 清空 Auth Store，跳转登录页 |
| `409` | `contact_already_registered` | 注册页提示改为登录或找回密码 |
| `429` | `verification_request_throttled` / `auth_request_throttled` | 禁用提交并按 `Retry-After` 倒计时 |
| `429` | `verification_attempts_exceeded` | 要求重新申请验证码 |
| `503` | `verification_channel_unavailable` | 提示邮件渠道暂时不可用 |
| `500` | `internal_error` | 显示通用错误和 `X-Request-ID` |

## 7. 前端实施顺序与验收

```text
Auth DTO 与 API Client
  → Auth Store 和启动 /users/me
  → 登录、退出、受保护应用壳
  → 注册与 Mailpit 验证码
  → 忘记/重置密码
  → 带登录态的 F2 文档管理
  → 双用户产品验收
```

最小验收必须证明：

1. 注册后刷新页面仍能通过 `/users/me` 恢复用户；
2. 退出后受保护页面回到登录页；
3. 用户 A 的文档不会出现在用户 B 的页面；
4. 重置密码后旧 Cookie 与旧密码都失效，新密码可以登录；
5. 注册验证码不能用于重置密码，重置验证码也不能用于注册；
6. 页面不会显示或记录密码、验证码、Cookie 和后端内部错误。

## 8. 当前非目标

- 本轮已实现认证页面和应用外壳，并完成本地 Mailpit 真实后端认证全流程，但尚未完成生产邮件和双用户产品验收；
- 生产邮件、短信、第三方 OAuth 和“保持登录”选择尚未实现；
- 团队工作区、成员角色、共享文档和租户切换属于 P7；
- 前端不能修改后端认证契约后再要求后端被动适配，契约变化必须先更新共享 API 文档。

权威接口总览见 [HTTP API 总览](../../shared/api/http-api-overview.md)，安全与数据隔离边界见
[P6 个人用户域](../../shared/architecture/personal-user-domain.md)，后端内部实现见
[P6 后端交接](../../backend/architecture/personal-user-backend-handoff.md)。

## 9. 2026-08-18 真实联调证据

- `POST /auth/verification-codes` 经 Vite `/api` 代理向 Mailpit 交付注册与密码重置验证码，均返回 `202`；
- 注册返回 `201` 并设置 `rag_session`，随后 `GET /users/me` 返回 `200` 与同一公开用户；
- 退出返回 `204` 并清除 Cookie，随后 `GET /users/me` 返回 `401 authentication_required`；
- 原密码重新登录返回 `200`；密码重置返回 `204` 并撤销旧 Session；
- 重置后旧 Cookie 与旧密码均返回 `401`，新密码登录与再次恢复当前用户均返回 `200`；
- 公开注册、忘记密码页面可直接访问；匿名访问受保护页面会携带原路径重定向到登录页；
- 真实浏览器表单已完成注册、刷新恢复、退出、原密码登录、密码重置、旧密码拒绝和新密码登录；
- 旧密码登录时界面正确显示中文统一提示和后端 `X-Request-ID`，没有把英文错误文本当作程序分支；
- 已登录刷新和 `/answer` 冷启动深链接均能恢复 Session、页面标题、内容与导航高亮；
- 联调只使用一次性测试账户，记录不保存邮箱、密码、验证码或 Cookie 内容；
- 类型检查、Lint、格式检查、8 个测试文件 23 个测试和生产构建再次通过。
