# 脚手架母版使用说明

本仓库（`github.com/gtkit/gin-api`）是**孵化新项目的脚手架母版**。用它起一个新项目分两步：`gonew` 复制并改名 → `project init` 收尾。

## 前提

母版与部分 `github.com/gtkit/*` 依赖是私有托管，`gonew` 和后续构建都需要访问权限。
直接从 Git 仓库获取私有模块时，在当前终端设置：

```bash
export GOPRIVATE='github.com/gtkit/*'
```

这个 `export` 只影响当前终端及其子进程。希望之后打开的终端也生效，可选择在 shell 启动文件中设置，
或使用 Go 的用户级持久配置：

```bash
go env -w GOPRIVATE='github.com/gtkit/*'
```

两种方式择一即可；进程环境中的同名变量优先于 `go env -w` 的配置。以上示例适用于原值为空、
或只需要这一个私有模块前缀的环境；已有其他组织的规则时，先用 `go env GOPRIVATE GONOPROXY GONOSUMDB`
查看，将 `github.com/gtkit/*` 合并到逗号分隔的清单，保留原有规则。

`GOPRIVATE` 决定哪些模块不使用公共代理和公共校验和数据库，**不授予仓库读取权限**。
直接访问时，Git 仍须通过团队批准的 HTTPS 凭据管理器或 SSH 方式完成认证，并拥有母版及依赖仓库的读取权限；
凭据不写进本仓库、模块地址或命令示例。使用 SSH 时，Git 的 URL 映射也应遵循团队配置。

默认情况下，`GONOPROXY` 和 `GONOSUMDB` 都回落到 `GOPRIVATE`；前者控制绕过代理，后者控制绕过公共校验和数据库。
若团队提供**同时托管公共和私有模块**的可信内部代理，则应按团队要求覆盖代理选择，例如：

```bash
export GOPRIVATE='github.com/gtkit/*'
export GOPROXY='https://proxy.example.com'
export GONOPROXY='none'
```

`proxy.example.com` 是占位地址，须换成实际代理；此例让私有模块也经内部代理下载。
未单独设置 `GONOSUMDB` 时它仍继承 `GOPRIVATE`；若已有显式值，须合并对应私有前缀。
代理的认证和上游仓库权限由团队配置。仅代理部分模块、允许回落的环境应使用团队给定的代理链，
不要直接套用这个单代理示例。这里的正确变量名是 `GONOSUMDB`，不是 `GONOSUMCHECK`。
详细语义见 [Go 私有模块文档](https://go.dev/ref/mod#private-modules)。

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

`project init` 是开发期工具，由 `scaffold` 构建约束门控，不在服务二进制里。
显式传入 `--force-reinit-git` 会重建 Git 仓库；上面的 make 目标已把 tag 封装好，
裸命令形式是 `go run -tags scaffold . project init --name your_project`。

`project init` 会：

- 把 `--name` 写入 `project.mk`（应用身份的**唯一来源**：`APP_NAME` / `APP_DISPLAY_NAME`），
  再执行 `make gen-appmeta` 生成 `internal/appmeta`，并校验生成物与来源零差异——
  不直接正则改写生成产物，否则会形成第二个身份来源；
- 删除母版所属环境的专属部署入口（`.gitlab-ci.yml` 与 `deployment.sh`）。它们绑定母版环境的
  进程管理服务名、部署根路径与分支，不存在参数化后通用的形态，保留等于分发一条必然断裂的部署链——
  **派生项目需自备部署入口**；
- 精确删除母版历史验收报告 `REVIEW.md`、`REVIEW-my.md`；保留 `pprof/` 工具箱和其他文档。
  dry-run、删除与失败回滚共用同一文件清单，失败时按原契约恢复字节、权限及存在性；
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

- 按业务需要实现模块，在 `internal/bootstrap/modules.go` 显式装配服务与路由，新增模型同步登记到 `cmd/migrate.go`。
- `user` 模块保留作为认证/参考起点（它是 `sharedauth.UserVerifier` 提供者），按新项目需要改造或替换。
- 提交前跑 `make check`；CI 使用相同的 `make check-ci` 入口。部署脚本与部署 CI 配置是外部冻结方案，其中不含质量 job。

## JWT 密钥轮换操作手册

母版的 JWT 使用**带 kid 的多密钥**体系。local 模式由 `gojwt` 签发；external 模式只加载 active/previous 公钥验签，不提供签发器、不读取私钥。Ed25519 私钥采用 PKCS#8 PEM（`PRIVATE KEY`），公钥采用 PKIX PEM（`PUBLIC KEY`），支持 Go `crypto/x509` 与 OpenSSL。

**生成一组新密钥**（kid 必须是新的——同 kid 会被无条件拒绝）：

```bash
go run . jwt generate --kid k2026b --out-dir config/pem
```

生成命令给私钥 0600、公钥 0644。local 模式的 `server` 在生产环境启动时会强制检查私钥权限，
Unix 下还会检查 active/previous 公钥不可被 group/other 写入；无需先运行 doctor 才生效。
dev/test 私钥权限分支较宽，doctor 的 Unix 检查仍严格。
NTFS 使用 ACL，不能用 Unix 权限位证明访问控制已经收紧；Windows 的 doctor 只检查存在性/普通文件。
**生产用密钥请在 Linux 上生成**；Windows 上的生成能力只供本地开发。
完整规则、符号链接及格式检查见 [JWT 文件权限与启动校验](config/README.md#jwt-文件权限与启动校验)。

**轮换步骤**：

1. 生成新 kid；
2. 把**当前** active 的公钥挪进 `jwt.previous`（只放公钥，不放私钥）；
3. 把新 kid 设为 `jwt.active`；
4. 重启进程。

重叠窗口（previous 保留多久）必须**覆盖旧令牌的剩余 TTL**，否则轮换仍会造成批量失效。`jwt.token_ttl_hours` 正值决定访问令牌寿命；<=0 时 local 默认 1 小时、external 默认 30 天。保留窗口须以旧令牌签发时的实际 TTL 为准。活跃用户会在刷新时自然迁移到新 kid。

**几条硬约束**：

- `previous` 最多 8 个：无 kid 的存量令牌只能逐个尝试，没有上限就是无界成本；
- 密钥集在**进程生命周期内不可变**，不做热重载——轮换靠重启；
- `jwt.unkeyed_token_accept_until` 是无 kid 存量令牌的**绝对** RFC3339 截止时刻，默认空即不接受。取值应为「切换时刻 + 旧令牌最大 TTL」，**迁移完成后删除该配置**；
- 它与 `jwt.laravel_accept_until` 是两个独立维度，分别删除。

## 限流的两条使用约束

**1. `store: local` 的多副本语义**：有效限额 = 配置值 × 副本数。它适合"高位防护上限"（挡住明显异常的调用量），不适合精确业务限额——"每账号每分钟 5 次登录"在 3 个副本上实际是 15 次，而那正是攻击者需要的余量。精确限额必须用 `store: redis`。

**2. `failure_mode: local_fallback` 只保证算法模型一致，不保证状态连续**：切换时 Redis 里的令牌计数不会迁移，本地影子桶从满桶起算，瞬时会多放行至多一个 Burst。不要据此做容量规划。

另：`ratelimit.key_hmac_secret` 的轮换语义是**桶全部重算 = 一次性限额重置**（不做双密钥灰度）。换密钥意味着所有主体重新拿到一个满桶，选在低峰期操作。

## Redis 故障与恢复

`RedisRequired` 是已启用能力的推导结果，不是忽略 Redis 错误的总开关。本地认证、任一
`redis + fail_closed` 限流策略、任务队列启用时，Redis 都是必需资源；其启动失败会阻止服务启动。
仅使用可降级能力时，已配置但不可达的 Redis 才允许降级启动。启用了 Redis 消费能力却没有配置
地址或所需 DB 编号属于配置错误，不按临时故障放行。

| 能力 | 启动时已配置但不可达 | 运行中不可用 | 恢复方式 |
|---|---|---|---|
| Redis 限流 `fail_open` | 若无其他必需能力，可降级启动并放行 | 后端故障时放行 | Owner 首次恢复后，原共享限流后端按既有健康阈值恢复 |
| Redis 限流 `fail_closed` | Redis 必需，启动失败 | 拒绝，现有中间件返回 429 | 由已有客户端的健康探测恢复；readiness 同时反映必需 Redis 状态 |
| Redis 限流 `local_fallback` | 若无其他必需能力，使用本地影子桶启动 | 切到本地影子桶 | Owner 首次恢复后，共享后端健康探测通过再切回 Redis，不迁移本地配额 |
| 本地认证撤销/刷新 | Redis 必需，启动失败 | 撤销查询失败返回 503，不把故障当成“未撤销”；刷新/登出不能视为成功 | 已有客户端恢复后重试；不提供撤销 fail-open 开关 |
| Redis GET 响应缓存 | 无其他必需能力时，暂时透传并等待恢复 | 读取失败回源，写入失败记录错误；读/写分别有 1s/2s 上限 | Owner 恢复后，已挂载路由延迟共用一个 Store；正常构造后后续请求可命中缓存 |
| 任务队列 | 启用时 Redis 必需，启动失败 | 入队错误返回调用方，处理/重试继续按队列原契约执行；必需 Redis 状态影响 readiness | 依赖恢复后按队列重试/调用方策略恢复，不承诺已失败入队自动补发 |

可选 Redis 的首次恢复由 Owner 统一管理：数据 tracing 就绪后等待 5 秒开始尝试，失败后从尝试结束
再等 5 秒；每次传入 10 秒 context 预算，实际返回还受客户端拨号/读写超时约束，不是 10 秒硬时限。
每次沿用配置和 redisx 默认值，完整客户端及全部实际 DB 的 tracing 就绪后一次发布；首次成功后不再替换客户端。
资源可用性仍需真实观测推进（连续失败 3 次降级、成功 2 次恢复），限流沿用自身的共享健康状态与冷却节奏。
未配置的 Redis 不启动恢复，必需 Redis 初始失败仍阻止启动，DB 声明错误仍由配置校验拒绝。

构造器异常、无效客户端、tracing 失败或候选清理失败会停止自动重试，并保留错误供诊断。
缓存延迟构造失败也只诊断一次并保持透传，不反复构造。关闭先封闭客户端发布，再取消并等待恢复任务；
某次关闭调用超时只代表该调用没有等到最终结果，不能当作资源已成功关闭。

`failure_mode` 必须显式填写，没有适用于所有业务的默认值。`local_fallback` 不迁移桶状态：
每个副本独立计额，切换后可能多放行一个 Burst，不能用作精确业务配额。
`auth.mode: external` 默认不在本服务做本地撤销查询，由 IdP 承担撤销；这与 Redis 故障时放行已有撤销检查不同。

## 引入 WebSocket 端点时的强制约束

母版**不含任何 WebSocket 端点**，因此也没有连接注册表——按"无消费者不建机制"的原则，现在建它只是一个没人用的抽象。

但引入 WS 端点的**同一个 change** 必须同时补上两件事，否则关停会出现"假成功"：

1. **连接注册表**：`Hijack` 之后的连接**不受 `http.Server.Shutdown` 管理**——Shutdown 只等非 hijacked 的请求收尾。没有注册表的话，Shutdown 会正常返回，而 WS 连接还全都活着。
2. **draining 阶段独立 drain**：向每个连接发 close frame，超时后强制 `Close()`。它排在分相关停的哪一步取决于该端点是生产者还是消费者语义，需在该 change 里明确。

超时侧同样要注意：WS 走 `TimeoutStreaming`（可续期的写空闲超时），**不能**用 `TimeoutClientVisible`——那个 wrapper 会缓存响应且不透明，Hijack 拿不到底层连接。

## CORS 请求头清理清单

当前请求头白名单位于 `internal/middleware/cors.go` 的 `allowedRequestHeaders` 和
`allowHeadersValue`，共八项。CORS 白名单只决定浏览器预检能否通过；允许一个头，
不表示认证中间件会读取它，也不表示该头传来的身份可信。

| 请求头 | 当前用途或已知边界 | 精简前确认 |
|---|---|---|
| `Content-Type` | 声明请求体类型，JSON API 使用 | 保留仍需跨域提交 JSON 等请求体的客户端支持 |
| `Authorization` | 默认 JWT 认证的主要载体，使用 `Bearer` | 保留默认认证路径；变更须同步全部客户端和网关 |
| `App-Token` | 默认 JWT 认证的存量兼容载体 | 所有旧客户端迁移到 `Authorization: Bearer` 后才可考虑移除 |
| `AccessToken` | 存量 CORS 放行项；默认 JWT 认证不读取 | 真实客户端/网关用途未确认，须核对后决定 |
| `X-CSRF-Token` | 存量 CORS 放行项；默认 JWT 认证不读取 | 核对派生项目是否另有 CSRF 中间件或客户端提交 |
| `Token` | 存量 CORS 放行项；默认 JWT 认证不读取 | 真实客户端/网关用途未确认，须核对后决定 |
| `X-Token` | 存量 CORS 放行项；默认 JWT 认证不读取 | 真实客户端/网关用途未确认，须核对后决定 |
| `X-User-Id` | 存量 CORS 放行项；默认 JWT 认证不以它建立身份 | 核对业务读取与网关注入；身份仍以验签结果为准 |

默认认证的实现见 `internal/middleware/auth.go`：同时提交 `Authorization` 与 `App-Token`
且值不一致会拒绝，同名头出现不一致的多个值也会拒绝。移除某个 CORS 放行项只会收窄浏览器跨域请求，
不会替代服务端认证逻辑。

派生项目要清理时，先核对网页、移动端和网关的实际请求头；再同步修改上述两个白名单、
`internal/middleware/cors_test.go`、`internal/middleware/cors_preflight_test.go` 及本文。
验证保留头的预检成功、删除头的预检被拒绝，并检查仍在支持期内的客户端。
本表记录当前状态，没有在母版删除任何请求头；外部客户端用途尚未确认的项不能据此直接删除。

## 移除 Laravel 兼容层（手动清单）

母版内置了 Laravel（tymon/jwt-auth）存量 token 的**单向兼容验证**，默认经 `cfg.JWT.Laravel.Enabled` 关闭。绿地新项目通常不需要它。`project init` **不会自动删除**（涉及多文件源码编辑，自动手术脆弱），请按下面清单手动移除：

1. 删除文件：
   - `internal/pkg/jwtauth/laravel.go`
   - `internal/pkg/jwtauth/laravel_test.go`
   - `internal/middleware/auth_laravel_test.go`
2. 简化 `internal/bootstrap/modules.go` 的 `newTokenParser`：去掉 Laravel 回退分支，直接 `return issuer, nil`（或让 `buildMiddlewares` 直接用 `runtime.TokenIssuer()`，并删除 `newTokenParser`）。
3. 删除 `internal/platform/config/types.go` 中的 `LaravelJWTConfig` 类型、`JWT.Laravel` 字段及其校验分支。
4. 清理相关测试用例：`internal/bootstrap/providers_token_test.go`、`internal/bootstrap/providers_phases_test.go` 中的 Laravel 回退开启态用例，`internal/platform/config/validation_test.go` 的 `TestValidateLaravelJWTConfig`。
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
   结构体的 `telemetry` 字段及构造处的赋值；在 `internal/bootstrap/modules.go`
   删 `buildEngine` 里 `TracingEnabled` /
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

## 按能力精简 gtkit 依赖

下面按当前源码导入列出裁剪入口，版本统一以 `go.mod` 为准。**运行时关闭能力仍会保留源码导入和模块依赖**；
`project init` 也不会自动裁剪这些能力。本节供派生项目决定裁剪范围。

AI 对话模块及其专用 SDK 已移除；升级时清理 `modules.ai` 和 `APP_MODULES__AI__*`，
历史 SQL 与现有数据保留，见 [数据库说明](database/README.md#ai-模块移除后的历史迁移)。

### 可按业务需求评估的能力

`taskqueue`、`upload`、`outbound`、`auditlog` 统一放在 `internal/optional/`，默认未接线。
每个目录自带启用和删除说明；设计约定见 [可选扩展目录](internal/optional/README.md)。
`internal` 限制跨项目导入，`optional` 区分可裁剪能力；包名仍直接使用功能名。

| 能力与 gtkit 模块 | 当前源码入口 | 保留条件与移除关联 |
|---|---|---|
| 上传：`gouploader` | `internal/optional/upload/` | 默认未接入；构造、关闭和路由接线均为 UPLOAD 注释。启用或删除按 [上传模块说明](internal/optional/upload/README.md) 操作，目录、限制与存储策略由业务显式提供。 |
| 机器人通知：`msgbot` | `internal/platform/notification/robot/` | 需要 webhook 或显式机器人目标时保留。`news.fsurl` 为空只会跳过默认目标，不代表不存在显式调用。移除需处理 `internal/bootstrap/resources.go` 的构造/关闭、dispatch 的 Robot 依赖与校验、`news` 配置和对应测试。 |
| 邮件：`tools/mailx` | `internal/pkg/mail/`、`internal/platform/notification/mail/` | 需要 SMTP 或邮件告警时保留。移除需处理通知装配/关闭、dispatch 的 Mail 依赖、`email` 配置校验和业务发信调用及测试。仅清空 `email.host` 不会移除模块依赖。 |
| 短信：`smsx`、`smsx/providers/aliyun` | `internal/pkg/sms/`、`internal/platform/notification/sms/` | 需要阿里云短信或短信告警时保留。移除需处理通知装配/关闭、dispatch 的 SMS 依赖、`sms` 配置校验和业务短信调用及测试。仅清空 `sms.provider` 不会移除模块依赖。 |
| 异步队列与调度：`asynqx` | `internal/optional/taskqueue/` | 默认未接入主程序；目录外接线均为 TASKQUEUE 注释。启用或删除按 [taskqueue 说明](internal/optional/taskqueue/README.md) 操作。普通主程序构建不编译它，`go build ./...` 仍会编译该可选包。 |
| 出站 HTTP、重试与熔断：`httpc`、`gobreaker` | `internal/optional/outbound/` | 默认未接入，配置和初始化均为 OUTBOUND 注释，见 [启用说明](internal/optional/outbound/README.md)。 |
| 写操作审计 | `internal/optional/auditlog/` | 记录器、仓储、模型、中间件及 SQL 都在目录内；默认不注册审计表，见 [启用说明](internal/optional/auditlog/README.md)。 |
| HTTP 响应缓存：`gincache`、`gincache/persist/ristrettoadapter` | `internal/pkg/httpcache/`、`internal/platform/responsecache/` | 需要显式挂载的响应缓存时保留。移除需核对 bootstrap/runtime 缓存能力、路由挂载、`cache` 配置与测试。`internal/pkg/cache/` 是另一项本地缓存封装，不能按名字一起删。 |
| 内置迁移 CLI：`migratex` | `cmd/migrate.go` | 使用内置 migrate 命令时保留。移除前须有替代迁移流程，并核对命令注册、`internal/bootstrap/migrateruntime.go` 与迁移测试；迁移文件和数据库历史仍须保留、审阅。 |

通知渠道的所有权说明见 [通知渠道与替换方式](docs/observability.md#通知渠道与替换方式)：
dispatch 和 logging 借用发送能力，实际渠道由各自 Owner 释放。裁剪某渠道必须同时处理创建和关闭引用，
不能只删 `go.mod` 条目或从 `processLogs` 去掉一个字段。

### 默认基础依赖

下列模块已经用于默认服务链路，精简可选能力时应保留；替换它们属于对应能力的独立改造。

| gtkit 模块 | 当前用途与主要入口 |
|---|---|
| `logger/v2` | `internal/platform/logging/` 的日志实例与排空，`internal/pkg/log/` 为项目门面 |
| `gojwt` | `internal/pkg/jwtauth/` 的签发、验签、claims 与 PEM 工具；切换 `auth.mode` 不会自动替换验签器 |
| `ormx` | `internal/platform/mysql/open.go` 的 MySQL 装配及仓储分页 |
| `redisx` | `internal/platform/redis/open.go` 的 Redis 装配；Redis 是否必需由已启用能力决定 |
| `golimit/v2` | `pkg/ratelimit/local.go` 的本地限流；选择 Redis 存储不表示本地能力可以直接删除 |
| `ginx` | auth HTTP handler 的绑定/响应辅助 |
| `goerr` | 中间件、响应、事务及缓存的错误处理 |
| `verify` | `internal/bootstrap/validation.go` 的校验注册与中文消息，以及响应校验 |
| `json/v2` | 进程 JSON 后端及模型 |

### 裁剪顺序

1. 先确定保留能力和兼容范围，再从上表入口查全部调用方，包括 `_test.go`、构建标签和派生项目代码。
   例如核对文件上传和短信 SDK 引用：

   ```bash
   rg -n --glob '*.go' 'github\.com/gtkit/(gouploader|smsx)' .
   ```

2. 在单独获准的代码变更中同步处理装配、功能源码、配置字段/校验/示例/说明和对应测试。
   配置还被其他能力使用时保留共享部分；部署数据及历史迁移另行确认。
3. 所有残留导入处理完后再运行 `go mod tidy`，回读 `go.mod`/`go.sum` 差异。
   间接依赖能否消失取决于剩余依赖图，不能逐项保证；可用 `go mod why -m <完整模块路径>` 查保留原因。
4. 按裁剪范围验证保留功能、默认构建和受影响的构建标签；受控检查按项目授权规则执行。
   有本机 `go.work` 时还要核对 `GOWORK=off` 的独立模块结果，避免本地替换掩盖缺失依赖。

## 预置能力清单

逐项标注状态，避免"以为它已经在工作"或"以为它是示例可以删"两类误解。

> **交付平台是 Linux 容器**（`Dockerfile` 与 `make build` 都固定 `GOOS=linux`），CI 也只跑
> `ubuntu-latest`。Windows 与 macOS 是开发平台，下列能力里依赖 Unix 专有语义的部分在
> Windows 上不成立：**文件权限位**（NTFS 用 ACL，Go 恒报 0666/0777）、**信号投递**
> （`Process.Signal` 只支持 Kill）、**符号链接**（需特权）、**目录 fsync**
> （`FlushFileBuffers` 不接受目录句柄）。
>
> 测试侧由 `internal/testutil` 的 `RequireUnixFilePerm` / `HasUnixFilePerm` /
> `RequireSignalDelivery` / `RequireSymlink` 显式声明，不具备时以带理由的 SKIP 呈现，
> 而不是让 `go test ./...` 在开发机上无故变红——那种红色会让整份测试输出失去信号价值。
> 生产侧按平台分流（`internal/diagnostics/keyperm_*.go`、`cmd/syncdir_*.go`）。
>
> **首次 clone 后请确认工作区行尾是 LF**。仓库用 `.gitattributes` 固定 `eol=lf`；
> 从更早版本升级的 clone 需执行一次 `git add --renormalize .`（先提交手头改动）。
> CRLF 工作区会让 `golangci-lint` 报出纯行尾的 gofmt 差异。

### 【默认开启】开箱即生效，无需接线

| 能力 | 位置 | 说明 |
|------|------|------|
| 分层架构传感器 | `scripts/check-architecture.sh` | `make check` 的第一步；含路由注册唯一入口、客户端 IP 唯一 accessor 等规则 |
| 包级变量重绑定检查 | `scripts/check-package-vars.go` | 每处重绑定必须删除或在 `security`-风格的登记表中认领 |
| 文档引用存在性检查 | `scripts/check-doc-refs.go` | 跟踪的 `*.md` 里的链接与反引号裸路径必须解析到真实路径；解析不到要么修好、要么在登记表中认领，失效的登记同样失败 |
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
| 数据库连接校验 | `internal/platform/mysql/open.go` 的 OpenDB | 实测 vendor/版本、会话时区/字符集/sql_mode |
| 迁移体系 | `database/` + `make migrate-*` | 含基线迁移与 `make migrate-verify` |
| 就绪探针明细 | `internal/router/health.go` | `/ready` 按依赖分列状态，失败时使用固定安全文案；原始原因进入服务端限频日志，该端点仍需限制访问（见下节） |

### 就绪探针的暴露面约束

`/ready` 的响应体按依赖分列状态，失败时返回固定文案：

```json
{
  "status": "unavailable",
  "checks": {
    "database": { "status": "unavailable", "error": "dependency check failed" },
    "redis": { "status": "ok" }
  }
}
```

依赖级状态沿用四态模型：required 依赖（database，以及 `auth.mode: local` 下的 redis）
失败是 `unavailable` + 503；optional 依赖失败是 `degraded` + 200，实例仍接流量。
整体 `status` 取最严重的那一项。

原始依赖错误仅进入服务端日志：首次失败、错误变化和恢复立即记录，同类持续失败
最多每 30 秒记录一次。客户端和监控应按 HTTP 状态码及结构化状态判断，不解析错误原文。
绑定错误保持 HTTP 422；已初始化的校验翻译器继续提供中文字段提示，其余绑定错误统一返回
`请求参数格式错误`，原始错误仍保留为服务端诊断 cause；请求体超限仍优先返回 413。

**该端点只应对编排器与内网可达，不得暴露到公网。** 反向代理侧按路径拦掉 `/ready` 的外部访问即可，
代码层不做限制：探针必须能被编排器无凭据访问，加鉴权会让它在正常部署中失效。

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

不注册任何认证端点，本服务只验签与授权。默认 Ed25519 验签只要求 active/previous 公钥；
`jwt.active.private_key_path` 可留空，即使填写也不会加载。`jwt generate` 和 `jwt reencode` 仍按各自原规则处理完整密钥对。
接线点：

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

然后在 `internal/bootstrap/modules.go` 的 `opt.RegisterModules` 里加一行
`xxxhttp.Register(v1, ...)`。

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

### 后台定时任务

可选队列保存在 `internal/optional/taskqueue/`，默认接线和配置段均已注释。
先按 [启用说明](internal/optional/taskqueue/README.md) 恢复接线，再在
`internal/bootstrap/taskqueue.go` 中追加 `taskqueue.PeriodicTask`，
配置 `tasks.enabled` 和 `tasks.scheduler.enabled`。
固定间隔的本地工作可直接使用监听取消的 `time.Ticker` 循环。
注册方式及关停行为见 [后台任务与调度](docs/workers-and-scheduling.md)。

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
| 后台任务 | 在 `modules.go` 的 `buildWorkers` 中追加 `Worker` 构造 |
| 具名授权实现 | 在 `buildAuthorizers` 中注册；ID 与 `deps.Authorize(id)` 的实参对齐 |
| `role.admin` 授权 | 查 `user_roles` 表这一权威角色源（**示例，可按需删除**，见下节）。**未建表或表为空时一律拒绝**，因此不接入角色数据时行为与此前的恒拒绝占位一致。刻意不采信 JWT 的 role claim：令牌寿命可达数十天，仅凭它放行等于把「一次签发」当成「持续有效的高权限」。换成 IAM / IdP 同步表时只改 `internal/runtime/module/account/module.go` 里的角色源构造，authorizer 与路由都不用动 |
| 自定义校验规则 | 在 `internal/bootstrap/validation.go` 的 `validationOptions` 函数体内追加 `verify.WithTranslation`（新 tag + 校验函数 + 中文消息）/ `verify.WithValidationTranslation`（给既有 tag 配消息）/ `verify.WithStructValidation`（跨字段校验）。**只能在这里注册**：validator/v10 的注册与校验不能并发执行，handler 里注册是数据竞争 |
| 迁移专用凭据 | 配 `database.migrate_username`；不配则回退运行凭据并告警 |
| Laravel 存量 token 兼容 | 配 `jwt.laravel.enabled`；终态应删除，见下节清单 |
| 本地认证（`auth.mode: local`） | 注册/登录/刷新/登出四个端点，bcrypt 口令、刷新令牌轮换与重用检测、登出撤销。见下方「认证：两种模式二选一」 |
| 指标（OTel） | 默认 **no-op**：不开导出时每次记录都是空操作。已埋点：HTTP 请求量/时延/状态、限流判定（按策略与拒绝原因）、worker 实际执行次数/耗时、DB 连接池、认证事件。执行指标覆盖 OneShot 和队列处理尝试，具体口径见 `docs/observability.md`。配 `telemetry.enabled` 即经内置 OTLP exporter 导出，见下方「接指标导出」 |
| pprof | 配 `pprof.enabled`；**所有环境**都只允许字面回环 IP，绑定失败即启动失败（pprof 无鉴权，谁能连上谁就拿到堆栈与内存快照）；远程排障走 SSH 端口转发 |
| 限流 | 配 `ratelimit.policies` + `ratelimit.key_hmac_secret`，并在模块的 `stageRateLimits(deps.RateLimit, "/route", "策略ID"...)` 引用它（它返回 `(preAuth, postAuth)` 两组，须分别挂在认证前后） |
| 告警扇出（`internal/pkg/news`） | **有对外副作用，配置即生效，见下节**。配 `news.fsurl`（飞书/钉钉/企微 webhook）、`email.alert_to`、`sms.alert_phones`+`sms.alert_template`；三者各自独立，配哪个通哪个，全不配则无通道 |
| 写操作审计（`internal/optional/auditlog`） | 默认未接入；按 [审计模块说明](internal/optional/auditlog/README.md) 恢复 AUDITLOG 注释接线、选择业务写路由并接入审计表迁移。记录器按 consumer 顺序排空，队列满载或写入失败时丢弃并告警，不记录请求体、响应体或 query。不需要时可直接删除目录。 |
| 文件上传（`internal/optional/upload` + `gtkit/gouploader`，版本由母版钉住） | 本地磁盘一行构造：`upload.NewLocalUploader(upload.LocalOptions{Dir, MaxFileSize, AllowedTypes})`——三项**必填无默认**（gouploader 自身默认 10GB 上限、空白名单全放行，母版纪律是安全决策必须显式）。返回的 Uploader 持有后台清理 goroutine，关停时 Close。库的后台错误（清理失败、钩子 panic、降级放行、启动期安全建议）默认已接进 `logger/v2`，可经 extra 传 `WithOnBackgroundError` 取代。对象存储/分片/STS 直传用 gouploader 原生 API，见下方「接入文件上传 / 对象存储」 |
| 上传文件回源安全（`internal/optional/upload` + `internal/optional/upload/headers.go`） | 在上传文件的静态服务/下载路由组上挂 `upload.FileHeaders(upload.DefaultPolicy())`：恒定 `nosniff`，内联白名单之外强制 `Content-Disposition: attachment`（浏览器只能下载不能渲染，挡存储型 XSS）。**零值 Policy 全部强制下载**；默认白名单刻意排除 `text/html` 与 `image/svg+xml`（可携带脚本），放宽是显式安全决策 |
| 验证码（`internal/pkg/verifycode`） | 母版不构造实例。装配处 `verifycode.New(ctx, redis客户端, sender, Options{})` 按渠道各建一个实例（短信/邮件用不同 `KeyPrefix`），sender 用闭包适配 `internal/pkg/sms`/`mail`。包内兑现：Lua 原子签发与重置计数、原子校验（命中即毁、尝试超限销毁）、SetNX 发送节流、Redis 键经 SHA-256 不落明文手机号/邮箱。状态变更脚本禁止不确定结果的自动重试，仅在 NOSCRIPT 时重载；超时后由调用方处理未知结果，不自动重发验证码。**⚠ `Options.Debug` 是显式后门（固定码 + 只存不发），仅限本地/自动化测试装配，生产环境不得注入**——包内不做环境判断，责任在装配处 |
| 图形验证码（`internal/pkg/captcha`） | 母版不构造实例。装配处 `captcha.New(redis客户端, Options{})`，`Generate` 返回 ID + base64 图片（答案只落 Redis），`Verify` 一次性消费：无论对错同一 ID 只有一次校验机会，猜错必须换新图 |
| 出站 HTTP 客户端（`internal/optional/outbound`） | 默认未接入；按 [出站模块说明](internal/optional/outbound/README.md) 恢复 OUTBOUND 接线，再配置具名下游。超时、响应体上限、连接池与重试均由同一共享入口控制。 |
| 熔断（`internal/optional/outbound/breaker`） | 属于可选出站模块，随其启用。按下游名隔离故障，阈值配 `outbound.breaker`；业务调用和失败分类见 [出站调用](docs/outbound.md)。 |
| GET 响应缓存（`internal/pkg/httpcache`） | 配 `cache.enabled` + `cache.store`（redis/memory），并在只读路由上显式挂 `deps.Cache(0)`。**默认关闭且不提供全局开启路径**——响应缓存把一次响应的字节原样发给后续请求，挂到带身份的路由上就是把 A 的数据发给 B。开启后仍有四道防线：只对显式挂了中间件的路由生效、请求侧身份判据绕过、响应侧准入、命中回放前复检 |
| 任务队列（`internal/optional/taskqueue`，asynq） | 默认接线已注释，先按目录 README 启用。配置 `tasks.enabled`、队列权重与 `tasks.redis_db`；库号必须出现在 `redis.dbs` 中。通过 bootstrap 适配器接入既有启停顺序。 |
| 周期任务调度（`internal/optional/taskqueue`，cron） | 启用接线后设置 `tasks.scheduler.enabled`，在 `internal/bootstrap/taskqueue.go` 中追加 `taskqueue.PeriodicTask`。调度限于当前进程，队列处理器仍需业务幂等。 |

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
| `auth` 模块 | 本地认证（注册/登录/刷新/登出）。`auth.mode: external` 使用外部身份来源；JWT 验签与账号状态校验独立保留 |
| `users` 表的业务字段 | `vip_status` / `vip_level` / `vip_expire_at` / `follow` 来自存量业务，**不是通用母版该有的**。删除清单：① `internal/models/user.go` 的这四个字段与 `VipStatus` 类型；② `internal/bootstrap/modules.go` 的 `entitlement.vip` 注册、`internal/bootstrap/authorizers.go` 的会员授权实现、`internal/shared/auth` 的 `VIPVerifier` 及 `internal/runtime/module/account/module.go` 的 `Assembly.VIP` 接线；③ `internal/repository/useraccount` 的 `ValidateVIP`；④ 基线迁移里的这四列。四处一起改——只删模型会让 authorizer 编译失败，只删 authorizer 会留下一套没人用的判定 |
| `entitlement.vip` authorizer | 会员资格判定示例；无 VIP 概念的项目可连同 `VIPVerifier` 一起删 |
| `role.admin` 的角色表实现 | 角色判定示例。删除清单：① `internal/models/user_role.go`；② `internal/repository/userrole/`；③ `internal/shared/auth` 的 `RoleVerifier`；④ `internal/bootstrap/authorizers.go` 的 `roleAuthorizer` 与 `adminRoleName`；⑤ `internal/runtime/module/account/module.go` 里的 `Assembly.Roles` 字段及角色源构造，并在 `internal/bootstrap/modules.go` 把 `role.admin` 改回 `sharedauth.DenyAll(...)`；⑥ `database/migrations/*_create_user_roles_table.sql`。**⑤ 不能省**——只删实现会让 `role.admin` 未注册，而未注册的标识由装配层兜底为恒拒绝并告警，那是可启动的，但你会失去「为什么全是 403」的线索。它**不做**三件事：不引入策略引擎（Casbin 等）、不做角色缓存、不做角色层级与通配 |
| `users` 表的基线迁移 | 与 `auth` 模块配套；替换身份模型时一并重做 |

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
| `internal/pkg/mail` / `internal/pkg/sms` | 邮件与短信客户端，由 `internal/platform/notification/mail` / `internal/platform/notification/sms` 的 Owner 构造；`internal/pkg/news` 借用发送能力（见上文「告警扇出的对外副作用」） |

配置里的 `application` / `ssl` / `cors` / `log` / `database` / `redis` / `env` 是进程与连接的基础配置，取值说明以 `config/env.yml.example` 的逐字段注释为准。

### 接入文件上传 / 对象存储

上传能力集中在 `internal/optional/upload/`，使用 go.mod 锁定的 `github.com/gtkit/gouploader`。
默认不注册上传或下载路由，目录外的构造、关闭和路由接线均以 UPLOAD 注释保留。
普通主程序和 `make build` 不引入上传包及 SDK；`go build ./...` 仍会编译该可选包。

本地磁盘使用 `upload.NewLocalUploader`，目录、大小上限和类型白名单三项必须显式设置。
普通上传调用 `uploader.UploadService().Upload`；对象存储、分片、STS 直传继续使用 SDK 原生 API。
后台错误默认接入项目 logger/v2，上传器的 `Close` 需要随进程生命周期执行。

上传路由复用 `middleware.MultipartBody`；上传文件的下载或回源路由使用
`upload.FileHeaders`。鉴权、文件归属检查及文件记录由业务模块负责。

初始化位置、启用步骤和删除方法见 [上传模块说明](internal/optional/upload/README.md)。

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
