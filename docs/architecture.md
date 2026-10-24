# 架构约束

本项目采用「模块化 + 显式装配」结构：进程编排、模块装配、业务逻辑、数据访问四层职责分离，
层间依赖方向由 `scripts/check-architecture.sh` 强制，`make check` 第一步就会执行。

本文是分层与依赖边界的**单一来源**。传感器脚本里的每条规则都能在这里找到对应的理由；
反过来，这里的每条硬约束都应有可执行判据——只写在文档里的约束会被无声违反。

## 目录结构

```
cmd/                         → CLI 入口，选择 server/migration/doctor 等固定运行方式
internal/
  core/                      → 资源状态错误，不持有具体基础设施实现
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
    config/                  → 配置结构、加载、默认值与校验
    data/                    → DB/Redis 借用视图、健康和追踪工具
    mysql/、redis/           → 固定 SDK 连接的构造和关闭
    responsecache/           → 响应缓存的构造和关闭
    logging/                 → gtkit/logger 的 main/access 所有者
    notification/            → 通知契约、接纳/排空及 robot/mail/sms/dispatch
    telemetry/               → OTel 导出装配
  pkg/                       → 项目内部共享包（仅本仓可 import）
  shared/                    → 跨模块共享的轻量语义
  projectinit/               → 项目实例化器（//go:build scaffold 门控）
pkg/                         → 对外可复用包（可被仓库外项目 import）
```

### 固定技术栈与资源所有权

配置在 `internal/platform/config` 直接加载与校验；时区在任何后台资源启动之前应用。
`internal/bootstrap/resources.go` 以固定顺序构造通知通道、通知组合、日志、MySQL、Redis 和响应缓存。
没有通用插件契约、依赖图、注册回调或运行时发现。命令入口只选择所需运行方式，
业务 application 继续通过自己的窄端口访问仓储和外部服务。

各具体资源用 `Open(ctx, Options)` 完成构造，成功返回拥有关闭权的 Owner；消费者只取得借用能力。
`platform/data.Resources` 保存 DB/Redis 句柄与观测视图，不关闭连接。
main 直接使用 `gtkit/logger/v2` 包级 API；access 原生实例经 bootstrap → router → middleware 注入。
`platform/logging` 只管理构造和生命周期，不提供每条日志的转发、字段转换或接纳层。
正常关停确认日志源结束后，Runtime 先关闭缓存、Redis、MySQL，再关闭日志，最后关闭通知组合、短信、邮件、机器人。
部分构造失败会回收已取得的资源，清理错误与根因合并返回。

server 启动总预算 30 秒，单次资源构造最多 10 秒，父 deadline 只能收紧。
migration 的总启动预算为 15 秒，仅构造配置、通知/日志和数据库，使用迁移凭据及迁移专用 opener；
DDL 的命令预算与连接启动预算分开。doctor/JWT 保持最小装配。

HTTP 中间件由 `internal/router/middleware.go` 在最终 metrics 就绪后直接构造，
保持 Recovery、RequestID、可选 tracing、访问日志、metrics、内层 Recovery、安全头、
错误处理、可选 CORS 和请求体上限的顺序。每个 engine 独立构造，无组件生命周期状态。
缓存继续由具体路由显式挂载，带身份请求仍绕过缓存；Redis 必需性按已启用能力推导。

模块/扩展失败和 Runtime 回滚共享独立的 30 秒清理预算；正常关闭保留父取消、更短 deadline 和第二信号。
重复关闭共享首次结果，等待者可独立取消；关闭错误或 panic 不跳过后续所有者。
HTTP/worker 仍分阶段退出，存活 worker 导致共享资源关闭跳过并返回 ErrWorkerNotExited。
HTTP 强制关闭、Shutdown 失败或其他日志源未确认结束时也保留仍被使用的资源。
此时同一日志 owner 的 Drain 仅 `Zap().Sync()` 刷文件缓冲，不关闭 SDK 或通知渠道，
不保证通知队列排空；日志源后来结束仍可正常 Close。

日志和通知的实际排空需要 SDK 与 sender 响应取消。SDK 的 void Sync、部分无 context 的 Close
无法硬中断，不通过遗弃后台关闭 goroutine 隐藏尚未完成的 I/O。
通知渠道的替换与关闭约束见 [通知渠道与替换方式](observability.md#通知渠道与替换方式)。

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

`internal/projectinit` 与入口 `cmd/project.go` 由 `//go:build scaffold` 门控，
**不进服务二进制**。`project init` 用于新项目初始化，显式使用 `--force-reinit-git`
会重建 Git 仓库，运行服务无需携带该能力。

口径分工是硬约束，两边都不能省：

| 入口 | 带 tag？ | 原因 |
|------|---------|------|
| `go build` / `Dockerfile` / `make build` | **否** | 服务二进制不包含项目初始化工具 |
| `vet` / `lint` / `test` / `coverage` | **是** | 覆盖项目初始化相关代码与测试 |

两条可执行判据在 `scripts/check-architecture.sh`：默认构建的依赖闭包不得含
`golang.org/x/tools`（`go list` 自身失败也算失败，不能当成"没有匹配"）；
`cmd` 包另有一对测试分别断言 `project` 命令在默认构建下缺席、在带 tag 构建下在场。

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

`internal/bootstrap/providers.go` 只负责基础设施视图与进程级装配顺序；
`internal/bootstrap/modules.go` 集中维护模块服务、认证授权、HTTP 路由及后台任务的接线，
两者同属 `bootstrap` 包，不增加运行时注册层或修改模块内路由职责。

Runtime 先读取配置并校验本地 JWT 密钥、TLS 证书，再创建日志、数据库、Redis 和缓存。
TLS 证书保存在内存中供 HTTP 预绑定复用，不二次读取文件；预绑定仍重新检查当前有效期，
并在日志已就绪后报告临期告警。端口预绑定仍先于 worker 启动。

当前模块仅借用 Runtime 的基础设施，`buildServices` 不创建或拥有独立客户端，
不维护空的模块清理栈。真正创建资源的 provider 在构造成功处登记到 `providerCleanup`：
失败时逆序回滚，成功时把清理责任交给 `App`，在 HTTP 与 worker 收尾后、关闭共享基础设施前执行。
仍有未退出 worker 时跳过扩展与共享资源清理并返回错误，避免关闭它正在使用的客户端。

`internal/bootstrap/providers.go` 的 `newProvidersWithBudget` 按七个阶段顺序编排，本函数只做顺序拼接、
错误透传与失败回滚（`providerCleanup` 逆序撤销已登记的真实副作用），不内联任何单个模块的装配细节：

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

新增模块时，在 `services` 增加实际需要的产物字段，`buildServices` 调用模块装配入口，
`buildEngine` 的 `RegisterModules` 注册路由。具名授权在 `buildAuthorizers` 注册，
任务与调度器在 `buildWorkers` 接入。新增持久化模型同时登记到 `cmd/migrate.go` 的
`WithDDLModels`，并添加经过审阅的迁移 SQL；新增配置字段同步更新配置示例与校验。

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
- 同归属管理器的嵌套 `Do` 使用同一物理事务的savepoint；内层成功不独立提交，
  内层失败回滚到savepoint，外层可显式处理后继续。未知/跨管理器、并行或已关闭的scope拒绝。
  savepoint或回滚失败使外层不可提交；清理错误与原错误并列返回，panic在清理后继续传播。

## 配置管理

- 用结构体承载配置，不用散落全局变量。
- 配置从外置 YAML 加载（`config/env/env.yml`）：进程启动时一次性读取并校验，
  找不到或缺关键项 fail-fast；不编译进二进制。
- 运行环境由配置文件内的 `env` 字段决定（dev/test/prod），不依赖文件名或命令行参数。
- 未知配置键即启动失败——写错的键静默失效，比启动失败难查得多。
- 敏感配置（密钥、密码、证书）不进版本控制，走外部注入 / 文件挂载。
  也可用 `APP_` 前缀环境变量覆盖（双下划线分层，如 `APP_DATABASE__PASSWORD`）。
