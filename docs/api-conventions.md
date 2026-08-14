# API 约定

本文约束 HTTP transport 层：响应信封、handler 流程、错误映射、路由与流式响应。
本文出现的每个函数名都取自本仓真实源码——照抄即可编译。

## 统一响应信封

`internal/pkg/response`：

```go
type ResResult struct {
    Code    goerr.Code `json:"code"`
    Message string     `json:"msg"`
    Data    any        `json:"data"`
}
```

- `code = 0` 表示成功，非零为业务/错误码。
- 字段名是 `msg` 不是 `message`；这是前后端契约，不要改。
- `nil` data 序列化为空数组而非 `null`——由 response 包统一处理，handler 不管。
- 404、405 等不进任何路由组的响应也用同一个信封，客户端不必为它们写第二套解析。

## 响应 helper

只使用这些真实存在的函数（`internal/pkg/response`，惯例导入名 `resp`）：

| 函数 | 用途 |
|---|---|
| `resp.Ok(c, data)` | 成功 |
| `resp.Err(c, err)` | 错误，HTTP 状态码由 `goerr.Status` 决定 |
| `resp.OkErr(c, err)` | **HTTP 200 + 业务错误码**，用于"请求成功但业务被拒" |
| `resp.MapErr(c, err, mapper)` | 经 `ErrMapper` 把领域 error 映射成响应 |
| `resp.ErrWithData(c, err, data)` | 错误 + 额外元数据 |
| `resp.BindStructErr(c, err)` | 结构体绑定/校验失败 |
| `resp.BindErr(c, err)` | 绑定失败（非结构体） |
| `resp.BadRequestErr(c, msg)` / `resp.NotFoundError(c, msg)` / `resp.TooManyRequestsErr(c, msg)` / `resp.PayloadTooLargeErr(...)` / `resp.UnsupportedMediaTypeErr(...)` / `resp.GatewayTimeoutErr(...)` / `resp.NotAllowedMethod(...)` | 具体场景的快捷式 |

禁止：

- handler 手写散乱 JSON。
- handler 把底层错误字符串直接给客户端——`clientMessage` 会对文案做长度与内容清洗，绕过它等于绕过脱敏。
- 硬编码裸数字错误码。先看 `goerr` 已有的 `Status*()`，没有合适的再按其自定义状态 API 新增。

## 领域错误映射：`ErrMapper`

模块的领域 error 集中映射，**不散落在各个 handler 分支里**：

```go
// internal/module/user/transport/http/handler.go
var errMapper = resp.NewErrMapper(
    resp.ErrMapping{
        Target:  application.ErrUserNotFound,
        Status:  goerr.StatusNotFound(),
        Message: "用户不存在或已注销",
    },
)

// handler 里一行收口
if err != nil {
    resp.MapErr(c, err, errMapper)
    return
}
```

集中的好处是面向客户端的文案有单一来源：改一处即可，也能一眼看出这个模块对外暴露了哪些错误语义。
散在 handler 里的 `goerr.New(nil, ..., "字面量")` 做不到这一点——它们不会出现在任何一张表里。

## 对客文案与日志文案的分工

`goerr.Item` 有两个消息，去向不同：

| 方法 | 去向 | 内容 |
|---|---|---|
| `Error()` | 服务端日志（`response.logError` 记的就是它） | 含技术细节 |
| `Message()` | 响应体 `msg` 字段（经脱敏截断） | 面向用户，不含实现细节 |

`goerr.New(cause, status, msgs...)` 的行为决定了正确写法：

```go
msg := status.Msg()                    // 内置中文文案，如 StatusNotLoggedIn → "未登录"
if len(msgs) > 0 && msgs[0] != "" {
    msg = msgs[0]                      // 传了就覆盖**对客文案**
}
if cause != nil {
    msg = msg + ": " + cause.Error()   // 仅 Error() 变长，Message() 不变
}
```

因此：

```go
// ✅ 技术原因进 cause，对客用内置文案
goerr.New(fmt.Errorf("token parse failed: %w", err), goerr.StatusNotLoggedIn())
//   日志: "未登录: token parse failed: ..."
//   对客: "未登录"

// ❌ 两个参数都用反了
goerr.New(nil, goerr.StatusAuth(), "token parse failed")
//   日志: "token parse failed"   ← cause 为 nil，日志没有比对客更多的信息
//   对客: "token parse failed"   ← 技术细节泄漏给了调用方
```

**认证失败的对客文案不区分具体原因**（令牌缺失/格式非法/已过期/已撤销/主体不可用）。
区分等于告诉调用方他手里的令牌处于哪种状态——攻击者据此能判断一个令牌是「从未有效」
还是「曾经有效但已撤销」，后者说明这个账号真实存在且曾登录过。

授权失败不合并到「未登录」：那会让一个已登录但无权限的用户按提示重新登录，然后依然失败。

技术原因写成包内私有的 sentinel（不导出）——它们是日志内容，不是调用方需要 `errors.Is`
判断的契约。导出会让它们变成事实上的公开 API，而对外承诺一组「失败原因」恰好与上面那条相反。

## 错误码

错误码规范由 `github.com/gtkit/goerr` 提供并经 `goerr.ValidateCode` 强制，**本项目不另造一套**：

```
项目组代号(10) + 服务代号(01) + 模块代号(00~99) + 错误序号(00~99)
```

已占用的模块号段：`00` 通用、`01` Elasticsearch、`02` MySQL、`03` MongoDB、
`04` Redis、`05` Kafka、`06` RabbitMQ、`07` 订单。

`goerr` 的既有约定（同样适用于本项目）：

- HTTP 状态码语义化：业务逻辑错误 200，参数错误 400/422，认证 401，权限 403，
  资源不存在 404，冲突 409，限流 429，服务端/中间件错误 500，服务不可用 503。
- 客户端经 **HTTP 状态码**判断错误大类，经响应体 **code** 判断具体错误。
- **错误码一经发布不得复用**；废弃码保留占位，不允许回填新语义。
- 新增错误码优先复用已有语义，避免为同一失败场景创建多个近义码。

需要本项目专有的错误码时用 `goerr.NewStatus(code, httpCode, msg)` 新建，
选一个未占用的模块号段，不要挤进已有段。**不要写裸数字**。

## Handler 标准流程

```go
func (h *Handler) Update(c *gin.Context) {
    var req dto.UserUpdateRequest
    if err := ginx.BindBody(c, &req); err != nil {
        resp.BindStructErr(c, err)
        return
    }

    userID, ok := authenticatedUserID(c)
    if !ok {
        return  // helper 已写出响应（技术原因进日志，对客用内置文案）
    }

    data, err := h.service.UpdateProfile(c.Request.Context(), userID, application.UpdateCommand{
        NickName: req.NickName,
    })
    if err != nil {
        resp.MapErr(c, err, errMapper)
        return
    }

    resp.Ok(c, data)
}
```

固定流程：**绑定 → 取上下文身份 → DTO 映射 command → 调 application service → response 收口**。

### 绑定必须用 `ginx.BindBody`，不能用 `c.ShouldBind*`

`ShouldBind` / `ShouldBindJSON` / `ShouldBindWith` 被架构传感器禁止，`make check` 会失败。

原因：gin 的 `ShouldBind` 在 Content-Type 为 form 时会**同时**从 query string 与 body 取值填充结构体，
字段没有 form tag 时回退用字段名匹配。攻击者只要把 Content-Type 改成 form，
就能用 query 注入本该只来自 body 的字段。今天的 DTO 字段本就用户可控，注入拿不到额外收益——
但任何一个 DTO 将来加了不该由用户控制的字段（`Role` / `TenantID` / `IsAdmin` 这类），
它立刻变成真实漏洞，而加字段的人不会收到任何提示。

`ShouldBindQuery` 与 `ShouldBindUri` 不在此列：它们的数据来源本就是 query 与路径，语义明确。

### handler 里禁止

- 直接操作数据库 / import GORM / import repository（传感器强制）
- 编写复杂业务逻辑——放 application service
- 直接创建外部 SDK client——放 runtime 装配层
- 裸调 `c.ClientIP()`——用唯一 accessor（传感器强制），否则「日志里的 IP」与「被限流的 IP」会分叉

## 路由

RESTful 风格，版本号在 URL：

```
GET    /api/v1/users/:id
POST   /api/v1/orders/:id/pay      # 动作类用 POST
DELETE /api/v1/users/batch         # 批量用复数
```

注册方式用 gin 原生写法：

```go
// internal/module/<name>/transport/http/router.go
func Register(r *gin.RouterGroup, deps Deps) {
    handler := NewHandler(deps.Service)

    g := r.Group("/thing")
    g.Use(middleware.Timeout(5*time.Second, 2*time.Second))
    useRateLimit(g, deps.RateLimit, "default.ip", "default.user")
    g.Use(deps.Auth)                    // 需要鉴权就挂上

    g.GET("", handler.List)
    g.POST("", middleware.JSONBody(0), handler.Create)

    // 需要具名授权的路由单独分组：授权**必须排在认证之后**，
    // 因为它要的是已认证的身份。挂反了会得到"没有身份"而不是"没有权限"。
    admin := g.Group("", deps.Authorize("role.admin"))
    admin.DELETE("/:id", handler.Delete)
}
```

**访问控制由代码位置表达**：这条 `DELETE` 之所以要管理员，是因为它写在 `admin` 组里。
写在 `g` 上就是所有已认证用户都能删——而编译、`go vet` 与全部测试都不会有任何异常。
母版没有中央路由描述符来兜住这一点，新增路由时必须人工确认它在正确的组上。

然后在 `internal/bootstrap/providers.go` 的 `scaffold:modules` 标记处加一行。
`make module` 生成的模板已经是这个形状。

**新增路由必须人工确认它挂到了正确的组上。** 写在 `g` 外面就是公开接口，
而编译、`go vet` 与全部测试都不会有任何异常。本项目没有中央路由描述符机制，
访问控制由代码位置表达——这是选择 gin 惯用写法的代价。

### 具名授权的三态契约

授权实现返回 `(bool, error)`，三种取值的含义互不相同：

| 返回 | 含义 |
|---|---|
| `(true, nil)` | 允许 |
| `(false, nil)` | 明确拒绝 → 403 |
| `(_, non-nil)` | **依赖故障 → 503**，不得当作拒绝 |

第三条是关键：把「角色/权益数据源挂了」映射成 403，会让一次基础设施故障表现为
全体用户突然失去权限，而客户端收到的是一个正常的业务拒绝——既不会重试，
也不会触发任何告警。

两条配套约束：

- **依赖未注入时返回拒绝**，不是允许。装配缺失绝不能表现为放行。
- **查权威源，不采信令牌里的角色声明**。`Principal.Roles` 反映的是**签发那一刻**的事实，
  而令牌寿命可达数十天。仅采信它，等于把一次签发当成持续有效的高权限——
  一个被撤销权限的账号在令牌过期前仍然畅通无阻，而系统里没有任何地方能看出这件事。
  它可用于优化（先判否再查库），但不能单独作为放行依据。

判定需要缓存时加在 verifier 的实现侧，authorizer 与路由都不用改——
这正是把它定义成 port 的收益。**缓存的失效策略取决于「刚撤销的权限多久生效」
这个业务要求**，没有通用答案：设长了撤销不及时，设短了缓存形同虚设。

## 通用 HTTP 行为

以下语义对全部路由生效：

- 未匹配路径返回 404，未注册方法返回 405 并携带 `Allow` 头，两者共用同一 JSON 信封。
- 带尾斜杠的路径不做隐式重定向，按未匹配路由处置。
- `*gin.Context` 可直接当 `context.Context` 传给下游，请求取消与路由超时会一并传播。
- gin 运行模式由配置的 `env` 决定：`prod` 用 release、`test` 用 test、其余用 debug。

## 请求校验

- 用 gin binding tag 做基础校验。
- 更新接口用指针字段支持 partial update（区分「没传」与「传了零值」）。
- 列表接口给默认值和最大 page size。
- 复杂跨字段校验放 application 层，不塞进 handler。

## 中间件

### 全局（`internal/router/init.go`，顺序有测试锁定）

```
GinRecovery → RequestID → [otelgin] → GinLogger → Metrics
→ SecurityHeaders → ErrorHandler → [Cors] → MaxBytesLimit
```

方括号表示条件装配：`otelgin` 在开启 telemetry 时挂，`Cors` 仅当 `cors.allowed_origins` 非空时挂。

顺序不是随意的：Recovery 在最外层才能兜住后续全部中间件的 panic；RequestID 必须在日志之前，
否则日志里没有 ID；MaxBytesLimit 在最后，才能覆盖到不进任何路由组的 404/405 路径。

### 路由级

| 中间件 | 用途 |
|---|---|
| `middleware.Timeout(deadline, writeGrace)` | 协作式超时，handler 必须监听 ctx |
| `middleware.StreamTimeout(idleWrite)` | 流式响应的空闲写超时 |
| `middleware.JSONBody(maxBytes)` | 要求 JSON 并限大小；0 表示沿用全局上限 |
| `middleware.FormBody` / `MultipartBody` / `NoBody` | 其余三种请求体形态 |
| `middleware.NoStore()` | `Cache-Control: no-store`，挂在携带令牌/凭据的响应上 |
| `deps.Auth` / `deps.OptionalAuth` | 认证，由装配层注入 |
| `deps.Authorize(id)` | 具名授权，`id` 是注册表里的标识（`role.admin` / `entitlement.vip`） |

请求体形态必须显式声明：接受 JSON、接受 multipart 与「不接受请求体」三者的处置完全不同，
漏写会让中间件放行任意内容类型——一个只接受 JSON 的接口因此会收到 multipart，
而错误要到 handler 绑定失败时才浮现。类型不符返回 415，无 body 却带返回 400，超限返回 413。

路由级体积上限**只能比全局更严**：允许放宽会让全局上限形同虚设，
而全局上限正是那道不需要逐个路由审查就能成立的兜底。

## 流式响应的写超时

HTTP server 有一层保守的全局写超时兜底。普通路由受它保护——遗忘超时的代价从「无界挂起」
降为「保守截断」。

长连接必须**显式覆盖**它，手段是 `http.ResponseController.SetWriteDeadline`，
封装在 `middleware.StreamTimeout(idleWrite)` 里：设空闲写 deadline，每次写出后续期。

**「我需要长写」因此是一次显式决策，而不是默认状态。**

流式 handler 的一条硬约束：**后台写入的 goroutine 必须在 handler 返回前退出。**
gin 的 `c.Writer` 是 Context 的内部字段，handler 返回后归还对象池，
下个请求会把它重置到另一个响应上——晚退出一步写的就是别人的响应，
一次跨请求数据泄漏，且不报错、不进日志。
