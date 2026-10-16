# 架构约束

本项目采用「模块化 + 显式装配」结构：进程编排、模块装配、业务逻辑、数据访问四层职责分离，
层间依赖方向由 `scripts/check-architecture.sh` 强制，`make check` 第一步就会执行。

本文是分层与依赖边界的**单一来源**。传感器脚本里的每条规则都能在这里找到对应的理由；
反过来，这里的每条硬约束都应有可执行判据——只写在文档里的约束会被无声违反。

## 目录结构

```
cmd/                         → CLI 入口与子命令，只做参数解析与启动
internal/
  appmeta/                   → 应用级常量与路径元信息（由 make gen-appmeta 生成）
  bootstrap/                 → 进程级编排：拿资源、调模块装配、挂 router/worker、关停排序
  runtime/
    module/<module>/         → 模块装配层：config/db/cache → repo/client/adapter → application service
    resource/                → 运行期工具，不承载业务 DAO
  module/<module>/
    application/             → 业务逻辑层：service、port 接口、领域 error、command/result、options
    dto/                     → 模块独有请求/响应 DTO
    transport/http/          → HTTP 适配：handler 绑定/校验、router 注册
    worker/                  → 模块后台任务（可选）
  repository/<table-or-agg>/ → 数据访问层：封装 GORM/Redis；简单表一表一 repo，复杂聚合可用 aggregate repo
  models/                    → 持久化模型 + 跨层共享值类型/枚举
  middleware/                → Gin 中间件
  router/                    → 路由聚合、版本化模块注册、健康检查路由
  platform/
    config/                  → 配置加载与校验
    data/                    → 基础设施连接管理（DB/Redis）
    telemetry/               → OTel 导出装配
  pkg/                       → 项目内部共享包（仅本仓可 import）
  shared/                    → 跨模块共享的轻量语义
  scaffold/                  → 开发期模块生成器（//go:build scaffold 门控）
  projectinit/               → 项目实例化器（同上门控）
pkg/                         → 对外可复用包（可被仓库外项目 import）
```

### `internal/pkg/` 与根 `pkg/` 的区别

两者名字相近但约束完全不同，混淆的代价是发布出去才发现：

| | `internal/pkg/` | 根 `pkg/` |
|---|---|---|
| 可见性 | 仅本仓 | 任何外部项目 |
| 可否 import 本仓 `internal/` | 可以 | **禁止** |
| 可否直接写日志 | 可以 | **禁止**，事件经调用方注入的回调暴露 |

根 `pkg/` 的两条禁止有可执行判据（`check-architecture.sh`），且不是风格约束：

- **不得 import 本仓 `internal/`**——包一旦被外部项目引用，而它自身 import 了本仓 `internal/`，
  外部项目的编译会因 Go 的 internal 可见性规则直接失败。违反这条的包只是**看起来**可复用：
  在本仓内怎么跑都正常，只有真正被外部引用时才暴露，而那时已经发布出去了。
- **不得直接写日志**——包内直接写日志会把使用者绑定到某个具体日志库，而换一个库只是换个绑定对象。
  把事件交给调用方则彻底消除这个选择。

当前根 `pkg/` 下只有 `pkg/ratelimit`。

## 开发期工具的构建隔离

`internal/scaffold`、`internal/projectinit` 与它们的 `cmd` 入口（`cmd/module.go`、
`cmd/project.go`）由 `//go:build scaffold` 门控，**不进服务二进制**：`module` 会重写
`internal/bootstrap/providers.go`，`project init` 带 `--force-reinit-git` 且会永久丢弃
git 历史——两者对派生项目都不存在正确用法。

口径分工是硬约束，两边都不能省：

| 入口 | 带 tag？ | 原因 |
|------|---------|------|
| `go build` / `Dockerfile` / `make build` | **否** | 服务二进制里不该有生成器 |
| `vet` / `lint` / `test` / `coverage` | **是** | 这些包的测试锁定着模块生成与项目实例化两组行为；不带 tag 跑，它们根本不会被编译，保护会静默消失而 `make check` 依然全绿 |

两条可执行判据在 `scripts/check-architecture.sh`：默认构建的依赖闭包不得含
`golang.org/x/tools` 与 `github.com/gofrs/flock`（`go list` 自身失败也算失败，
不能当成"没有匹配"）；`cmd` 包另有一对测试分别断言两命令在默认构建下缺席、
在带 tag 构建下在场。

`cmd/modulepath.go` **不加 tag**——`resolveModulePath()` 被 `cmd/migrate.go` 消费，
`migrate` 是运行期命令。

在 IDE 里改这些包时给 gopls 配 `"buildFlags": ["-tags=scaffold"]`，否则文件被排除在
索引外（表现为满屏 undefined）。

## 装配边界

1. **`bootstrap/` 只做进程级编排**：拿基础设施资源、调用各模块装配入口、组装 router 与 worker、
   决定实例生命周期。不在这里直接拼模块内部的 repo/client/service。
2. **`runtime/module/<module>/` 做模块内部装配**：把 config/db/cache 翻译成 repo / client / adapter，
   再注入 application service。模块对外只暴露少量构造函数。
3. **`module/<module>/application/` 只保留业务**：service、port、领域 error、command/result、options。
   禁止 import Gin、GORM、具体 repository。

### 模块装配入口的两种形态

按模块产出的数量选择，不必强求统一：

```go
// 单一产物、依赖简单：直接返回 service
func NewService(db *gorm.DB) (*someapp.Service, error)

// 多个产物或依赖较多：Deps 入参 + Assembly 出参
// internal/runtime/module/auth/module.go、internal/runtime/module/ai/module.go 都是这一种
type Deps struct {
    DB     *gorm.DB
    Redis  *redis.Client
    Issuer authapp.AccessIssuer
    Config config.AuthConfig
    // ...
}

type Assembly struct {
    Service    *authapp.Service
    Revocation *tokenstore.RevocationList
}

func New(ctx context.Context, deps Deps) (Assembly, error)
```

用具名 struct 而不是长参数列表：装配层的入参会随模块演进增长，位置参数在增加到第四五个之后，
调用点就再也读不出哪个是哪个，而类型相同的两个参数写反不会有编译错误。

application service 同样收 `Deps` struct，可选行为用 functional option
（如 `userapp.WithAuditLogger(...)`）——必需依赖放 `Deps` 让缺失变成编译错误，
可选行为放 option 让默认值成为正常路径。

### bootstrap 的分阶段装配

模块自建的客户端由模块 `Assembly.Close` 交出清理入口。模块构造失败时先自行释放
已建立部分；`buildServices` 与 `newProviders` 在后续失败时逆序回滚。装配成功后，
清理责任移交 `App`，在 HTTP 与 worker 收尾之后、关闭共享基础设施之前执行。
仍有未退出 worker 时跳过模块清理并返回错误，避免关闭它正在使用的客户端。

`internal/bootstrap/providers.go` 的 `newProviders` 按七个阶段顺序编排，本函数只做顺序拼接、
错误透传与失败回滚（`rollbackStack` 逆序撤销已产生的进程级副作用），不内联任何单个模块的装配细节：

```
resolveInfra（基础设施与派生 HTTP 配置）
  → 遥测导出 → 数据层追踪埋点 → 指标仪表          （telemetry.New / InstrumentTracing / metrics.New）
  → 限流注册表 → 出站客户端 → 响应缓存 → 任务入队端   （buildRateLimits / httpclient / httpcache / tasks）
  → buildServices（各模块 application service）
  → buildMiddlewares（认证与授权中间件）
  → buildWorkers（后台任务 + WorkerManager）
  → buildEngine（路由引擎，含限流引用的 fail-closed 判定）
```

顺序是契约：遥测必须先于指标（仪表在构造时绑定全局 MeterProvider）；限流、出站、缓存、
任务入队必须先于 service（service 在构造时按名字取用它们，名字未登记属装配错误）；
worker 必须先于路由（readiness 要读 worker 状态）。

阶段间用三个聚合 struct 传递状态：`infra`（基础设施与派生配置）、`services`（各模块 application
service 的具名字段）、`middlewares`（鉴权等 Gin 中间件）。新增基础设施时才往 `infra` 加字段，
不提前占坑。

模块生成器经 `providers.go` 的八个稳定标记接线，新增模块只产生「追加字段/追加行」级改动，
不重构阶段函数。前四个是**注入型**（生成器在标记前插入接线行），后四个是**校验型**
（生成器只断言标记存在；这些位置的内容是业务判断，由使用者手写）：

| 标记 | 位置 | 类型 |
|---|---|---|
| `scaffold:module-imports` | import 区 | 注入 |
| `scaffold:module-service-fields` | `services` struct 字段 | 注入 |
| `scaffold:module-services` | `buildServices` 构造 | 注入 |
| `scaffold:modules` | `buildEngine` 的 `RegisterModules` 闭包 | 注入 |
| `scaffold:module-authorizers` | `buildAuthorizers` 具名授权注册 | 校验 |
| `scaffold:task-handlers` | `buildWorkers` 的队列消费者处理器参数表 | 校验 |
| `scaffold:periodic-tasks` | `buildWorkers` 的周期任务调度器参数表 | 校验 |
| `scaffold:module-workers` | `buildWorkers` 的 Worker 集合 | 校验 |

生成器另外注入 `internal/platform/config/modules.go`（`scaffold:module-config-fields` /
`scaffold:module-config-validators`）、`config/env.yml.example`（`scaffold:module-config-example`）
与 `cmd/migrate.go`（`scaffold:migrate-models`）。改动这些文件的结构时必须同步维护标记与
`internal/scaffold` 的插入逻辑及测试，避免标记与真实结构脱节导致自动接线失效。

### 选择性 DDD 战术

本项目走 clean architecture（ports & adapters），DDD 只按需采纳低成本战术，**不做**领域模型与
持久化模型分离、聚合根内存不变量、领域事件等重型改造（与现有 DB 层并发模型冲突且横扫全仓，
收益不明确）。

可采纳：值对象/强类型消除裸 `string`/`int` 传参；纯业务规则下沉 application service；
规则复杂的模块可加薄 `domain.go` 只放纯不变量/策略函数，不碰持久化。
CRUD 型模块不加 `domain.go`，避免无谓 ceremony。

## 依赖方向

业务主脊：

```
bootstrap → runtime/module → application
runtime/module → repository, application
transport/http → application, module/<m>/dto
application → 自身 port, models, module/<m>/dto, shared, 中立错误包
repository → models, 中立错误包
```

`application → models` 允许但应克制：可引用枚举、值类型、只读持久化结构；写操作和复杂业务流
优先定义 application 自己的 command/result，避免完整 model 长期扩散到 API result。

## 包可见范围

- **纯基础包：任何层可 import**。无业务、无 HTTP 协议、无 gorm/redis 依赖的工具包，
  以及 `appmeta`、`models`、`shared/*`。纯分页类型（查询入参与结果元信息）放 `internal/pkg/paging`。
- **基础设施型包：仅 repository、runtime/module、platform 可 import**。依赖 GORM/Redis/文件系统/
  外部 SDK 的工具包，例如 GORM 分页执行器 `github.com/gtkit/ormx/paginator`（含 `gtkit/ormx` 全部子包）。
- **协议/HTTP 适配包：仅 transport/http、router、middleware 可 import**。例如 response、
  gin context helper、中间件。

分页边界：

- `internal/pkg/paging`：纯分页类型（查询入参 `Query` 与结果元信息 `Paging`），不依赖 gorm，任何层可 import。
- `github.com/gtkit/ormx/paginator`：GORM 查询执行器（外部库），application / dto / models 禁止 import。

## 禁止规则

以下每条都有可执行判据：

- `application` 禁止 import `gorm.io/gorm`
- `application` 禁止 import `internal/repository/*`
- `application` 禁止 import `github.com/gin-gonic/gin`
- `transport/http` 禁止 import `internal/repository/*` 与 `gorm.io/gorm`
- `repository` 禁止 import `github.com/gin-gonic/gin`
- `repository` 禁止 import `internal/module/*`
- `module/<m>/worker` 只依赖 application，禁止直接 import repository / gorm
- `internal/pkg` 禁止反向 import `internal/module/*`
- 根 `pkg/` 禁止 import 本仓 `internal/`，禁止直接写日志
- `models` 禁止接收 `*gorm.DB` 的数据访问方法；业务规则方法优先放 application

传感器还覆盖四条与分层无关但同样是结构性的规则：

- **body 绑定只信 body**：禁止 `ShouldBind` / `ShouldBindJSON` / `ShouldBindWith`。gin 的
  `ShouldBind` 在 Content-Type 为 form 时会**同时**从 query string 与 body 取值填充结构体，
  字段没有 form tag 时回退用字段名匹配。于是攻击者只要把 Content-Type 改成 form，
  就能用 query 注入本该只来自 body 的字段。今天的 DTO 字段本就用户可控，注入拿不到额外收益——
  但任何一个 DTO 将来加了不该由用户控制的字段（`Role` / `TenantID` / `IsAdmin` 这类），
  它立刻变成真实漏洞，而加字段的人不会收到任何提示。
- **客户端 IP 只经唯一 accessor 取值**：IP 同时是审计事实与限流输入，两处各自裸调 `ClientIP()` 时，
  任何一处加了归一化就会让「日志里的 IP」与「被限流的 IP」指向不同主体。
- **出站 HTTP 不得用全局 client**：`http.DefaultClient` / `http.Get` / `http.Post` / `http.Head`
  用的是同一个进程级 client，它的 `Timeout` 为 0（没有任何超时），Transport 全局共享
  （一个依赖的连接池耗尽会波及所有其他依赖），且无处设置单依赖的超时或连接池。
- **数据访问层不得使用裸 SQL 入口**：`internal/repository` 与 `internal/runtime` 下禁止
  `.Raw(` / `.Exec(`。两条各自独立成立的理由：
  - **绕过软删除**。`gorm.DeletedAt` 的 `deleted_at IS NULL` 是在 clause 构建阶段加上的，
    而裸 SQL 整个跳过那个阶段。于是 `Raw("SELECT * FROM users")` 会把已软删的行一并返回，
    既不报错也不进日志——而 [`db-patterns.md`](db-patterns.md) 要求软删除一律用 `gorm.DeletedAt`。
  - **绕过参数化与白名单**。同一份规范要求 WHERE 的用户输入必须参数化、动态字段名走白名单；
    在一个 ORM 项目里，裸 SQL 是**唯一**还能做字符串拼接的地方，也就是那两条规范唯一的失守点。

  `internal/platform/data` 不在覆盖范围内：那里的裸 SQL 是启动期连接校验
  （读 vendor / 时区 / 字符集），不碰业务表也不涉及软删除。

## 访问控制由代码位置表达

路由用 gin 的原生方式注册，本项目**没有**中央路由描述符机制。

随之而来的代价必须说清楚：**新增路由时写在认证中间件覆盖不到的位置，它就是公开接口，
而编译、`go vet` 与全部测试都不会有任何异常。** 这一点无法用静态规则替代——
「这条路由该不该鉴权」是业务判断。换来的是任何会 gin 的人不用先学一套只在本仓成立的写法。

因此新增路由必须人工确认它挂到了正确的组上。

## 仓储粒度

- 简单表：一表一 repo。
- 复杂聚合：允许 aggregate repository 覆盖一个聚合根的多表/多介质。聚合 repo 的 README
  应包含「聚合边界」：覆盖表、外部介质、事务边界、缓存边界。

## 依赖注入

构造函数注入，禁止业务代码依赖全局变量。application 收 port 接口，不收 `*gorm.DB`。
`db → repo → adapter → service` 的拼装放在 `runtime/module/<module>`。

## 接口定义位置

- Repository / 外部依赖 port 定义在 application 层（使用方定义接口），放 `ports.go`。
- 遵循 Go 隐式接口实现，不提前抽象。
- 无基础设施依赖的极简模块可豁免 runtime 装配层，但要保持边界清晰。

## DTO 归属

- 模块独有 DTO 放 `module/<module>/dto`，包名用 `dto`。
- 真正跨模块共享的轻量类型放 `internal/shared/...`，不要塞进某个模块让别的模块横向 import。
- 写操作 / 复杂业务流：handler 做 DTO ↔ application command 映射。
- 简单读接口：可复用无 binding/form tag 的响应 DTO。
- repository 不得 import 模块 dto；查询条件用 repo 自己的 query/options，或 application port
  的中立输入类型，由 runtime adapter 桥接。

## 错误跨层规则

repo 不得把自己的 sentinel 变成 application 必须 import 的契约。按场景选：

1. **comma-ok**：单一找不到场景，port 返回 `(T, found bool, error)`。
2. **中立错误包**：真正跨模块复用的 NotFound / Conflict / Unauthorized 等可放共享错误包，
   但不要提前创建空包。
3. **runtime adapter 翻译**：模块特有的 repo sentinel 在 runtime adapter 翻译成 application 领域错误。

## runtime 层职责与红线

runtime 允许：config → options 翻译、创建外部 client、adapter 做类型和错误翻译。

runtime 禁止：

- **用环境名直接推导业务行为**。环境只选择配置，业务开关走显式配置项。
- **承载业务规则**。规则留在 application。

## 跨 repo 事务

跨多表/多 repo 的原子操作由 application 编排，application 经 `TxManager` port 注入事务能力，
不持有裸 `*gorm.DB`。

采用 context-tx 模式：事务 manager 把 tx 放进 context；repo 的 `mdbCtx(ctx)` 从 context 取 tx，
否则回退到默认 DB。

约定：

- repo 数据访问统一经 `mdbCtx(ctx)`。
- repo 内部可用事务处理单 repo / 聚合内部的多语句写。
- 跨多个 repo 的业务事务由 application 的 `TxManager.Do` 编排。

## 配置管理

- 用结构体承载配置，不用散落全局变量。
- 配置从外置 YAML 加载（`config/env/env.yml`）：进程启动时一次性读取并校验，
  找不到或缺关键项 fail-fast；不编译进二进制。
- 运行环境由配置文件内的 `env` 字段决定（dev/test/prod），不依赖文件名或命令行参数。
- 未知配置键即启动失败——写错的键静默失效，比启动失败难查得多。
- 敏感配置（密钥、密码、证书）不进版本控制，走外部注入 / 文件挂载。
  也可用 `APP_` 前缀环境变量覆盖（双下划线分层，如 `APP_DATABASE__PASSWORD`）。
