# 脚手架母版使用说明

本仓库（`github.com/gtkit/gin-api`）是**孵化新项目的脚手架母版**。用它起一个新项目分两步：`gonew` 复制并改名 → `project init` 收尾。

## 前提

母版是私有托管，`gonew` 需要能解析它。使用方在环境里设置：

```bash
export GOPRIVATE=github.com/gtkit/*
# 若走内部 GOPROXY，另按团队约定配置 GOPROXY / GONOSUMCHECK
```

安装 gonew（Go 官方模板工具）：

```bash
go install golang.org/x/tools/cmd/gonew@latest
```

## Step A：gonew 复制并重写模块路径

```bash
gonew github.com/gtkit/gin-api@latest example.com/yourorg/your-project ./your-project
cd ./your-project
```

`gonew` 会把母版内所有 `github.com/gtkit/gin-api/...` 的 import 重写为你的新模块路径。此时项目已是一个**可编译的真实项目**（未做身份/清理收尾）。

## Step B：project init 实例化收尾

```bash
# 预览将发生的改动（不落盘）
make project-init NAME=your_project ARGS=--dry-run

# 实际执行
make project-init NAME=your_project
```

`project init` 与 `module` 是**开发期工具**，由 `scaffold` 构建约束门控——它们不在服务二进制里
（`module` 会重写 `providers.go`，`project init` 带 `--force-reinit-git` 且会永久丢弃 git 历史，
两者对已经跑起来的派生项目都不存在正确用法）。上面的 make 目标已把 tag 封装好；
裸命令形式是 `go run -tags scaffold . project init --name your_project`。

`project init` 会：

- 把 `--name` 写入 `project.mk`（应用身份的**唯一来源**：`APP_NAME` / `APP_DISPLAY_NAME`），
  再执行 `make gen-appmeta` 生成 `internal/appmeta`，并校验生成物与来源零差异——
  不直接正则改写生成产物，否则会形成第二个身份来源；
- 删除母版所属环境的专属部署入口（`.gitlab-ci.yml` 与 `deployment.sh`）。它们绑定母版环境的
  进程管理服务名、部署根路径与分支，不存在参数化后通用的形态，保留等于分发一条必然断裂的部署链——
  **派生项目需自备部署入口**；
- 以指定分支（`--branch`，默认 `main`）初始化 git 并建立首个提交；
- 收尾自检 `go build ./...` + `make check`。

**明确不做**：不修改 `Makefile` 的 `COVERAGE_MIN`。派生代码主体与母版相同，没有覆盖率下降的事实依据，
把门禁重置到更低的"起步地板"既无依据又违反覆盖率棘轮只升不降。派生项目直接继承母版已验证的门槛。

**参数**

| flag | 说明 |
|------|------|
| `--name` | 必填。须匹配 `^[a-z][a-z0-9_]{0,62}$`，在**任何文件被修改之前**校验 |
| `--branch` | 初始分支名，默认 `main`；由 `git check-ref-format --branch` 判定，不读 `init.defaultBranch` |
| `--dry-run` | 只预览不落盘、不动 git |
| `--force-reinit-git` | 允许在已有 git 仓库上执行，**会永久丢弃现有 git 历史** |
| `--no-commit` | 只初始化并暂存，不产生提交；该模式不要求 git 提交身份 |

**护栏**

- 项目名与分支名先于任何文件改动被校验（项目名会同时进入 Go 字符串字面量与 Make KV，
  未校验即为字面量破坏与 Make 表达式注入面）；
- **已有 git 仓库默认拒绝执行**——它会丢弃历史，而普通输入（gonew 产物）本来就没有 `.git`；
  确需重建时显式传 `--force-reinit-git`；
- `.git` 为普通文件（linked worktree）、符号链接或其他特殊类型时，**即使带 flag 也拒绝**；
- 工作区非纯净时拒绝执行；在母版自身（模块路径仍为 `github.com/gtkit/gin-api`）上拒绝执行；
- 执行前在项目根**之外**建立受影响文件与旧 git 的快照，任一阶段失败都逐字回滚
  （含权限位；成功重建时旧 `.git/info/exclude` 会在首个 `git add` 之前写回，
  保证本地忽略规则对首个提交依然生效）；
- 可重复执行（幂等）。

## 之后

- 用生成器加业务模块：`make module NAME=<name> ARGS="--field title:string ... --access authenticated"`（自动接线进分阶段 `providers.go`，见 `cmd/MODULE_GENERATOR.md`）。
- `user` 模块保留作为认证/参考起点（它是 `sharedauth.UserVerifier` 提供者），按新项目需要改造或替换。
- 提交前跑 `make check`；改动触及生成器/模板/`providers.go` 时再跑 `make check-ci`（额外含脚手架防腐冒烟 `check-generator`）。质量门禁全部在本地执行——部署脚本与 CI 配置是外部冻结方案，其中不含质量 job。

## JWT 密钥轮换操作手册

母版的 JWT 是**带 kid 的多密钥**体系：轮换不再让存量令牌批量失效。

**生成一组新密钥**（kid 必须是新的——同 kid 会被无条件拒绝）：

```bash
go run . jwt generate --kid k2026b --out-dir config/pem
```

**轮换步骤**：

1. 生成新 kid；
2. 把**当前** active 的公钥挪进 `jwt.previous`（只放公钥，不放私钥）；
3. 把新 kid 设为 `jwt.active`；
4. 重启进程。

重叠窗口（previous 保留多久）必须**覆盖旧令牌的剩余 TTL**，否则轮换仍会造成批量失效。默认 TTL 是 30 天，所以旧公钥至少保留 30 天。活跃用户会在刷新时自然迁移到新 kid。

**几条硬约束**：

- `previous` 最多 8 个：无 kid 的存量令牌只能逐个尝试，没有上限就是无界成本；
- 密钥集在**进程生命周期内不可变**，不做热重载——轮换靠重启；
- `jwt.unkeyed_token_accept_until` 是无 kid 存量令牌的**绝对** RFC3339 截止时刻，默认空即不接受。取值应为「切换时刻 + 旧令牌最大 TTL」，**迁移完成后删除该配置**；
- 它与 `jwt.laravel_accept_until` 是两个独立维度，分别删除。

## 限流的两条使用约束

**1. `store: local` 的多副本语义**：有效限额 = 配置值 × 副本数。它适合"高位防护上限"（挡住明显异常的调用量），不适合精确业务限额——"每账号每分钟 5 次登录"在 3 个副本上实际是 15 次，而那正是攻击者需要的余量。精确限额必须用 `store: redis`。

**2. `failure_mode: local_fallback` 只保证算法模型一致，不保证状态连续**：切换时 Redis 里的令牌计数不会迁移，本地影子桶从满桶起算，瞬时会多放行至多一个 Burst。不要据此做容量规划。

另：`ratelimit.key_hmac_secret` 的轮换语义是**桶全部重算 = 一次性限额重置**（不做双密钥灰度）。换密钥意味着所有主体重新拿到一个满桶，选在低峰期操作。

## 引入 WebSocket 端点时的强制约束

母版**不含任何 WebSocket 端点**，因此也没有连接注册表——按"无消费者不建机制"的原则，现在建它只是一个没人用的抽象。

但引入 WS 端点的**同一个 change** 必须同时补上两件事，否则关停会出现"假成功"：

1. **连接注册表**：`Hijack` 之后的连接**不受 `http.Server.Shutdown` 管理**——Shutdown 只等非 hijacked 的请求收尾。没有注册表的话，Shutdown 会正常返回，而 WS 连接还全都活着。
2. **draining 阶段独立 drain**：向每个连接发 close frame，超时后强制 `Close()`。它排在分相关停的哪一步取决于该端点是生产者还是消费者语义，需在该 change 里明确。

超时侧同样要注意：WS 走 `TimeoutStreaming`（可续期的写空闲超时），**不能**用 `TimeoutClientVisible`——那个 wrapper 会缓存响应且不透明，Hijack 拿不到底层连接。

## 移除 Laravel 兼容层（手动清单）

母版内置了 Laravel（tymon/jwt-auth）存量 token 的**单向兼容验证**，默认经 `cfg.JWT.Laravel.Enabled` 关闭。绿地新项目通常不需要它。`project init` **不会自动删除**（涉及多文件源码编辑，自动手术脆弱），请按下面清单手动移除：

1. 删除文件：
   - `internal/pkg/jwtauth/laravel.go`
   - `internal/pkg/jwtauth/laravel_test.go`
   - `internal/middleware/auth_laravel_test.go`
2. 简化 `internal/bootstrap/providers.go` 的 `newTokenParser`：去掉 Laravel 回退分支，直接 `return issuer, nil`（或让 `buildMiddlewares` 直接用 `runtime.TokenIssuer()`，并删除 `newTokenParser`）。
3. 删除 `internal/platform/config/types.go` 中的 `LaravelJWTConfig` 类型、`JWT.Laravel` 字段及其校验分支。
4. 清理相关测试用例：`internal/bootstrap/providers_token_test.go`、`internal/bootstrap/providers_phases_test.go` 中的 Laravel 回退开启态用例，`internal/platform/config/load_test.go` 的 `TestValidateLaravelJWTConfig`。
5. 跑 `make check` 确认全绿。

> 注意：代码里 `applog` / `data` / `initProcessRuntime` 只是现有基础设施（日志 / DAO / worker）的命名，**与 Laravel 无关，不要动**。

## 移除 OTel 预置（手动清单）

母版把 OpenTelemetry 定位为预置能力：默认经 `telemetry.enabled` 关闭，关闭时零构造、
零网络连接，只剩 no-op 记录点的调用开销。确认所有部署环境都永远不接 OTLP 收集端时，
才值得做这份减法。`project init` **不会自动删除**（涉及多文件源码编辑，自动手术脆弱），
请按下面清单手动移除：

1. 删除文件：
   - `internal/platform/telemetry/`（整包，含测试）
   - `internal/platform/data/tracing.go`、`internal/platform/data/tracing_test.go`
   - `internal/platform/config/telemetry.go`、`internal/platform/config/telemetry_test.go`
2. `internal/platform/config`：删 `types.go` 的 `Telemetry` 字段，以及 `validate.go` 中
   `validateTelemetryConfig` 的调用行。
3. `internal/bootstrap/providers.go`：删 2a 步（`telemetry.New` 装配块，含 `rb.push` 与
   启用日志）和 2a' 步（`InstrumentTracing` 调用）；删 `providerSet` 与 `infra` 两个
   结构体的 `telemetry` 字段及构造处的赋值；删 `buildEngine` 里 `TracingEnabled` /
   `ServiceName` 两行接线。2b 步 `metrics.New(otel.GetMeterProvider().Meter(metricsScope))`
   保留原样——`otel` 的 API import 随之保留。
4. `internal/bootstrap/app.go`：删 `App` 的 `telemetry` 字段、构造处赋值与关停序列里的
   `a.telemetry.Shutdown(ctx)`。
5. `internal/router/init.go`：删 otelgin import、`Options` 的 `TracingEnabled` 与
   `ServiceName` 字段（后者仅供 otelgin 使用），以及 `engine.Use(otelgin.Middleware(...))`
   所在的条件块。
6. 删除 `config/env.yml.example` 的「OpenTelemetry 导出」一段；实际部署环境的 `env.yml`
   同理。
7. 跑 `go mod tidy`：两个 OTLP exporter 与 otelgin / otelgorm / redisotel 三个埋点库随之
   消失。`go.opentelemetry.io/otel` 与 `otel/metric` 由 `internal/pkg/metrics` 的记录点
   保留（轻量 API 包，无 SDK、无网络连接）；`otel/sdk` / `otel/sdk/metric` 以测试依赖
   身份留在 go.mod——指标的反证测试用内存 reader 断言记录值，它们只被 `_test.go`
   import，不进生产二进制。
8. 跑 `make check` 确认全绿。

> 边界：`internal/pkg/metrics` 建在 OTel metric **API** 上，是 HTTP 请求量/时延/状态、
> 限流判定、worker 健康、DB 连接池、认证事件这些指标的记录点，本清单保留它——
> 摘除的是导出侧与链路追踪。连指标底座也要换（如直连 Prometheus client）属于重写
> `internal/pkg/metrics` 的工程，改动面是它的全部调用方
> （`grep -rl "internal/pkg/metrics" internal` 枚举）。

## 预置能力清单

逐项标注状态，避免"以为它已经在工作"或"以为它是示例可以删"两类误解。

### 【默认开启】开箱即生效，无需接线

| 能力 | 位置 | 说明 |
|------|------|------|
| 分层架构传感器 | `scripts/check-architecture.sh` | `make check` 的第一步；含路由注册唯一入口、客户端 IP 唯一 accessor 等规则 |
| 包级变量重绑定检查 | `scripts/check-package-vars.go` | 每处重绑定必须删除或在 `security`-风格的登记表中认领 |
| 应用身份单一来源 | `project.mk` + `make gen-appmeta` | `check-appmeta` 保证生成物零漂移 |
| 路由级超时 | `internal/middleware/routetimeout.go` | 三种 mode；两级预算（ctx + socket 写 deadline）；服务器层有保守写超时兜底，流式路由经 `ResponseController` 覆盖 |
| Panic 恢复 | `internal/middleware/recovery.go` | 全局第一道。普通 panic 返回统一 500；连接已断或响应已开始写出时只记日志不改写响应。**刻意不接告警**——请求级异常接告警会在一次流量异常里形成告警风暴，淹没进程级事件 |
| 请求 ID | `internal/middleware/requestid.go` | 进程前缀 + 原子计数；**不采信客户端传入值** |
| 安全响应头 | `internal/middleware/securityheaders.go` | 五类固定头；不发 HSTS（由 TLS 终止层负责） |
| 统一错误响应 | `internal/middleware/error.go` | 收集 handler 挂在 gin context 上的错误，转成与业务接口同一个 JSON 信封 |
| 校验错误中文消息 | `internal/bootstrap/validation.go` + `internal/pkg/response` | 绑定/校验失败的 422 `msg` 为中文、按对外 json 字段名呈现（多字段失败取字段名排序后的第一条）；启动阶段初始化翻译器（`gtkit/verify`），失败即启动失败——validator 首次校验即缓存字段名，晚初始化会让消息漏出 Go 字段名。非校验类绑定错误（如 JSON 语法错误）不经翻译路径 |
| 请求指标 | `internal/middleware/metrics.go` | 记录每请求的量/时延/状态码。recorder 为 nil 时返回直接放行的中间件——"没接指标"是正常路径，装配处不写判空。默认 no-op，导出见「接指标导出」 |
| CORS | `internal/middleware/cors.go` | **仅当 `cors.allowed_origins` 非空时才装配**；预检放行的请求头与响应声明的同源，避免浏览器放行服务端并不接受的头。允许方法取自 gin 在 405 路径上写好的 `Allow` 头（真实路由树匹配），不自建路由索引、也不硬编码——自建索引要在应用层重写一遍路由匹配，而两份匹配逻辑一旦在带参路由这类边界上不一致，表现就是「预检说路径不存在、实际请求却成功」。因此本中间件要求 engine 开启 `HandleMethodNotAllowed`；路径上自行注册了 OPTIONS 时，预检交给那个 handler |
| 可信代理边界 | `proxy.trusted_proxies` | **默认不信任任何代理**；部署在反代之后必须配置 |
| 认证与授权分离 | `internal/middleware/auth.go` + `internal/shared/auth` | 认证只判身份；授权依赖故障映射 503 而非 403。token **只经请求头承载**（`Authorization: Bearer` 为主，`App-Token` 为存量兼容）：query 会进访问日志、Referer 与浏览器历史，是长期泄漏面 |
| 限流契约 | `pkg/ratelimit` | 策略零值即拒绝；键基数达上限拒绝新建（不淘汰活跃桶） |
| JWT 密钥轮换 | `internal/pkg/jwtauth` | 令牌带 kid；未知 kid 直接拒绝（无回退遍历）；退役公钥仍可验签 |
| 请求体契约 | `internal/middleware/requestbody.go` | `JSONBody`/`FormBody`/`MultipartBody`/`NoBody` 四个中间件：类型不符 415、无 body 却带 400、超限 413；路由级上限只能比全局更严 |
| 全局请求体上限 | `internal/middleware/maxbody.go` | 兜底 404/405 等不进任何路由组的路径；声明长度与 chunked 两条路径都覆盖 |
| 有界 body 日志 | `internal/middleware/logger.go` | 默认关闭；开启时前缀有界、脱敏 |
| 配置严格解码 | `internal/platform/config` | 未知键即启动失败 |
| 配置环境变量覆盖 | `internal/platform/config/load.go` | YAML 之后加载 `APP_` 前缀变量，双下划线分层（`APP_DATABASE__PASSWORD` → `database.password`）；密钥可不落 YAML。`APP_` 是独占命名空间，对应不上配置键的变量即启动失败 |
| 数据库连接校验 | `internal/platform/data.OpenDB` | 实测 vendor/版本、会话时区/字符集/sql_mode |
| 注销信息留存 | `user_deactivations` 表 | 明文保存注销前手机号/邮箱/昵称/IP 供申诉与风控；**保留期限、到期清理与库账号读取权限须由使用方按所在司法辖区确定后再上线**，见该表的迁移注释 |
| 迁移体系 | `database/` + `make migrate-*` | 含基线迁移与 `make migrate-verify` |
| 就绪探针明细 | `internal/router/health.go` | `/ready` 按依赖分列状态与失败原因，错误详情截断至 256 字节；**响应体含内网地址与数据库账号名，该端点不得对公网开放**（见下节） |

### 就绪探针的暴露面约束

`/ready` 的响应体按依赖分列状态与**失败原因**：

```json
{
  "status": "unavailable",
  "checks": {
    "database": { "status": "unavailable", "error": "dial tcp 10.0.3.12:3306: connect: connection refused" },
    "redis": { "status": "ok" }
  }
}
```

依赖级状态沿用四态模型：required 依赖（database，以及 `auth.mode: local` 下的 redis）
失败是 `unavailable` + 503；optional 依赖失败是 `degraded` + 200，实例仍接流量。
整体 `status` 取最严重的那一项。

带错误原因是为了让排障止步于探针，不必每次都登进机器翻日志。代价是响应体会带出
内网拓扑与账号信息——`dial tcp 10.0.3.12:3306` 暴露内网地址与端口，
`Access denied for user 'appuser'@'10.0.3.12'` 连账号名一起给出。

**因此该端点只应对编排器与内网可达，不得暴露到公网。** 探针本就不该对公网开放，
带上错误详情后代价更高。反向代理侧按路径拦掉 `/ready` 的外部访问即可，
代码层不做限制：探针必须能被编排器无凭据访问，加鉴权会让它在正常部署中失效。

错误详情截断至 256 字节。`err.Error()` 的长度不受本项目控制——驱动可能把整条 SQL、
TLS 证书链或上游返回的 HTML 错误页放进去，而一个膨胀到下游拒绝解析的探针响应，
恰好在最需要它的时候失效。

### 认证：两种模式二选一（`auth.mode`，无默认值）

启动时必须显式选择，忘记选会直接启动失败。两种默认都会静默给出一个错误的系统：
默认 `local` 会给接外部身份的部署凭空加一组凭据端点；默认 `external` 会让人以为
认证已就绪，而实际上没有任何签发入口。

**`local` —— 本服务保管凭据**（母版的默认实现）

开箱提供 `POST /api/v1/auth/{register,login,refresh,logout}`：

| 能力 | 实现要点 |
|------|---------|
| 密码 | bcrypt，cost 可配（默认 12，下限 10）。账号不存在时**同样执行一次哈希比对**，否则响应时间会泄漏手机号是否已注册 |
| 访问令牌 | 复用既有 JWT 签发与密钥轮换。寿命由 `jwt.token_ttl_hours` 决定（唯一来源） |
| 刷新令牌 | 独立的随机串，不是 JWT——JWT 不可撤销，而刷新的核心需求正是可撤销。服务端存哈希，一次性使用并每次轮换 |
| 重用检测 | 已消费的刷新令牌再次出现 → **撤销整个令牌族**。仅拒绝这一次的话，攻击者仍持有链条上的其它令牌 |
| 登出 | 访问令牌 jti 进撤销表，存活时间等于令牌剩余寿命，因此撤销表不会无限增长 |
| 防爆破 | 复用限流：`auth.credential` 策略（按来源，比业务默认严得多） |

**要求可用的 Redis**：登出与刷新轮换都依赖它做撤销，缺了直接启动失败。
"能登录、登不出"比没有认证更危险——它看起来是完整的，没人会再去补。

**`external` —— 身份来自外部**

不注册任何认证端点，本服务只验签与授权。接线点：

1. `internal/pkg/jwtauth` 的 parser 换成你的 IdP 验签实现（或配置为接受其 JWKS）；
2. `sharedauth.UserVerifier` 改为查询你的用户同步表；
3. 撤销由 IdP 负责，`middleware.AuthOptions.Revocation` 留 nil。

母版**不预置任何具体 IdP 的对接**：Keycloak、Auth0、自研网关在 claims 形状、
用户同步时机与撤销通知方式上差异极大，预置任何一家都会让另外几家的使用者
先删掉再重写。

切到该模式后可整体删除：`internal/module/auth/`、`internal/runtime/module/auth/`、
`internal/repository/authcredential/`、`internal/pkg/{credential,tokenstore}/`、
配置里的 `auth.bcrypt_cost` / `auth.refresh_ttl` 与两条 `auth.*` 限流策略。

### 新增路由

用 gin 的方式写：

```go
// internal/module/<name>/transport/http/router.go
func Register(r *gin.RouterGroup, deps Deps) {
    handler := NewHandler(deps.Service)

    g := r.Group("/thing")
    g.Use(middleware.Timeout(thingDeadline, thingWriteGrace))

    // 声明受哪些限流策略约束。**不决定挂在认证的哪一侧**——
    // 那由策略自身的作用域决定，推导收口在装配层。
    preAuth, postAuth := stageRateLimits(deps.RateLimit, "/thing", "default.ip", "default.user")

    // 顺序是契约：来源限流挡的是尚未证明身份的流量，必须在认证之前；
    // 身份限流拿不到身份就退化成"每个请求都被拒"，必须在认证之后。
    g.Use(preAuth...)
    g.Use(deps.Auth)          // 需要鉴权就挂上
    g.Use(postAuth...)

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

然后在 `internal/bootstrap/providers.go` 的 `opt.RegisterModules` 里加一行
`xxxhttp.Register(v1, ...)`。`make module` 生成的模板已经是这个形状。

**新增路由时必须人工确认它挂到了正确的组上。** 写在 `g` 外面就是公开接口，
而编译、`go vet` 与全部测试都不会有任何异常——这是选择 gin 惯用写法的代价，
换来的是任何会 gin 的人不用先学一套只在本仓成立的写法。

可用的路由级中间件：

| 中间件 | 用途 |
|--------|------|
| `middleware.Timeout(deadline, writeGrace)` | 协作式超时，handler 必须监听 ctx |
| `middleware.StreamTimeout(idleWrite)` | 流式响应的空闲写超时（SSE） |
| `middleware.JSONBody(maxBytes)` | 要求 JSON 并限大小；0 表示沿用全局上限 |
| `middleware.NoBody()` | 拒绝携带请求体 |
| `middleware.NoStore()` | `Cache-Control: no-store`（`internal/middleware/nostore.go`）。挂在携带令牌/凭据的响应上——这类响应一旦进浏览器缓存、共享代理或 CDN，令牌的寿命就不再由服务端控制。头在 handler 之前设置，流式/提前写出的响应同样带上。母版已挂在四个认证端点上 |
| `deps.Auth` / `deps.OptionalAuth` | 认证，由装配层注入（`internal/middleware/auth.go`） |
| `deps.Authorize(id)` | 具名授权 |
| `stageRateLimits(deps.RateLimit, "/route", "策略ID"...)` | 路由级限流（`internal/middleware/ratelimit.go`），返回 `(preAuth, postAuth)` 两组，**分别挂在认证前后**。响应头用 `X-RateLimit-*` 而非 RFC 9331 的 `RateLimit-*`——前者是当前生态里客户端与网关实际在读的那一组 |

全局中间件（Recovery / RequestID / 日志 / 指标 / 安全头 / 统一错误响应 / CORS / 体积上限）
在 `internal/router/init.go` 里按固定顺序装配，顺序有测试锁定。

### 后台任务的单实例执行

多副本部署下，定时任务在每个实例上都会跑。`internal/pkg/singleton` 提供
基于 Redis 的租约，让每轮只有一个实例执行。

**注册时必须声明重复执行时靠什么保证正确**，零值会被拒绝：

| 声明 | 含义 |
|------|------|
| `GuaranteeIdempotent` | 重复执行无副作用差异（如批量 `DELETE ... LIMIT`，第二次影响 0 行） |
| `GuaranteeFenced` | 每个写操作携带 `Held.Fence` 序号，且**写路径拒绝比已见值更小的序号** |

这个约束不是形式主义。**锁不是正确性保证**：网络分区、进程长时间暂停（GC、
宿主机挂起）、租约过期后旧实例恢复，都会让两个实例同时在跑。声明强迫作者
在注册时就回答"重复执行会怎样"，而不是把问题留到事故现场。

两个容易混淆的概念：

- **随机 token** 只解决"别删掉/续期别人的锁"（compare-and-delete / compare-and-renew）；
- **fence 序号**才解决"租约过期后旧实例仍在写"。它必须单调递增，且由下游拒绝旧序号。

把随机 token 当 fencing 用是一个看起来能工作、实际毫无保护的组合：
两个随机值之间没有先后关系，下游无从判断谁更旧。

新增单实例任务：

```go
lease, err := singleton.NewLease(ctx, redisClient, prefix, "my-task", 2*time.Minute)
runner, err := singleton.NewRunner(lease, func(ctx context.Context, held singleton.Held) error {
    // 幂等任务：忽略 held.Fence
    // fenced 任务：把 held.Fence 传给写路径，并在那里 WHERE fence < :fence
    return doWork(ctx)
}, singleton.Config{
    Interval:   time.Hour,
    Guarantee:  singleton.GuaranteeIdempotent,
    MaxRuntime: 30 * time.Minute,
})
```

再用 `newSingletonWorker` 包成 Worker 注册进 `buildWorkers`。它的 `Prepare`
会在启动期用随机探测键真跑一遍抢锁/续租/释放——只 PING 不能证明当前 ACL
能执行脚本，也不能证明锁键与 fence 键在同一个集群 slot。

### 接指标导出

母版预置了配置门控的 OTel 导出（SDK + OTLP/HTTP exporter，metrics 与 trace）：
配 `telemetry.enabled: true` 加收集端 `endpoint` 即生效，**记录点一处都不用改**。
关闭时进程沿用全局 no-op provider，零构造、零网络连接。导出器已纳入关停序列，
flush 受 `telemetry.shutdown_timeout` 预算约束，且强制关停（第二个信号）会
提前放弃剩余 flush。

```yaml
telemetry:
  enabled: true
  endpoint: "otel-collector:4318"   # OTLP/HTTP，不带 scheme
  metrics: true
  traces: true
  sample_ratio: 0.1
```

用 Prometheus 拉取或云厂商专有协议的监控栈，在 `main.go` bootstrap 之前
自行设置全局 provider（`otel.SetMeterProvider`），记录点同样不需要改动。

**标签基数是硬约束**，加埋点时必须守住：路由标签用路由模板（`FullPath()`），
未匹配路由归到固定常量；禁止把原始 URL、用户 ID、IP、令牌、订单号写进标签。
序列数是标签取值的笛卡尔积——把请求方可控的值写进去，一次目录爆破就能把
监控后端压垮，而故障出现在监控侧：最需要它的时候正好不可用。

状态类指标（连接池用量、worker 最后成功时间）走**异步回调**，采集时读当前值。
主动上报要在"打得太密"与"错过变化"之间选，而一个每天跑一次的任务，
主动上报意味着它的指标一天只有一个数据点。

### 【注册位 — 需要接线才生效】

| 能力 | 如何启用 |
|------|---------|
| 后台任务 | 在 `providers.go` 的 `scaffold:module-workers` 标记前追加一行 `Worker` 构造 |
| 具名授权实现 | 在 `buildAuthorizers` 的 `scaffold:module-authorizers` 标记前注册；ID 与 `deps.Authorize(id)` 的实参对齐 |
| `role.admin` 授权 | 查 `user_roles` 表这一权威角色源（**示例，可按需删除**，见下节）。**未建表或表为空时一律拒绝**，因此不接入角色数据时行为与此前的恒拒绝占位一致。刻意不采信 JWT 的 role claim：令牌寿命可达数十天，仅凭它放行等于把「一次签发」当成「持续有效的高权限」。换成 IAM / IdP 同步表时只改 `providers.go` 里 `svc.roles` 的构造，authorizer 与路由都不用动 |
| 自定义校验规则 | 在 `internal/bootstrap/validation.go` 的 `validationOptions` 函数体内追加 `verify.WithTranslation`（新 tag + 校验函数 + 中文消息）/ `verify.WithValidationTranslation`（给既有 tag 配消息）/ `verify.WithStructValidation`（跨字段校验）。**只能在这里注册**：validator/v10 的注册与校验不能并发执行，handler 里注册是数据竞争 |
| 迁移专用凭据 | 配 `database.migrate_username`；不配则回退运行凭据并告警 |
| Laravel 存量 token 兼容 | 配 `jwt.laravel.enabled`；终态应删除，见下节清单 |
| 本地认证（`auth.mode: local`） | 注册/登录/刷新/登出四个端点，bcrypt 口令、刷新令牌轮换与重用检测、登出撤销。见下方「认证：两种模式二选一」 |
| 指标（OTel） | 默认 **no-op**：不开导出时每次记录都是空操作。已埋点：HTTP 请求量/时延/状态、限流判定（按策略与拒绝原因）、worker 连续失败与最后成功时间、DB 连接池、认证事件。配 `telemetry.enabled` 即经内置 OTLP exporter 导出，见下方「接指标导出」 |
| pprof | 配 `pprof.enabled`；**所有环境**都只允许字面回环 IP，绑定失败即启动失败（pprof 无鉴权，谁能连上谁就拿到堆栈与内存快照）；远程排障走 SSH 端口转发 |
| 限流 | 配 `ratelimit.policies` + `ratelimit.key_hmac_secret`，并在模块的 `stageRateLimits(deps.RateLimit, "/route", "策略ID"...)` 引用它（它返回 `(preAuth, postAuth)` 两组，须分别挂在认证前后） |
| 告警扇出（`internal/pkg/news`） | **有对外副作用，配置即生效，见下节**。配 `news.fsurl`（飞书/钉钉/企微 webhook）、`email.alert_to`、`sms.alert_phones`+`sms.alert_template`；三者各自独立，配哪个通哪个，全不配则无通道 |
| 写操作审计（`internal/pkg/auditlog` + `internal/middleware/audit.go` + `internal/repository/auditlog` + `operation_audit_logs` 表） | 母版不接线。装配四步：① `auditlog.New(repo, Options{})` 构造记录器；② 把 `recorder.Run` 注册为 consumer worker（HTTP 停止后 drain 存量）；③ 在需要审计的路由组（认证之后）挂 `middleware.OperationAudit(recorder.Enqueue)`；④ 保留期清理接 singleton 周期任务调 `repo.DeleteOlderThan`（保留期是合规决定，母版不定值）。**语义是尽力而为**：队列满载与落库故障丢弃并计数告警，不阻塞业务；不记请求体/响应体/query。不用时删除：包目录 ×3 + 中间件文件 + `internal/models/operation_audit.go` + 对应迁移 |
| 文件上传（`internal/pkg/upload` + `gtkit/gouploader`，版本由母版钉住） | 本地磁盘一行构造：`upload.NewLocalUploader(upload.LocalOptions{Dir, MaxFileSize, AllowedTypes})`——三项**必填无默认**（gouploader 自身默认 10GB 上限、空白名单全放行，母版纪律是安全决策必须显式）。返回的 Uploader 持有后台清理 goroutine，关停时 Close。对象存储/分片/STS 直传用 gouploader 原生 API，见下方「接入文件上传 / 对象存储」 |
| 上传文件回源安全（`internal/pkg/upload` + `internal/middleware/uploadheaders.go`） | 在上传文件的静态服务/下载路由组上挂 `middleware.UploadedFileHeaders(upload.DefaultPolicy())`：恒定 `nosniff`，内联白名单之外强制 `Content-Disposition: attachment`（浏览器只能下载不能渲染，挡存储型 XSS）。**零值 Policy 全部强制下载**；默认白名单刻意排除 `text/html` 与 `image/svg+xml`（可携带脚本），放宽是显式安全决策 |
| 验证码（`internal/pkg/verifycode`） | 母版不构造实例。装配处 `verifycode.New(ctx, redis客户端, sender, Options{})` 按渠道各建一个实例（短信/邮件用不同 `KeyPrefix`），sender 用闭包适配 `internal/pkg/sms`/`mail`。包内兑现：Lua 原子校验（命中即毁、尝试超限销毁）、SetNX 发送节流、Redis 键经 SHA-256 不落明文手机号/邮箱。**⚠ `Options.Debug` 是显式后门（固定码 + 只存不发），仅限本地/自动化测试装配，生产环境不得注入**——包内不做环境判断，责任在装配处 |
| 图形验证码（`internal/pkg/captcha`） | 母版不构造实例。装配处 `captcha.New(redis客户端, Options{})`，`Generate` 返回 ID + base64 图片（答案只落 Redis），`Verify` 一次性消费：无论对错同一 ID 只有一次校验机会，猜错必须换新图 |
| 出站 HTTP 客户端（`internal/pkg/httpclient`） | 在 `outbound.clients.<名字>` 下配 `base_url` 等，按名字取用。默认值：超时 10s、响应体上限 8 MiB、每主机 64 连接。**响应体上限不可省**——下游返回 HTML 错误页或被中间设备劫持时，没有上限就会被整个读进内存 |
| 熔断（`internal/pkg/breaker`） | 随出站客户端自动生效，阈值配 `outbound.breaker`（零值取保守默认：连续失败 5 次熔断、60s 后半开）。实例**按下游名各自独立**，一个下游的故障不会熔断另一个 |
| GET 响应缓存（`internal/pkg/httpcache`） | 配 `cache.enabled` + `cache.store`（redis/memory），并在只读路由上显式挂 `deps.Cache(0)`。**默认关闭且不提供全局开启路径**——响应缓存把一次响应的字节原样发给后续请求，挂到带身份的路由上就是把 A 的数据发给 B。开启后仍有四道防线：只对显式挂了中间件的路由生效、请求侧身份判据绕过、响应侧准入、命中回放前复检 |
| 任务队列（`internal/pkg/tasks`，asynq） | 配 `tasks.enabled` + `tasks.queues`（权重必须为正）+ `tasks.redis_db`（**必须同时出现在 `redis.dbs` 里**，否则队列按"redis 未配置"处理）。`shutdown_timeout` 必须小于容器 SIGKILL 宽限期，否则 drain 会被杀掉、在处理的任务既没完成也没重新入队 |
| 周期任务调度（`internal/pkg/tasks`，cron） | 配 `tasks.scheduler.enabled`（要求 `tasks.enabled` 也开着），并在 `providers.go` 的 `scaffold:periodic-tasks` 标记处追加 `tasks.PeriodicTask` 声明。**每条都必须声明重复投递时靠什么保证正确**，零值在构造期被拒绝——底层调度器没有任何跨实例协调，N 个副本就是每次触发投递 N 次；本包用 Redis 租约选主把它降到易主窗口内，但**消除不了**。触发时刻写在代码里而非配置里：配置里的 cron 可能指向一个没有处理器的任务类型，其表现是任务按时入队、一直重试到进死信队列。详见 `docs/workers-and-scheduling.md` |

### 告警扇出的对外副作用

`internal/pkg/news` 挂在 `gtkit/logger/v2` 的 Messager 上：**进程记一条告警级日志，就会向已配置的全部通道各发一条消息**——飞书/钉钉/企业微信 webhook、SMTP 邮件、阿里云短信。

因此它的误配置后果是**真实发送**，而不是一条错误日志：

- `email.alert_to` 非空即启用邮件通道，收件人会收到真实邮件；
- `sms.alert_phones` 与 `sms.alert_template` 成对配置即启用短信通道，号码会收到真实短信，**按条计费**；
- webhook 配错群地址，告警内容会发进那个群。

派生项目接手时应先确认这三处的取值，再让服务在有真实流量的环境启动。单次发送有 10s 独立超时（告警发生在日志调用路径上，通道故障不能拖住调用方），异步队列深度 64，超出即丢弃并计数——告警只承载进程级事件，积压几十条本身就已经是告警风暴。

### 【示例 — 可按需删除】

| 内容 | 说明 |
|------|------|
| `user` 模块 | 认证/参考起点，是 `sharedauth.UserVerifier` 的提供者。删除前需替换该提供者 |
| `users` 表的业务字段 | `vip_status` / `vip_level` / `vip_expire_at` / `follow` 来自存量业务，**不是通用母版该有的**。删除清单：① `internal/models/user.go` 的这四个字段与 `VipStatus` 类型；② `entitlement.vip` authorizer 与 `VIPVerifier`（`internal/bootstrap/providers.go`）；③ dto 里对应的响应字段；④ 基线迁移里的这四列。四处一起改——只删模型会让 authorizer 编译失败，只删 authorizer 会留下一套没人用的判定 |
| `entitlement.vip` authorizer | 会员资格判定示例；无 VIP 概念的项目可连同 `VIPVerifier` 一起删 |
| `role.admin` 的角色表实现 | 角色判定示例。删除清单：① `internal/models/user_role.go`；② `internal/repository/userrole/`；③ `internal/shared/auth` 的 `RoleVerifier`；④ `internal/bootstrap/authorizers.go` 的 `roleAuthorizer` 与 `adminRoleName`；⑤ `providers.go` 里 `services.roles` 字段及其构造，并把 `role.admin` 改回 `sharedauth.DenyAll(...)`；⑥ `database/migrations/*_create_user_roles_table.sql`。**⑤ 不能省**——只删实现会让 `role.admin` 未注册，而未注册的标识由装配层兜底为恒拒绝并告警，那是可启动的，但你会失去「为什么全是 403」的线索。它**不做**三件事：不引入策略引擎（Casbin 等）、不做角色缓存、不做角色层级与通配 |
| 三张表的基线迁移 | 与 `user` 模块配套；替换业务模型时一并重做 |
| 注销档案清理 worker | 与 `user` 模块配套的唯一内置业务 worker；未配置保留期时不注册。**它是单实例任务**（见下方「后台任务的单实例执行」），因此启用时 Redis 是硬依赖。删 `user` 模块时连同 `internal/module/user/worker` 与 `modules.user` 配置段一起删 |

### 【内部支撑包 — 不构成能力，无需接线】

这些没有"启用方式"：它们要么已经被上面某项能力在内部使用，要么是供派生项目按需 import 的工具类型。列出它们是为了让清单穷尽——使用者不会去找一个他不知道存在的东西。

| 位置 | 内容 |
|---|---|
| `internal/middleware/redact.go` | 日志脱敏的判定与替换，无导出构造函数，由访问日志与 panic 恢复在内部调用。敏感键分精确匹配与子串匹配两组——`code` 这类短名走精确匹配，否则会误伤 `zipcode`/`areacode` |
| `internal/pkg/errx` | 错误树判定谓词（`IsPureCancellation` / `IsPureAbort`）。逐成员判定聚合错误，避免"取消 + 回滚失败"被整体当成正常退出 |
| `internal/pkg/response` | 统一 JSON 响应信封与错误码到 HTTP 状态码的映射，被中间件与各模块 handler 使用 |
| `internal/pkg/ginc` | gin context helper（客户端 IP 唯一 accessor、请求上下文取值） |
| `internal/pkg/paging` | 纯分页类型（查询入参 `Query` 与结果元信息 `Paging`），不依赖 gorm，任何层可 import。执行分页查询的是 `gtkit/ormx/paginator`（依赖 gorm，application/dto/models 禁止 import）；`Query.Sort` 须经数据访问层白名单映射后才进 SQL |
| `internal/pkg/cache` | 进程内缓存工具：singleflight 防击穿、TTL 抖动防雪崩、容量上限 + LRU 防无界。供派生项目按需 import，母版自身不构造实例（GET 响应缓存走的是 `httpcache`，两者不是一回事） |
| `internal/pkg/console` | 带颜色的命令行输出 helper（`Success`/`Error`/`Warning`/`Exit`/`ExitIf`）。ANSI 转义自实现，不依赖第三方着色库；不做 TTY 检测，重定向到文件时同样写入转义序列 |
| `internal/pkg/log` | 进程级日志初始化：主实例（不缓冲）+ access 实例（缓冲）。application 层禁止 import 任何 logger 包，须定义 port 由 runtime 注入 |
| `internal/pkg/metrics` | OTel 记录点封装，默认 no-op（见上表「指标（OTel）」） |
| `internal/pkg/jwtauth` | JWT 签发/验签与多密钥轮换（见上文「JWT 密钥轮换操作手册」） |
| `internal/pkg/credential` / `internal/pkg/tokenstore` | 本地认证模式的口令哈希与刷新令牌存储（见「认证：两种模式二选一」；切到 `external` 后可整体删除） |
| `internal/pkg/singleton` | 基于 Redis 租约的单实例执行（见上文「后台任务的单实例执行」） |
| `internal/pkg/mail` / `internal/pkg/sms` | 邮件与短信客户端，当前唯一构造点在 `internal/pkg/news`（见上文「告警扇出的对外副作用」） |

配置里的 `application` / `ssl` / `cors` / `log` / `database` / `redis` / `env` 是进程与连接的基础配置，取值说明以 `config/env.yml.example` 的逐字段注释为准。

### 接入文件上传 / 对象存储

上传能力经 `github.com/gtkit/gouploader` 接入（**已在母版 go.mod，版本钉在 v1.7.0
并随门禁验证**）：对象存储抽象（内置本地磁盘与 S3 兼容实现）、分片断点续传、
STS 直传、大小与类型限制都在包内；云厂商 adapter 是独立 module，按需
`go get github.com/gtkit/gouploader/{aliyun,tencent,huawei,aws}`。

本地磁盘走仓内装配入口（三项必填，见注册位表）：

```go
uploader, err := upload.NewLocalUploader(upload.LocalOptions{
    Dir:          cfg.UploadDir,
    MaxFileSize:  32 << 20,
    AllowedTypes: []string{".jpg", ".png", ".pdf"},
})
// 进程关停时 uploader.Close()（它持有分片孤儿清理的后台 goroutine）
```

对象存储与 STS 直传直接用 gouploader 原生 API（`gouploader.New` +
`WithS3Storage` / 云厂商 adapter / `WithSTSProvider`），附加能力
（分片参数、回调、公开 URL 构造）经 `NewLocalUploader` 的 extra 参数
或原生 Option 透传。

HTTP 路由、鉴权与响应头由业务侧负责（gouploader 明确不做）：上传路由按
`docs/api-conventions.md` 挂 `middleware.MultipartBody(上限)`；**回源路由必须挂
`middleware.UploadedFileHeaders`**（见上方注册位表），否则上传的 HTML/SVG
会被浏览器内联渲染，构成存储型 XSS。

### 本地开发依赖（可选）

```bash
cp compose.env.example .env    # 填本机密码；.env 不入库
make dev-up                    # 拉起 MySQL/Redis 并等待健康
make dev-down                  # 停止，保留数据卷
make dev-reset                 # 删除数据卷（需二次确认）
```

**这套编排只用于本地开发与测试**，不是生产部署方案，也不替代已冻结的 `deployment.sh` / `.gitlab-ci.yml`。
端口只绑 `127.0.0.1`——开发库口令通常很弱，绑 `0.0.0.0` 会把它暴露到局域网。

### 供应链门禁

```bash
make security-source     # 日常：发布候选快照的漏洞与密钥扫描
make security-release    # 发版前：追加全历史密钥扫描，禁止跳过漏洞扫描
```

刻意**不并入 `make check`**：安全扫描依赖网络与 Docker，并入会让 `make check` 在离线环境失败，
最终逼人绕过整个门禁。`make tag` 会强制执行 `security-release`。

发布流程在 `scripts/release.sh`，**不写在 Makefile 配方里**：GNU make 的 `-n`（dry-run）
对含 `$(MAKE)` 的配方行不生效，把发布逻辑放在那里会让一次"我先看看会发生什么"的
dry-run 真实地改版本、提交、打 tag 并推送。`make check` 里的 `check-release-wiring`
每次都会证明发布入口是真实目标、且三道门禁确实被调用——而不是只看退出码。
