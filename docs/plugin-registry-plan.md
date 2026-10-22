# 编译期插件架构重构方案与实施计划

> 历史记录：以下路径、命令和验收结果按当时版本保留，部分文件现已迁移或移除。当前基础组件采用显式 Open/Close 装配，见 [当前架构](architecture.md) 和 [简化方案](architecture-simplification-plan.md)。

修订日期：2026-10-21（Asia/Taipei）。

> 本版状态：阶段 1 已由用户提交，阶段 2–3 定向验收完成，阶段 4 双环境完整质量门禁、真实本地功能验收及隔离目录代码回退演练已通过。全部阶段 0–4 已完成并归档回读，证据见第 15–16 节。回滚与插件停止总预算各 30 秒；JSON 不插件化、不迁移版本。未执行 commit/push/tag/release、依赖升级或上游修改。
>
> 本次开发验证及插件重构功能验收通过，SDK 非合作式关闭/void Sync、生产容量和部署退出宽限期仍受限或未验证，不能据此宣称生产发布准入或永不 OOM。历史第 12–14 节保留当时事实，不替代第 16 节的本轮完整证据。

## 1. 需求来源与本次修订

需求来自用户在当前 `pluggable` 分支提出的架构约束，以及随后认可的评审方案：使用接口与插件注册器实现编译期可插拔，由业务入口显式选择和注册实现；统一执行 `Name`、`Init`、`Start`、`Stop`；按依赖排序，禁止循环依赖；只抽象有替换需求的组件。

本期完整覆盖六类扩展点：配置、日志、数据库（默认 MySQL）、Redis、缓存、HTTP 中间件。不使用 Go 标准库 `plugin` 包、动态 `.so`、运行期发现或自动扫描，不预埋 MQ 接口。

本版保留原计划阶段 0–4 的编号和主顺序：计划与基线 → Core → 内置插件 → Bootstrap/迁移接线 → 全量验收。阶段 2 扩展为六类组件，阶段 3 相应覆盖所有入口和消费端；旧版将配置、日志、中间件排除在非目标的条款不再适用。

修订还纠正两类口径：当前已有插件代码不等于完整目标已经实现；历史测试记录不等于本版代码、完整失败路径或真实依赖验收已经通过。

## 2. 当前事实、问题与未确认项

### 2.1 重构前已核对的工作树事实（历史基线，当前结果见第 14 节）

- 当前分支为 `pluggable`。工作树已有代码修改、删除和未跟踪文件，包含 `internal/core/`、历史路径「internal/plugin/」（已迁移或移除）、历史路径「internal/bootstrap/plugin_registration.go」（已迁移或移除） 等；它们属于本轮开始前的工作内容，不能覆盖或作为可随意丢弃的临时文件。
- 模块为 `github.com/gtkit/gin-api`，`go.mod` 声明 Go 1.27.0。文档阶段记录的工具链为 go1.27.2、本仓 `go.work`；阶段 1 已重新核对工具链与定向测试，见第 13 节。
- 当前服务装配链为 `cmd/server.go → bootstrap.NewApp → NewRuntime → 注册器 Init/Start → ResourcesFromClients → newProviders → App.Run`。旧版将 `data.NewResources` 写成当前主路径，已经不准确。
- 历史路径「internal/core/plugin.go」（已迁移或移除） 已有四方法接口；注册器已有依赖校验、拓扑排序、状态和停止错误聚合代码。MySQL、Redis、响应缓存已有默认插件实现，但三者仍在同一个 `internal/plugin` 包。
- `NewRuntime` 在注册插件前固定调用 `config.Load` 和 `initProcessRuntime`；后者固定初始化主日志与访问日志。HTTP 全局链仍由 `internal/router/init.go` 的 `useGlobalMiddleware` 构建。
- `Resources` 同时保留旧的自有连接路径和新的插件借用视图。新路径的 `Close` 不关闭连接；旧路径仍含 `ormx` 具体类型和资源创建责任，属于需要收敛的迁移兼容层。
- `PluginRegistrar` 当前接收已经加载的完整配置，并返回数据库、Redis、缓存三项固定能力。这个签名不能直接表达“配置本身也是第一个插件”。
- `CacheLifecyclePlugin` 返回具体 `*httpcache.Stager`；Redis 生命周期接口同时包含底层句柄、健康检查、池统计和具体 availability tracker。消费端替换边界仍需收窄。
- 已核对 33 个生产 Go 文件直接导入 `github.com/gtkit/logger/v2`；此外访问日志和 SQL logger 还通过具体实例、zap 字段或 `ormx/zlogger` 耦合。日志可替换不能只包装初始化函数。
- 配置加载器及 JWT 命令的配置读取都使用 koanf；JWT 命令复用同包的路径、安全校验和环境变量映射。拆分加载器时必须一起核对，不能只搬 `load.go`。
- `App` 已有 Producer → LB 传播等待 → HTTP 排空 → Consumer → Bootstrap worker 的分阶段关停，并有存活 worker 检查、强制信号和独立日志排空。插件逆序停止不得替代这些语义。

### 2.2 阶段 1 前确认的静态缺陷与修正契约

| 编号 | 源码依据与触发条件 | 本版要求 |
|---|---|---|
| F-01 | `core.startPlugins` 在全部插件 Init 成功后，只回收已启动插件及当前失败插件；A/B/C 初始化后，B 启动失败会遗漏 C；failed 状态的后续 Stop 不会补做清理 | 清理集合覆盖全部已初始化插件，包含尚未 Start 的插件；Init 失败包含当前已进入回调的插件 |
| F-02 | `NewRuntime` 绕开旧 `NewResources` 的总计 30 秒、单资源 10 秒预算，直接向注册器传入口 ctx | 恢复总预算与单插件回调预算，父 deadline 只收紧、不放宽 |
| F-03 | 生命周期等待只读 done 通道；`Runtime.Close` 等路径再次 WithoutCancel；失败回滚沿用可能已取消的启动 ctx | 等待可取消；回滚独立有界；停止保留已建立的停止预算和强制信号 |
| F-04 | 配置、日志、中间件固定装配；缓存能力泄露 Stager；根 plugin 包同时导入全部默认实现依赖 | 六类能力接线，消费端不依赖默认实现；默认实现拆入独立子包 |

以上为阶段 1 前的源码基线；F-01/F-02/F-03 的 Core 修复与反证结果见第 13 节。原两插件且末尾失败的测试不能证明 F-01 已覆盖；本阶段已新增三插件中途失败回归。F-04 仍留阶段 2–3。

### 2.3 依赖与执行环境基线

| 依赖 | 当前锁定版本 | 本期策略 |
|---|---|---|
| Gin / GORM / go-redis | v1.12.0 / v1.31.2 / v9.23.0 | 保留既有 transport、ORM 和客户端契约，不做驱动无关化 |
| `gtkit/ormx` / `gtkit/redisx` | v1.6.0 / v1.4.0 | 已核对本地源码与模块缓存中的构造、探活、关闭 API，优先适配 |
| `gtkit/gincache` / ristretto adapter | v1.3.1 / v1.1.0 | 复用 Store 与既有响应准入规则 |
| `koanf/v2` | v2.3.8 | 保留为默认配置加载实现，移出配置 schema/校验依赖面 |
| `gtkit/logger/v2` | `go.mod` 为 v2.3.0；workspace 使用本地 logger/v2 源码，实际解析路径见 16.5 | 分别验证 workspace 与 `GOWORK=off`，不自动升级依赖 |

本地 gtkit 内容源为 `/Users/xiaozhaofu/go/src/my-gtkit-package`。本轮仅记录边界，不改该目录。现有 `Sync()` 无 error 返回，不能从其返回值证明日志刷盘、关闭全部成功；这项限制必须进入日志验收记录。

阶段 1 已核实本机 OpenSpec 入口、匹配 change、apply-ready 和严格验证；定向测试结果见第 13 节。真实 MySQL/Redis 的阶段 4 结果见 16.5；部署退出宽限期和生产容量仍未确认。旧计划的 CLI 故障不代表当前不可用；不编造 QPS、性能收益、真实回滚耗时或完整项目通过率。

## 3. 目标、非目标与兼容约束

### 3.1 目标

1. Core 仅持有通用插件契约、依赖图和生命周期调度，不导入 Gin、GORM、go-redis、koanf、gtkit 或业务模块。
2. 六类扩展点均有最小能力契约和一个现有技术栈的默认实现；测试替身能够替换实现，无需修改 Core。
3. 业务 `main`/`cmd` 组合根显式构造、注册、选择默认实现；Bootstrap 消费能力和编排进程，不隐式选择默认 SDK，不使用无类型服务定位器。
4. 生命周期同时覆盖正常流程、部分初始化、启动中途失败、取消、超时、重复停止和关闭失败；资源有且只有一个 owner。
5. HTTP、worker、迁移、诊断和 JWT 命令维持各自既有装配需求，按需选插件；迁移和轻量命令不得因此依赖 Redis、缓存或完整 HTTP 链。
6. 只有组件选择点需要新增接口；普通工具、现有业务 port、固定进程骨架不泛化为插件。

### 3.2 非目标

- 不引入动态加载、热插拔、热更新、反射发现、完整 DI 容器或新的 MQ 扩展点。
- 不重构 application/repository 业务分层，不更换 GORM、SQL 方言、Redis 客户端模型，不承诺任意数据库可直接替换。
- 不改表结构、迁移内容、业务规则、HTTP 路由、DTO、鉴权策略、任务处理器、队列投递语义或部署架构。
- 不调整根目录层级、移动既有业务模块、搬迁 `pkg/` 工具；仅在插件边界内拆分默认实现及迁移与其直接相关的装配代码。
- 不升级 Go 或依赖，不首次引入任何 v2 模块/分支/tag，不安装工具，不提交、推送或发布。
- 不以“所有插件都有四方法”为由要求每个函数、每条中间件都成为插件；配置和中间件组合允许没有实际启动/停止工作。

### 3.3 必须保持的兼容契约

- 配置：`config/env/env.yml`、`APP_ROOT` 路径规则、`APP_` 覆盖、双下划线层级、保留环境变量、未知键拒绝、生产文件安全校验、默认值及 JWT 命令专用读取语义不变。
- 日志：主日志/访问日志分工、路径与保留策略、结构化字段、脱敏、request/trace 关联、channel 和已有进程告警行为不变；不能把所有普通错误日志改成告警。
- MySQL：运行/迁移凭据选择、连接池、版本/vendor 校验、时区、字符集、collation、严格模式、事务与错误翻译不变；迁移继续不套用服务进程的驱动读写 deadline。
- Redis：逻辑库、原始 key、前缀、已有 TTL、池参数和重试策略不变。required 仍由已启用能力推导；optional 启动不可达继续保持本进程降级至重启，不擅自改为热重连或切换客户端。
- 缓存：保持现有内存/Redis 后端、容量限制、响应码和请求/响应准入、带身份请求绕过及显式路由挂载语义；通用进程内缓存算法仍为普通工具。
- HTTP：保留现有中间件相对顺序、可信代理、请求体上限、404/405、CORS、panic 响应和观测覆盖；不把缓存自动挂到所有路由。
- 关停：保留单一信号监督者、第二信号非零退出、worker 存活时不提前关闭插件共享资源、独立日志排空及迁移最小装配。

## 4. 方案比较与选择

| 方案 | 替换能力与正确性风险 | 开发/运维成本 | 兼容与回滚 | 选择 |
|---|---|---|---|---|
| A：小注册器 + 六类能力 + 显式装配 | 修复既有注册器，组件通过窄接口替换；风险集中在初始化、日志消费端和入口接线 | 改动集中于适配/装配层；无新增基础设施 | 保留既有业务契约，可逐阶段回退本次改动 | 采用，用户已认可方向 |
| B：引入完整 DI/应用容器 | 可提供生命周期，但增加与现有显式装配、WorkerManager 重叠的机制 | 新依赖与两套装配概念 | 回退需同时撤销容器接线 | 不采用 |
| C：全链路驱动无关化 | 连仓储、事务、分页、迁移都改为通用存储 port，显著扩大行为面 | 跨业务模块，成本未估算 | 兼容证明和回滚复杂 | 不属于本期 |

不依据未测量的性能选择方案。本期保持池规模、缓存容量和请求热路径语义；注册/拓扑只发生在启动期，不在每次请求中查表。容量、压测、生产灰度数字未确认，本计划不把它们写成已满足条件。

## 5. 目标布局、依赖方向与六类组件

### 5.1 目标布局（阶段 2 已拆分默认实现，完整组合根接线待阶段 3）

```text
internal/core/                         唯一 Plugin 契约、PluginRegistry、通用生命周期状态
internal/plugin/                       必要能力接口；不导入下方默认实现
internal/plugin/builtin/config/        默认 koanf/YAML 加载插件及轻量加载入口
internal/plugin/builtin/logging/       默认 gtkit 日志插件与字段/实例适配
internal/plugin/builtin/mysql/         默认 ormx MySQL 插件及服务/迁移 opener
internal/plugin/builtin/redis/         默认 redisx 插件及诊断 probe
internal/plugin/builtin/cache/         默认 gincache/内存响应缓存插件
internal/plugin/builtin/httpmiddleware/ 默认 Gin 中间件组合插件
internal/bootstrap/                    保留现有进程、模块、HTTP/worker 分阶段编排
internal/platform/config/              保留配置 schema、默认值、业务校验；移出默认解析器
internal/platform/data/                收敛为借用资源视图、可用性和追踪适配
internal/pkg/log/                      项目日志门面；不构造/关闭默认 SDK，不导入 news 通知器
pkg/、其他 internal/pkg/               普通工具，保持现有位置
```

这是本版明确授权的拆分范围；阶段 2 已将旧 MySQL、Redis、缓存扁平实现迁入 历史路径「internal/plugin/builtin/」（已迁移或移除） 相应子包并更新必要引用，配置/日志/中间件默认实现亦已独立，无第二套默认实现。完整 main/cmd 选择与生命周期接线仍属阶段 3。

依赖方向：组合根 → Core/能力接口/选中的实现/Bootstrap；Bootstrap → Core/能力接口/现有模块装配；默认实现 → Core/能力接口/所需 SDK 或工具。Core 不依赖 plugin 包，能力接口不依赖 builtin；业务 application 继续通过自己的 port 工作。

能力接口不嵌入具体实现的管理对象。Core 生命周期接口只定义一次；注册器拿 `core.Plugin`，消费者拿配置、日志、数据库、Redis、缓存或中间件能力，不获得 Stop/Close 权限。接口允许使用本期保留的 Gin/GORM/go-redis 类型，但不得暴露 `*ormx.Client`、`*redisx.Client`、`*koanf.Koanf`、`*loggerv2.Logger` 或 `*httpcache.Stager`。

默认实现隔离不等于整个应用依赖图必然删除所有 gtkit 模块，例如现有仓储仍使用 `ormx/paginator`。验收应检查具体实现依赖是否留在契约/Core/门面中，不误报其他明确保留的用途。

### 5.2 六类插件的能力与所有权

下列能力与方法形状是本版拟实施契约，除既有 Plugin 四方法外均尚未实现；草案标记不代表实施时可自行换方案。对应代码阶段应据此固定契约文件并同步全部调用方，如需改变职责或签名，先在本文件说明影响，不冒充已存在 API。

| 类别 | 消费端能力 | Init / Start / Stop 职责 | 依赖与失败语义 |
|---|---|---|---|
| 配置 | `Value() (*config.Config, error)`；返回已完成校验的快照，未初始化返回明确错误 | Init 读取/覆盖/校验；Start、Stop 无实际工作；本期没有 watch goroutine | 第一个初始化，不依赖正式日志；失败仅用 stderr 诊断，无默认配置伪成功 |
| 日志 | `Log(context.Context, LogEntry)`；项目自有事件/级别/字段承载现有主日志、访问日志、channel 和告警语义 | Init 创建并安装本次实例；Start 短返回；Stop 排空并释放本次拥有的实例/队列/文件 | 依赖配置；构造部分失败先回收自身；正常停止晚于仍需日志的插件 |
| 数据库 | `DB() *gorm.DB`、必要的 `SQLDB()` 与 `PingContext`，按消费者需要拆分 | 默认 MySQL Init 读已加载配置、按用途开连接并校验；Start 校验就绪；Stop 关闭唯一 owner | 依赖配置/日志；MySQL 为现有服务必需；不注册第二个“数据库资源插件”再开同一连接 |
| Redis | 逻辑库句柄、是否配置、探活和池观测；availability 由资源视图负责，不强制实现具体 tracker | Init 创建客户端或确定 optional 降级；Start 短返回；Stop 只关闭自有客户端 | 依赖配置/日志；保留 required 推导和降级规则，普通消费者没有 Close |
| 缓存 | `Enabled()`、`Middleware(time.Duration) gin.HandlerFunc`；不返回 Stager | Init 创建默认响应缓存资源；Start 短返回；Stop 只释放自己的 store/后台工作 | 依赖配置/日志；Redis 后端再依赖 Redis，内存后端无 Redis 依赖；借用 Redis 不关闭它 |
| HTTP 中间件 | `Build(MiddlewareDeps) ([]gin.HandlerFunc, error)`，返回明确有序链 | Init 校验组合配置；Start 短返回；Build 在观测仪表就绪后构建链；Stop 不关闭借用资源 | 依赖配置/日志；Metrics 等装配产物通过 Build 的具名参数传入，不在 Init 中提前捕获未建好的仪表 |

配置插件必须在自己的 Init 中加载，不能先在 Bootstrap 固定加载一次再给插件一个空 Init。其他插件构造函数只接配置 provider 等类型明确的依赖及用途值，在自己的 Init 中读取配置；不得在注册阶段偷读尚未加载的 Value。

HTTP 中间件组合是一个选配组件，不给每条 handler 加四方法。Build 在 HTTP Serve 前调用，返回链在开始服务前冻结，重复安装或构建失败属于装配错误。全局链序保持：外层 Recovery → RequestID → 可选 tracing → 访问日志 → Metrics → 内层 Recovery → SecurityHeaders → ErrorHandler → 可选 CORS → MaxBytesLimit。路由认证、授权、路由限流和显式响应缓存继续保留各自挂载位置。

### 5.3 日志边界与循环依赖处理

项目门面使用自有日志事件和字段，不以 `zap.Field` 作为公共日志契约；默认 logging 插件负责转换。现有 printf、结构化字段、context、channel、访问日志和进程告警调用必须逐项映射并测试，不能直接删掉无法映射的字段或告警。

门面仅在启动期安装所选 Logger，未安装时只用不落盘、不发通知的最小 stderr 兜底；不支持运行期热替换。application 不增加日志库 import，已有业务日志 port 保留，由 runtime adapter 转接。

当前 `internal/pkg/log` 构造 notifier 并导入 `internal/pkg/news`；news 也需要记录日志。应把实例/notifier 构造移到默认 logging 插件，使门面不再 import news，避免形成 log → news → log 环。告警发送失败不能递归触发同一个告警通道。

SQL 日志应通过注入的 GORM logger adapter 接到选定日志能力，不再从 `logger.Default().Zap()` 提取全局具体实例；继续参数化脱敏、慢查询/错误分类和请求关联。改动是日志适配，不改变数据库业务语义。

`logger/v2.Sync()` 的错误不可见性作为已知能力限制记录：本期不凭空返回“全部刷盘成功”。如阶段 2 证明必须补上游关闭错误/取消能力，应先提出兼容的 API 与文件清单并获得对应授权，再改本地内容源；现有 Sync 调用保持兼容，不自动安装、升级或发布。该条件项未解决时，受影响验收项标为受限/未验证，不允许勾选通过。

## 6. 注册器、预算与异常路径契约

### 6.1 不扩张四方法接口

```go
type Plugin interface {
    Name() string
    Init(context.Context) error
    Start(context.Context) error
    Stop(context.Context) error
}
```

依赖继续由 `Register(plugin, dependencyNames...)` 声明。名称在注册时校验并固定；nil/typed-nil、空名、重名、空/重复依赖拒绝注册。冻结后拒绝增加插件；缺失依赖、自依赖和循环必须在任何生命周期回调前失败。固定图的执行顺序确定，独立插件的排序以注册顺序为依据。

状态路径为 registering → initializing → initialized → starting → running → stopping → stopped；Init/Start 失败进入 failed，并记录实际清理进度与错误。未初始化不直接 Start 回调；继续支持 Registry.Start 自动先 Init。已完成的 Init/Start 再次调用返回生命周期错误，不重新创建资源；Stop 为幂等终态操作。

回调不持注册器锁，不允许插件重入同一注册器。并发等待者共享同一操作结果，等待者取消只结束自己的等待，不启动并发清理、不改写实际执行者状态。Stop 遇到尚未完成的 Init/Start 必须等待或按自己的 ctx 返回；不能一边初始化一边关闭依赖。

### 6.2 清理集合与错误

- 在进入每个 Init 回调前登记可能取得资源的插件；它返回错误也属于清理集合，未进入 Init 的插件不清理。
- Init 失败/被取消时，逆序清理已进入 Init 的集合；Start 失败/被取消时，逆序清理全部初始化集合，包含尚未 Start 的插件。
- 正常 Stop 对初始化集合逆序执行。单个 Stop 报错不阻断后续 Stop；清理根因与全部可获得的关闭错误用 `errors.Join` 保留，取消分支也不能丢失关闭错误。
- 不因转成 failed/stopped 就伪造尚未完成的清理；重复等待返回记录的停止结果，不重复关闭。超时仍存活的任务/资源必须在结果中列明，不宣称已经释放。
- 默认 opener 若交回非 nil 资源及 error，适配层必须先明确回收责任并清理已取得资源，不能仅返回错误后丢掉句柄。

### 6.3 Context 与预算

| 阶段 | 目标契约 | 值的来源/状态 |
|---|---|---|
| 初始化和启动 | 全部插件共用启动总预算；每次 Init/Start 回调进一步收紧，取父剩余时间与单回调上限的较小值 | 总计 30 秒、单回调 10 秒；沿用原资源预算并扩展到注册器，尚待接线验证 |
| 失败回滚 | 保留启动 context 的值，剥离已经结束的启动取消后建立独立 timeout；保留强制退出信号 | 用户已确认回滚总预算 30 秒；阶段 1 实现 Core，阶段 3 接进程信号，不新增 YAML 配置 |
| 插件停止 | Registry 使用 30 秒总上限，保留调用方更短 deadline/cancel；App/Runtime 的预算和强制信号接线留阶段 3 | 用户已确认插件停止总预算 30 秒；与 HTTP/worker 的既有预算分别记录 |
| 日志排空 | 日志原有 30 秒上限与调用方剩余预算取较小值；强制信号可结束等待 | 保留 `loggerDrainBudget` 的上限语义；被截断必须记录未完整排空 |
| 迁移启动 | 继续使用专用启动预算，不把它当成 DDL 执行截止时间 | 保留 `defaultMigrationStartupTimeout` 的现有 15 秒语义 |

本版新增的回滚/停止预算已获用户确认，实现与验收状态见阶段 1 记录；不是实测容量。配置键、连接池和超时重试的既有值不随本次重构调优；若确需新增运行配置，先修订本计划并同步真实配置、example 与说明后再实施。

Init/Start 参数是操作预算，不是后台 goroutine 的存活期。需要后台工作的插件自行持有运行取消句柄，并在 Stop 中取消、等待；不能把 Start 返回后就会取消的 context 交给长期工作。

context 只能约束合作实现。对不接受 context 的数据库/日志关闭 API，不用遗留 goroutine 包装成“已硬中断”；应记录底层关闭边界和未退出项。部署总退出预算、HTTP/worker/插件/日志预算之和与部署宽限期的关系，在阶段 3 根据真实部署事实核对，不能未经核对承诺总退出时长。

### 6.4 装配、关停与轻量命令

- 组合根创建配置插件 → 其他插件通过 provider 读取配置；注册依赖图并返回具名能力集合；Registry Init/Start 后，Bootstrap 构建追踪、仪表、模块与路由。中间件 Build 必须晚于仪表创建。
- 用户在阶段 3 明确批准默认组合静态声明 cache→redis：配置在 Init 才加载，Core 在此前冻结图，组合根不能提前读配置决定依赖。该边只表达默认实现可能借用 Redis 的生命周期先后；memory/disabled 不访问 Redis，Redis required/optional 及降级规则不变。其他替代组合自行按真实依赖注册；不建立 MySQL→Redis。日志依赖配置，需要日志的插件依赖日志，逆序关闭时日志最后。
- 正常关停保留现有 producer/LB/HTTP/consumer/bootstrap worker 阶段，再关闭模块与扩展，最后通过注册器停止缓存、Redis/MySQL、日志、配置；不重复直接调用这些资源的 Close。
- 存活 worker 时仍跳过插件共享资源关闭，返回现有可判别错误；独立日志排空由日志插件拥有的幂等排空能力承接，和正常 Stop 共用所有权状态，不另建 logger 或重复关闭。普通业务消费者不取得该管理能力。
- 迁移只选择配置、日志、数据库插件，使用迁移专用凭据和 opener，不保留具体 `*ormx.Client` 作为 Bootstrap 对外契约，不初始化无关 Redis/缓存/中间件。
- doctor 默认只加载和校验配置；deep 才创建对应探测资源并明确释放。JWT 密钥命令保持专用部分配置读取，不启动主应用。开发期 module 命令与集成测试的配置加载调用一起迁移，不意外改变 CLI 行为。

## 7. 文件范围与已有改动保护

### 7.1 当前阶段 3–4 允许修改的文件

按第 7.2 节及第 15 节执行入口、Bootstrap/迁移/优雅退出、相关测试/文档和现有传感器，之后执行阶段 4 原有全量验收。Core 保持阶段 1 的已提交实现，默认组件保留阶段 2 的能力/兼容契约；有证据的本轮缺陷才在已批准组件范围修复。阶段 2 工作树仍未提交，已另存阶段 3 前快照，禁止以 HEAD 覆盖这些改动。配置键/值、依赖、业务/存储结构和目录层级不变。

### 7.2 未来 Core、默认实现和装配范围

| 文件/位置 | 拟改动与边界 |
|---|---|
| 历史路径「internal/core/plugin.go」（已迁移或移除）、`plugin_registry.go`、对应测试 | 保留四方法，修复清理集合、状态等待、预算和错误传播 |
| 历史路径「internal/plugin/interfaces.go」（已迁移或移除）、拟新增六类契约文件及对应测试 | 固定最小能力与具名参数，去掉 Stager 和具体 tracker 的实现强制 |
| `internal/plugin/{mysql,redis,cache}.go`、`plugin_test.go` | 分别迁到第 5.1 节子包；原测试责任随实现迁移，不以删测试完成重构 |
| 第 5.1 节六个 `builtin` 子包及各自测试 | 新增/迁入默认插件，不增加第七类扩展点 |
| 原 schema 包 load.go、jwt_command.go 与测试（现 历史路径「internal/plugin/builtin/config/load.go」（已迁移或移除） 及同包 JWT/加载测试） | 解析器与部分配置读取迁入默认配置子包；schema、默认值、校验留原包；共享辅助函数随真实调用关系一起迁移，禁止留下反向 import 的兼容 wrapper |
| `internal/platform/data/{open,redis,init,mysqlversion,resources,tracing}.go` 及对应测试 | 默认 opener/probe 移入所属默认子包；Resources 收敛为借用视图；探活/追踪消费能力；参数化 SQL 日志接选定 logger |
| `internal/pkg/log/logger.go`、对应测试 | 收敛为项目门面，实例构造和 notifier 装配迁入默认 logging 子包；支持现有日志语义 |
| `internal/pkg/httpcache/cache.go`、对应测试 | 默认响应缓存实现继续复用；消费端改接口，不反向选择默认实现 |
| `internal/bootstrap/{plugin_registration,runtime,migrateruntime,processruntime,providers,app}.go` 及对应测试 | 类型明确的插件输入、能力解析、失败回滚与唯一管理入口；保留固定骨架 |
| `internal/router/init.go`、对应中间件顺序/基线测试 | 从组合根接收构建好的有序链；引擎配置与路由、安全语义保持 |
| `cmd/{server,migrate,doctor,jwt,module}.go`、必要的 `main.go` 装配参数、对应测试 | 默认选择留在入口，轻量命令按需构造；CLI 注册机制本身不改造 |
| 四个 repository 集成测试：`userrole`、`aiconversation`、`dbtx`、`useraccount` 下 `integration_test.go` | 只同步配置加载入口引用，不改 SQL/业务断言或实际连接目标 |
| `scripts/check-architecture.sh`、`docs/architecture.md`、`docs/observability.md`、`internal/pkg/log/README.md`、对应门禁/文档测试 | 代码实施后同步边界、日志说明和真实可执行判据，不在本轮提前改成已实现 |

日志调用迁移的已识别生产文件如下；仅替换日志 import/调用和必要字段构造，不改变所在模块业务行为。重叠文件合并处理，不视为额外模块重构：

- 入口：`cmd/signals.go`。
- Bootstrap：`app.go`、`http.go`、`listener.go`、`migrateruntime.go`、`pprof.go`、`processruntime.go`、`providers.go`、`ratelimit.go`、`runtime.go`、`singleton_worker.go`、`workermanager.go`、`worker_scheduler.go`。
- 中间件：`internal/middleware/{error,ratelimit,recovery,requestid,routetimeout}.go`；访问日志消费者 `internal/middleware/logger.go` 同步去掉具体实例和 SDK 字段耦合。
- 其他共享代码：`internal/pkg/{auditlog/auditlog,breaker/breaker,httpcache/cache,log/logger,news/news,response/response,upload/uploader}.go`。
- 资源和旧插件：`internal/platform/data/{init,resources}.go`、`internal/plugin/{redis,cache}.go`。
- 模块适配和 HTTP：`internal/runtime/module/ai/module.go`、`internal/runtime/module/auth/adapters.go`、`internal/module/ai/transport/http/handler.go`、`internal/router/health.go`。

上述文件的直接关联测试随调用迁移同步。阶段开始前按真实引用重新核对清单；若需要改变 application/repository 业务接口、目录层级或未列出的组件能力，停止依赖该变化的步骤，先补计划与对应授权，不按 glob 扩大改动。

不修改 `go.mod`、`go.sum`、`go.work`，不改真实环境配置值、数据库迁移、队列实现或外部服务。现有业务对 `ormx/paginator` 的依赖保持。上游 gtkit 修改是条件项，必须先给独立 API/文件/兼容验收清单并取得对应授权。

## 8. 分阶段实施步骤

### 8.1 各步骤共同执行纪律

阶段 1–4 的前置条件首先是：本版文档已经确认，且用户明确授权了对应代码阶段。每一步还必须读取保存计划、最新用户指令、全局必读规则及项目错误记录，按主题加载必要规范与 skill。

代码阶段开始时才核对 OpenSpec CLI 和 active changes；只复用明确匹配的 change，歧义时停止选择；CLI 不可用或未 apply-ready 不改应用代码，除非用户明确授权跳过。不因旧计划的手工 artifacts 或历史 CLI 故障自行绕过。阶段 1–2 均复用匹配的 `add-plugin-lifecycle-registry`；Core 阶段已确认 apply-ready，阶段 2 同步本阶段任务并严格验证，不归档仍含阶段 3–4 待办的 change。

每一步按下表产出代码/文档及证据，表内“检查/证据”同时是验收判据。任何失败、未执行、缺真实依赖、超时未退出或验收不达标，均保留为失败/未验证；同一问题连续自纠三轮不收敛即停止。完成当前获准阶段后更新本计划实际结果并汇报，不自动进入未授权阶段。

### 阶段 0：计划与基线（文档阶段已完成）

| 步骤 | 前置条件 | 具体改动/交付物 | 检查方法与验收证据 | 停止条件 | 当前状态 |
|---|---|---|---|---|---|
| 0.1 | 用户已认可评审方向并指定本文件 | 核对旧计划、分支和现有文件，修正当前事实与历史记录的边界 | 只读文件/状态输出与本计划事实一致；不运行代码门禁 | 无法读取必读规则或旧计划；发现非本轮文档授权 | 已完成只读核对 |
| 0.2 | 0.1 完成 | 仅保存本版完整计划，保留六类目标、阶段 0–4 与历史记录 | 对本文件全文回读，核对六类组件、步骤前置/停止/证据/状态、风险与回滚，无相互冲突的非目标 | 需要修改其他文件才能完成当前文档请求 | 已落盘并全文回读；仅文档核对完成 |
| 0.3 | 0.2 回读结束 | 给用户文档链接与确认摘要 | 用户已确认完整计划并授权阶段 1；阶段 2–4 仍待授权 | 未授权下一阶段不得执行 | 已确认，阶段 1 已授权 |

阶段交付物仅本文件。阶段 0 的完成不代表代码基线测试完成，也不授权应用代码修改。

### 阶段 1：Core 注册器与契约测试（定向验收完成）

阶段前置：阶段 0 完成、本版确认、阶段 1 代码授权及代码工作流前置条件满足。开始前记录受影响 Core 基线检查的真实结果，既有失败不冒充本轮引入或通过。

| 步骤 | 前置条件 | 具体改动/交付物 | 检查方法与验收证据 | 停止条件 | 当前状态 |
|---|---|---|---|---|---|
| 1.1 | 阶段前置满足 | 固定四方法、注册冻结、依赖校验、确定性排序及状态语义 | 空名/nil/typed-nil/重复/缺失/自依赖/多节点循环测试；图错误时回调次数为 0；默认 Core 依赖闭包无具体基础设施 | 需要业务类型或 SDK 进入 Core | 通过，见 13.2 |
| 1.2 | 1.1 契约固定 | 修复 F-01；分别保存 Init 清理集合、实际操作结果，取消分支同样 Join 清理错误 | A/B/C 全 Init 后 B Start 失败，断言 Stop C/B/A 且各一次；Init 当前部分取得资源后失败；根因和多个关闭错误均可 errors.Is 命中 | 无法区分未初始化、未启动和未退出资源 | 通过，先复现旧缺陷再验证修复 |
| 1.3 | 1.2 清理责任确定 | 修复 F-02/F-03，加入第 6.3 节通用预算与可取消等待，不重复剥离停止 context | 取消发生于 Init/Start/并发等待/Stop 各位置；回滚仍有可用预算；父 deadline 收紧；Stop 强制取消；无第二组并发清理；受控 fake 验证次数、顺序和退出 | 需要声称能杀掉不合作回调，或以遗留 goroutine 掩盖阻塞 | 通过，受控时间与并发契约已运行 |
| 1.4 | 1.1–1.3 定向用例完成 | 回读完整修改函数和调用方，形成阶段证据记录 | `GOCACHE=/tmp/gin-api-gocache go test -race -count=1 -timeout=5m ./internal/core`；记录命令、环境、退出码、case 与未验证项 | 相关全量失败或同一问题三轮不收敛 | Core 定向验收通过；全仓 lint 阻断单列 |

阶段交付：修复后的唯一 Registry、契约测试和真实阶段验收记录。Core 通过只证明注册器契约，不证明六类组件/真实外部依赖通过。

#### 阶段 1 实施细则（改代码前落盘；实际验收见第 13 节）

- 兼容：保留 `NewPluginRegistry()` 与 `Register/Init/Start/Stop` 签名及四方法接口；新增 `SetForceSignal(<-chan struct{}) error`，只能在生命周期开始前设置。这是生命周期取消接线，不是新增组件扩展点。原有调用无需修改；进程入口实际接线留阶段 3。
- 状态：锁只保护状态与操作发布，不包住插件回调。Start 在隐式 Init 前先取得唯一操作所有权；并发加入者共享结果，可用自己的 context 取消等待。Stop 等待在途操作，不抢跑另一组清理；等待与实际停止共用同一个 30 秒上限。停止结果缓存，不自动重启或重复关闭。
- 清理：Init 以已进入回调的前缀为清理集合；Start 以全部已初始化插件为清理集合，包括未 Start 的项。所有失败/取消分支聚合根因、context 错误和逆序 Stop 错误；Stop 单项失败仍尝试后续项。回调 panic 转为可判别的契约错误并进入同一清理路径，不恢复运行。
- 时间：首次 Init 固定启动截止时间（总 30 秒与父 deadline 较早者），Start 继续使用剩余额度；每次 Init/Start 上限 10 秒。回调返回时也核对 context，防止超时后返回 nil 被误报成功。回滚仅在清理边界脱离启动取消，保留 values，独立总计 30 秒；正常 Stop 保留父取消与 deadline。
- 强制信号：清理期间至多有一个信号观察 goroutine，监听强制信号及清理结束；返回前取消并 join。等待者直接 select 调用方取消/操作完成/停止强制信号。禁止用每回调启动 goroutine、超时后遗弃的办法伪造硬中断。
- 内存/性能：注册时复制依赖、冻结规范化名称；图与清理集合按注册量分配，不放入请求热路径，不保留已结束调用的 context。拓扑使用注册序号最小堆确定稳定顺序（O(E+V log V) 时间、O(V+E) 空间）；环路径用迭代查找，避免图深度带来的递归栈增长。没有运行期增长的历史结果队列或无界重试。基准只测 Core 合成图与分配，不能证明组件、生产吞吐或永不 OOM。
- 测试顺序：先保存基线和回退快照；新增三插件 Start 中途失败、取消与清理错误反证并实际观察旧实现失败；随后实现并用 `testing/synctest` 验证预算和并发交错，再运行 Core 完整 race/覆盖率、调用方定向回归、build/vet/lint、依赖与差异检查。检查失败按真实归因处理，不修改阶段外历史问题，不降低门禁。
- 验收/停止：1.1–1.4 均保存实际命令、退出码、用例映射及未验证项；更新当前阶段任务。核心契约失败则不交付为通过；不合作插件的无限阻塞、任意内存分配、Goexit/进程退出及真实组件资源释放不属于 Core 能硬隔离的能力，不写“保证永不 OOM”。阶段 2–4 继续待实施/待授权。

### 阶段 2：六类内置插件、能力边界与资源所有权（本阶段定向验收完成，限制单列）

阶段前置：阶段 1 验收通过并获阶段 2 授权。每个组件先按真实引用列出配置字段、路径、键/逻辑库、TTL、超时、日志与调用方兼容清单，再迁移；不凭印象补齐。由于接口/包拆分会影响现有引用，表内每一步包含它的必要调用方和测试引用同步，构成可编译的原子步骤；完整入口组合与关停切换留在阶段 3。

| 步骤 | 前置条件 | 具体改动/交付物 | 检查方法与验收证据 | 停止条件 | 当前状态 |
|---|---|---|---|---|---|
| 2.1 | 阶段前置；配置/JWT 读取和共享 helper 清单齐全 | 建立配置能力；schema/校验留原包，koanf 全部默认读取移入 builtin/config，迁移 server/migrate/doctor/JWT/module 和四组集成测试的必要引用 | 加载、APP_ 覆盖、APP_ROOT 排除、未知键/缺文件/权限拒绝、非法配置、JWT 部分配置、未 Init 取值用例；替代 loader 不改变 Core；schema 包不再导入 koanf | 需要改配置键、默认值、路径、凭据校验或形成 config↔builtin 环 | 通过（配置能力/定向回归），见 14.2.1 |
| 2.2 | 2.1 配置能力可用；现有日志调用与字段/告警清单齐全 | 建立日志事件契约/门面及 builtin/logging；迁出 notifier 构造；逐项迁移第 7 节日志调用与关联测试，业务 port 保留 | 替代 logger 接收到主/访问/channel/告警和 context 字段；SQL adapter 脱敏；正常与部分构造失败关闭各一次；门面不依赖 gtkit/zap/news；无告警递归；Sync 错误限制显式记录 | 出现字段/脱敏/告警丢失，或真实上游缺口尚未获对应授权 | 定向通过；SDK Sync 限制受限，见 14.3.1 |
| 2.3 | 2.1–2.2 能力可注入 | 数据库能力与 MySQL 默认实现拆开，opener/probe 移入所属默认包；数据视图不拥有连接；迁移保留用途区分 | 真正拥有连接的 fake/可关闭资源断言 Close 一次；开库后版本/会话校验失败回收；替代 Database 实现；服务与迁移 deadline/凭据保持；只读视图不关闭资源 | 需要改仓储/事务/方言、重复 owner 或绕开 MySQL 安全校验 | 定向通过，见 14.4.1 |
| 2.4 | 2.1–2.2 能力可注入 | Redis 默认实现与消费能力拆开，opener/probe 移入所属默认包；可用性由视图适配，不强制具体 tracker | 未配置、required 不可达、optional 不可达、nil/部分资源+error、取消和重复关闭；逻辑库/键前缀无变化；关闭次数可观察 | 引入重连/热替换、新逻辑库/前缀、降级吞取消或出现第二关闭者 | 定向通过，见 14.5.1 |
| 2.5 | 2.4 Redis 能力固定 | 默认缓存独立；consumer 只用 Enabled/Middleware，旧 Stager 留实现内部；内存/Redis 按真实依赖注册 | 两后端和禁用态；第三方替身无需创建 Stager；身份请求绕过、响应准入、显式挂载；cache Stop 不关 Redis；本地 store 只释放一次 | 自动扩大缓存范围、改变 key/TTL/容量，或借用资源被关闭 | 定向通过，见 14.6.1 |
| 2.6 | 配置/日志契约稳定，已核对现有链序与观测创建时间 | 中间件组合插件及 Build 具名入参；router 只安装返回链；普通 handler 不插件化 | 事件顺序与第 5.2 节一致；仪表先于 Build；413/404/405/OPTIONS/业务 panic/观测中间件 panic 保持响应、日志、指标；不重复安装链 | 默认安全能力遗漏或排序改变，或为顺序新增通用优先级框架 | 通过（定向验收），见 14.7.1 |
| 2.7 | 2.1–2.6 完成 | 核对六类契约、独立实现包、数据借用视图与兼容层；补齐阶段证据 | 按实际包清单运行六类插件、config/data/log/httpcache/router/middleware 及本阶段改动调用方的定向全量 race 测试；mock/内存证据与真实依赖证据分开记录 | 关键组件仅测试构造/指针置空，不能证明真实资源关闭；或扩大到第七类插件 | 通过（本阶段证据），SDK/发布限制单列，见 14.8.1 |

阶段交付：六类可替换契约、默认子包、必要引用迁移、资源/调用方映射与测试记录。旧 NewResources 自有构造及相关测试在确认无生产调用后迁移其测试责任并移除实现依赖，不保留永久双模式或删除故障覆盖来获得通过。

### 阶段 3：Bootstrap/迁移接线与关停回归（定向验收完成）

阶段前置：阶段 2 通过并获阶段 3 授权；不存在未说明的日志能力缺口或 owner 冲突。若阶段 2 保留已知限制，阶段 3 必须继承该未验证项，不将其改为成功。

| 步骤 | 前置条件 | 具体改动/交付物 | 检查方法与验收证据 | 停止条件 | 当前状态 |
|---|---|---|---|---|---|
| 3.1 | 阶段前置；所有构造无资源副作用 | 调整入口 registrar 签名与能力集合；默认实现只由 main/cmd 显式选择；配置先 Init，其他插件后取 Value | 入口事件账本证明注册期无加载/建连、配置只加载一次；能力与已注册实例一致；替代组合无需改 Core；Bootstrap 不导入 builtin | 需要让 Core 隐式选实现或反射解析依赖；出现未注册却已创建的资源 | 定向验收通过；证据见 15.5，原停止条件继续适用 |
| 3.2 | 3.1 入口组合确定 | Runtime/newProviders 消费能力；复用已有 telemetry/metrics 顺序；缓存/中间件不再通过具体对象取用 | NewApp 后段 JWT/仪表/模块/BuildEngine 任一点失败，扩展与插件按正确顺序回滚；Stager/具体 SDK 不泄露消费端；前后行为一致 | 正式仪表创建前绑定到 no-op，或失败路径丢弃清理错误 | 定向验收通过；证据见 15.5，原停止条件继续适用 |
| 3.3 | 3.2 装配回滚通过 | Runtime/App 停止 ctx 接线，日志独立排空合并 owner，保留固定 worker/HTTP 阶段 | 正常信号、启动期信号、第二信号、HTTP 异常、worker 异常、存活 worker、部分 Stop 失败、超时；借用资源只关一次；未退出资源返回非成功结果 | 提前关闭存活 worker 所需插件资源、第二信号失效、无界等待或假成功 | 定向验收通过；证据见 15.5，原停止条件继续适用 |
| 3.4 | 3.1–3.3 能力组合可用 | migrate 只选配置/日志/数据库；doctor/JWT/module 保持最小读取和探测；移除 Bootstrap 的具体 ormx client 契约 | 无 Redis 仍能完成迁移装配测试；server/migrate 身份不同规则保持；DDL 不继承服务读写 deadline；doctor JSON/退出码和默认无网络行为保持；JWT 命令不启动主应用 | 轻量命令引入无关依赖或改变 CLI/迁移安全语义 | 定向验收通过；证据见 15.5，原停止条件继续适用 |
| 3.5 | 3.1–3.4 实际通过 | 同步第 7 节架构/日志文档、针对性架构判据及相关测试；记录范围内调用方复核 | 受影响 bootstrap/cmd/router/middleware/module adapter 的定向全量 race 测试；源码/需求/测试/文档逐项映射；未改目录与业务接口 | 文档描述超前、隐藏存量依赖或通过放宽门禁解决失败 | 定向验收通过；证据见 15.5，原停止条件继续适用 |

阶段交付：入口手工注册、六类完整接线、最小命令组合、关停/回滚证据和反映已实现行为的文档。阶段结束仍不执行 commit/push 或上线。

### 阶段 4：全量质量、架构和验收（已完成，含实际归档回读）

阶段前置：阶段 1–3 按本版标准通过，且获阶段 4 对应验证授权。当前版本的 Makefile 和质量规范是命令/覆盖率门禁唯一来源，不降低覆盖率或削弱断言。

| 步骤 | 前置条件 | 具体改动/交付物 | 检查方法与验收证据 | 停止条件 | 当前状态 |
|---|---|---|---|---|---|
| 4.1 | 阶段前置，工具入口/版本和可写 cache 已确认 | 执行正常构建与完整质量检查，修复仅限本次引入的问题 | `GOCACHE=/tmp/gin-api-gocache go build ./...`；`GOCACHE=/tmp/gin-api-gocache make check`；覆盖含 scaffold 的 vet/lint/race/覆盖率及真实 Makefile 全部 check prerequisites，记录命令环境/退出码/结果 | 工具不匹配、既有失败无法归因或需要范围外修复 | 通过：双环境 build/make check exit0，均82.9%；见16.5；未发布 |
| 4.2 | 4.1 可运行 | 分别核对本地 workspace 与发布依赖结果；用替身逐类替换并检查依赖边界 | `GOWORK=off` 下重跑构建及受影响完整测试/门禁；记录 logger 解析来源；Core/契约/门面无默认 SDK 反向依赖；固定图确定排序、无 import cycle | 只能在本地较新 logger 编译或依赖缺失，不能作为发布通过 | 通过：双环境完整门禁/依赖来源/替换契约已复核；保留 SDK 差异，见16.5 |
| 4.3 | 明确为本地可用测试资源；真实依赖访问授权满足 | 验证真实 MySQL/Redis 启动、请求/缓存、失败恢复与释放，保留响应与资源证据 | 先查本机已有镜像/容器并复用；实际服务请求、两类缓存及信号退出；验证逻辑库/键/配置/日志脱敏不变；mock 与真实证据分列 | 无本地依赖或指向生产；缺镜像/容器未获授权；不创建替代数据或假报通过 | 通过：复用本地 MySQL/Redis，两环境各8 pass/0 fail/0 skip；无 DDL/生产操作，见16.5 |
| 4.4 | 4.1–4.3 结果齐全且 OpenSpec 前置有效 | 对第 9 节逐项给结果，完成实际代码 change 的严格验证/符合条件的归档，更新计划事实状态 | 在获准代码流程中执行相应 CLI 严格验证与正常门禁；仅全部要求满足时完成，并核对归档实际输出/目录结果；报告限制、失败及回滚能力 | 任何核心项失败/缺证据，不勾选通过、不宣称完成/Ready | 通过：矩阵、隔离回退、严格校验、实际归档及主规格回读均完成；见16.5–16.6 |

阶段交付：完整证据矩阵、执行环境差异、真实资源与退出行为结果、未验证项和最终结论。完整 change 已在阶段 4 实际归档回读，见16.6；没有在早期仍含后续待办时提前归档。

## 9. 最终验收矩阵（分阶段填写）

| 编号 | 验收标准 | 必须保存的证据 | 当前结果 |
|---|---|---|---|
| A-01 | 唯一 Core Plugin 四方法；无动态加载、MQ 预埋、Core 基础设施 import | 文件/依赖检查及相关契约测试 | 通过：唯一四方法、Core 标准库闭包、六类能力；未增加动态加载/MQ/JSON 生命周期插件 |
| A-02 | 六类扩展点均可替换，默认实现由组合根手工选择 | 六组替身组合测试；替换时 Core 无需改动的记录 | 通过：六类独立替身、cmd 显式注册/懒构造、Bootstrap 消费所选实例，Core 未改 |
| A-03 | 图错误在任何生命周期副作用前失败，排序确定且无循环 | 重复/缺失/自依赖/环/独立插件排序测试与回调计数 | Core 契约通过（含缺失、自环、多节点环、稳定就绪顺序） |
| A-04 | Init 部分失败、Start 中途失败/取消时完整逆序清理 | 三插件失败点不在末尾、当前部分资源、多个关闭错误、各 Stop 次数/顺序 | 通过：Core 部分 Init/Start 中途失败逆序清理、一次关闭与错误链；真实 MySQL 失败后恢复及连接池释放通过（16.5），真实任意第三方 Start 故障不在覆盖范围 |
| A-05 | 等待可取消、回滚独立有界、父预算只收紧、强制信号贯通 | 受控时间/阻塞 fake 测试和最外层 App 信号退出证据 | 通过（合作式契约）：共享回滚/Stop 各30秒、父取消/更短 deadline/force、外层失败与并发反证；真实首/第二 SIGTERM 两环境通过；不宣称硬中断 SDK |
| A-06 | 配置 schema/校验与默认加载器分离，所有命令读取语义兼容 | YAML/env/权限/未知键/JWT 专用路径及调用方测试 | 通过（阶段 2）：加载/环境/权限/未知键/JWT 及 cmd/bootstrap 回归；真实配置未改 |
| A-07 | 日志可替换且原字段、脱敏、关联、访问/channel/告警行为保持，实例只关闭一次 | 替代 sink 事件、真实日志产物/敏感值反证、部分初始化与排空测试；Sync 错误限制单列 | 受限：日志替换、文件/字段/脱敏/主访问告警/在途写入排空及双 SDK 回归通过；用户允许仅 HTTP ErrorLog 类型桥；void Sync 不可证明送达或硬 I/O 终止 |
| A-08 | MySQL 与 Redis 各唯一 owner，缓存借用连接，Resource view 不关资源 | 实际 Close 计数和真实池/连接释放证据，不只检查字段变 nil | 通过：owner/借用/缓存不关闭 Redis 契约；真实 SQL Ping 已关闭且 OpenConnections=0，Redis Ping 失败且 TotalConns=0，重复关闭通过；两环境一致 |
| A-09 | MySQL 安全/会话/凭据与迁移 deadline 不变，Redis required/optional 不变 | 真实探活和失败场景；服务/迁移身份及错误链证据 | 通过（本期兼容范围）：SQL 会话/安全/运行与迁移凭据选择/DDL deadline、Redis required/optional 契约；真实本地启动及迁移只读 Ping/关闭、MySQL 故障后恢复通过；未执行真实 DDL/生产双身份权限验收 |
| A-10 | 内存/Redis 缓存、禁用/降级、key/TTL/身份及响应准入不变 | 两后端真实请求及故障注入结果；本地 store 退出/Redis 未被 cache 关闭 | 通过（功能）：两环境真实 memory/Redis HTTP 命中、身份绕过、独有前缀/TTL/键清理、释放通过；禁用/降级/准入及借用保护契约通过；生产容量/峰值内存未验证 |
| A-11 | 中间件组合可替换，顺序/安全/观测与异常响应保持 | 请求事件账本、OPTIONS/413/404/405/panic、重复安装拒绝 | 通过（阶段 2）：替代组合、链序、最终仪表、OPTIONS/413/404/405/panic、并发 Build 和非法链拒绝 |
| A-12 | Bootstrap/HTTP/worker 既有阶段顺序和最小命令组合保持 | 外层装配失败、正常/强制退出、存活 worker、migrate/doctor/JWT/module 回归证据 | 通过：外层失败、HTTP/worker 顺序、存活 worker、最小迁移/轻量命令回归；两环境真实子进程就绪、首 SIGTERM 正常退出、第二信号非零退出 |
| A-13 | workspace 与锁定依赖均通过所需构建/测试，质量门禁不降低 | 两环境实际命令、依赖来源、退出码、覆盖率明细与 Makefile 门禁结果 | 通过：workspace 与 GOWORK=off 正常 build/完整 make check 均 exit0，完整覆盖率均82.9%≥72.5%；两环境真实套件各8 pass/0 fail/0 skip，依赖边界无放宽 |
| A-14 | 文档只描述实际行为，change 验证完成，回滚边界可复核 | 需求→文件→用例→结果映射、实际验证/归档及阶段回退记录 | 通过（本次范围）：完整矩阵/阶段记录、严格change与主规格验证、实际归档回读及隔离回退build/四包race完成；未执行生产/数据回滚 |

结果只使用“通过、失败、未执行、未验证、受限、不适用”，并注明依据。某类未启用不等于它的替换/降级测试不适用。真实依赖不足、日志关键字段/关闭结果无法证明、核心失败路径缺证据时，不给完整验收通过结论。

## 10. 风险、失败处理与回滚

### 10.1 风险与处理责任

后续获准执行阶段的实施者负责维护下面的证据，阶段验收由用户核对；不编造外部负责人、期限或已协调的生产窗口。

| 风险/触发条件 | 影响 | 处理与停止规则 |
|---|---|---|
| 配置/日志/插件之间形成包环或隐式全局取值 | 构建失败或初始化顺序不可解释 | 只向契约依赖；构造无 I/O；notifier 构造离开日志门面；失败停止当前步 |
| 日志调用、SQL/访问日志仍直连具体 SDK | 看似可替换但默认实例仍被使用 | 按第 7 节逐文件迁移并用替代 sink 验证；不得只改初始化 |
| default/logger 本地 v2.5 与锁定 v2.3 行为不同 | 本地通过而发布失败 | workspace/GOWORK=off 分开验证；不偷偷升级 go.mod |
| Init 已取资源、Start 未到达或 opener 同时返回资源与错误 | 连接/缓存工作泄漏 | 清理集合和所有权账本；当前插件与未启动插件的反证测试 |
| 子插件未退出时先关闭父依赖，或重复 Close | 请求/worker 收尾失败 | Stop 逆序、存活工作检查、borrower 无关闭权；未退出报告失败 |
| 关闭 API 不合作或不返回错误 | 超预算、错误不可见 | 声明能力限制、保留非成功结果；必要上游补强先列 API/兼容验收并获授权 |
| 配置/loader/默认 opener 拆分遗漏 CLI 或测试调用方 | 迁移、诊断、JWT 或开发期命令回归 | 同步第 7 节调用清单；轻量命令和现有用例不降覆盖 |
| 历史门禁失败或受限环境被当作通过 | 交付结论失真 | 历史记录与本版证据分开；未解决项不得勾选 |

### 10.2 启动与停止失败处理

拓扑/注册失败不进入插件回调。配置失败没有正式 logger，写最小 stderr 并返回根因。日志部分构造失败由日志插件回收已建立对象。资源/缓存/中间件装配失败由组件内部与 Registry/Bootstrap 按各自实际取得的所有权逆序回收；两层不得重复关闭同一资源。

停止错误聚合并进入命令返回值；不能把夹带关闭错误的 `context.Canceled` 整体归一化为成功。强制退出与未退出 worker 保持非成功语义。没有实际完成的资源回收、日志排空或迁移回滚不写成功。

### 10.3 分阶段回退

1. 每个获准代码阶段开始前，保存本次差异与原有工作树清单/内容作为恢复依据；保留当前未提交改动，不以 HEAD 覆盖用户工作。
2. Core 回退只撤回本阶段 Registry/测试差异；不得用旧版已知泄漏行为宣称本版验收通过。
3. 能力拆分回退时，成组恢复该组件的实现路径、契约和全部调用方，恢复唯一 owner；不留下新旧路径同时创建资源。
4. Bootstrap 接线回退只恢复本阶段前的完整装配/关闭路径，不单独切回 Resources 构造却保留插件关闭，避免双重 owner。
5. 本期没有数据库 schema 或持久化数据迁移，不执行数据补偿。出现连接目标、键名、数据结构、配置语义变化时停止并补计划，而不是现场改数据。
6. 回退采用可审阅的定向补丁，不执行 reset --hard、clean、批量删除、改写历史或覆盖非本次内容。真实回退演练、生产灰度、commit/push/tag/release 均需对应授权；当前没有生产发布安排。

## 11. 实施前仍须核对的事项

以下保留实施前核对边界；阶段 1–2 的实际结果分别以第 13–14 节为准，阶段 3–4 已完成，结果见第 15–16 节。真实本地依赖与归档已核对，SDK关闭限制、生产容量与部署退出宽限期仍按未验证/受限处理：

- 用户已明确授权继续阶段 3 并依原序完成阶段 4；JSON 不增加生命周期插件、不迁移版本。稳定性、性能、并发和内存要求按证据验收，不将普通工具或没有替换需求的业务模块插件化。
- 阶段 1–2 的匹配 change 已严格验证；未来继续只复核同一 change，不修改无关 change，不把 artifacts 齐备误报为全部实施任务完成。
- 真实本地 MySQL/Redis 的可用性和实际连接目标。缺镜像/容器先说明并取得对应授权，不隐式拉取，不触达生产。
- 非 context 关闭 API 的真实行为、日志 Sync 错误限制是否需上游专项补强及其授权；未补强前保留受限结论。
- 部署退出宽限期与既有 HTTP/worker/日志预算关系；未经核对不承诺硬退出时长。容量、压测和生产灰度数据未提供，本期不改容量配置或发布。
- 旧文档曾记载 ERR-20261020-138、140、141；实际当前错误日志没有这些条目，不能推断状态或恢复历史。阶段 1 新增 001–004，阶段 2 新增 005 起的条目；原 004 格式阻断已在阶段 2 修复并复验，具体状态以当前错误记录与 14.8.1 为准。

## 12. 旧版执行记录（历史保留，本版未复验）

以下保留修订前计划中的记录，来源为旧文档，不能替代本轮命令输出或本版扩展目标的验收：

| 原阶段 | 旧文档记载 | 本版解释 |
|---|---|---|
| 0 | 原 MySQL/Redis/Cache 范围计划及手工 OpenSpec artifacts 已回读 | 原范围遗漏配置/日志/中间件；本轮仅修订本文件，不同步 artifacts |
| 1 | `gofmt -w internal/core/*.go`；`GOCACHE=/tmp/gin-api-gocache go test -race ./internal/core -count=1`，旧记载 exit 0；曾记录 ERR-20261020-133 | 不证明本版 F-01/F-02/F-03、三插件中途失败、预算等待已通过 |
| 2 | 插件/data 定向 race 测试旧记载 exit 0；miniredis loopback 受限，记录 ERR-20261020-135 | 不证明真实连接释放或六类替换；不能因受限而移除必须的真实验收 |
| 3 | bootstrap/plugin/data/cmd 定向 race 测试旧记载 exit 0；原三插件接线已写入工作树 | 与当前已有改动区分；六类入口、日志消费端、最小命令与关闭修复均待本版验证 |
| 4 | 旧计划标为待开始；错误记录随后有 make check/coverage 失败信息 | 完整门禁无本轮通过证据，不把历史阶段 0–3 状态沿用为本版完成 |

## 13. 阶段 1 状态与交付记录（历史保留）

### 13.1 授权、环境与改动清单

2026-10-21（Asia/Taipei）在 `pluggable` 分支执行。用户已确认完整方案，仅授权阶段 1；回滚与插件停止总预算各 30 秒。阶段 2–4 未执行，未进行 commit/push/tag/release、容器、数据库操作、依赖升级或目录调整。

阶段 0 文档已实际落盘并全文回读；当时仅修改本文件，旧文档阶段的无代码/无测试边界保持为历史事实。本次在代码修改前补齐阶段 1 实施细则并回读，同步匹配的 `add-plugin-lifecycle-registry` artifacts，CLI `instructions apply` 返回 `state: ready` 后才修改 Core。

- 工具：`go version` 为 `go1.27.2 darwin/amd64`；golangci-lint `2.14.0`（built with go1.27.2）；当前 Node `/Users/xiaozhaofu/.nvm/versions/node/v24.22.0/bin/node`。
- 本机 OpenSpec 入口为 `/Users/xiaozhaofu/.nvm/versions/node/v24.6.0/lib/node_modules/@fission-ai/openspec/bin/openspec.js`，版本 1.3.0；以当前 Node 执行，不安装新工具。以下 OpenSpec 命令统一带 `OPENSPEC_TELEMETRY=0`。
- Go 检查统一使用 `GOCACHE=/tmp/gin-api-gocache`；lint 同时使用 `GOLANGCI_LINT_CACHE=/tmp/gin-api-golangci-lint`（已由 `golangci-lint cache status` 核对）。构建/vet 使用 `-p=4` 限制并行编译。
- 原工作树/Core/plan/change/错误日志快照：`/tmp/gin-api-stage1-go53obnh`，含 `worktree-sha256.json` 和 `git-status.txt`。这是临时回退依据，不是提交或永久归档；未对工作树执行实际回退。
- 本阶段代码：历史路径「internal/core/plugin.go」（已迁移或移除）、`plugin_registry.go`、`plugin_registry_test.go`、新增 `plugin_registry_contract_test.go`、`plugin_registry_benchmark_test.go`。
- 配套记录：本文、匹配 change 的 proposal/design/tasks/spec 及本次错误日志。对快照中所有阶段外脏文件逐一比对 SHA256，结果无变化；全仓 lint 报告的 `internal/platform/data/resources_test.go` 也与快照一致。

### 13.2 步骤验收与调用方核对

| 步骤/不变量 | 已实施行为 | 可复核证据与结果 |
|---|---|---|
| 1.1 唯一契约/图 | 四方法保留；零值 Registry 可用；注册复制依赖并冻结名称；图错误零回调；按注册序号选择就绪节点；迭代环查找 | `RegisterValidation`、`RegistrationMetadataIsFrozen`、`GraphValidationAndStableReadyOrder`、`DeepGraphDoesNotRecurse`；4096 节点链/环；Core 依赖闭包仅自身与标准库；通过 |
| 1.2 清理集合与错误链 | Init 清理已进入前缀；Start 清理全部已 Init 项；C/B/A 逆序各尝试一次；取消及多个关闭错误均保留；panic 转契约错误并继续清理；失败结果缓存 | `StartFailureCleansAllInitialized`、`CancellationJoinsCleanupErrors`、`InitFailureRollsBackCurrentAndCompleted`、`PanicStillCleansAndPreservesErrors`、`FailureResultsAreCached`；旧版先 exit 1，修复后通过 |
| 1.3 并发所有权 | Start 覆盖隐式 Init 的唯一操作；Init/Start/Stop 等待可取消；不竞争清理；Stop 等启动/回滚结束且只尝试一次 | `ConcurrentStartupSharesResult`、`StartWaitingForExternalInitCanCancel`、`StopWaitsWithoutConcurrentCleanup`、`ConcurrentStopSharesErrorAndAllowsWaitCancellation`、`ConcurrentRegistration`、`StopCannotDuplicateInFlightRollback`；通过 |
| 1.3 预算与信号 | 启动共 30 秒、单 Init/Start 10 秒；回滚独立 30 秒并保留 values；停止含等待共 30 秒、保留更短父预算；强制信号可切断清理和 Stop 等待；观察协程结束前 join | `StartupBudgets`、`RollbackDetachesStartupCancellation`、`CleanupBudgetsAndForce`、`StopBudgetIncludesWaiting`、`StopWaitCanBeForced`、`ForceObserverExitsOnSuccessfulCleanup`、`StartupDeadlineDoesNotConsumeRollbackBudget`；synctest 受控时间通过，包括父启动 3 秒耗尽后仍有 30 秒回滚、合计 33 秒虚拟时间 |
| 1.4 调用方 | `NewRuntime`、`closeRuntimeOnFailure`、`closePluginRegistryOnFailure`、`Runtime.Close`；迁移构造、失败处理和 Close；Registrar 与内置插件 | 原构造/生命周期签名保持，错误仍返回调用方；现有 Runtime/migration 会调用缓存 Stop 结果，不重复清理。原入口尚未接 `SetForceSignal` 且部分路径有 `WithoutCancel`，明确留阶段 3；本阶段不宣称进程级强制退出已贯通。 默认 MySQL/Redis/cache 的 Stop 当前调用不接受 context 的底层 Close；Core 不伪造硬中断，该组件限制留阶段 2 核对。plugin/bootstrap/cmd 定向 race 通过 |

并发反证另在隔离临时模块 `/tmp/gin-api-core-counterfactual-81i2mp3q` 执行：使用阶段前 Core 快照、受控 Init barrier 和 33 个并发 Start，旧实现有 25/33 返回错误生命周期结果，命令 exit 1；换成本阶段 Registry 后，同一测试 exit 0。该数字是一次确定交错中的实测结果，不是故障概率/吞吐指标，未改写工作树进行反证。

### 13.3 实际检查与退出码

| 检查 | 实际命令（除单独注明外均使用上方 Go/lint cache） | 退出码与结果 |
|---|---|---|
| 阶段前 Core 基线 | `go test -race -count=1 -timeout=5m ./internal/core` | 0；原用例通过，不能覆盖新增缺陷 |
| 新增反证对旧实现 | 同上，加入三插件中途失败、取消错误聚合、独立回滚用例 | 1；实际复现 C 漏停、清理错误丢失、回滚继承取消/无 deadline；不是最终检查失败 |
| 最终 Core 完整契约 | `go test -race -shuffle=on -count=1 -timeout=5m -coverprofile=/tmp/gin-api-core-stage1.cover ./internal/core` | 0；30 个顶层测试及其表驱动场景，无跳过；语句覆盖率 98.0%，耗时输出 2.447s；日志 `/tmp/gin-api-stage1-core-race.log` |
| 覆盖率明细 | `go tool cover -func=/tmp/gin-api-core-stage1.cover` | 0；所有公开方法非零，Register/Init/SetForceSignal 100%，Start 96.9%，Stop 95.7%；不冒充全仓覆盖率 |
| 调用方定向完整回归 | `go test -race -tags=scaffold -count=1 -timeout=5m ./internal/plugin ./internal/bootstrap ./cmd` | 0；分别输出 7.188s / 8.765s / 9.397s；未启用真实依赖 integration 标签；日志 `/tmp/gin-api-stage1-callers.log` |
| 全仓正常构建 | `go build -p=4 ./...` | 0；有一次模块 stat cache 不可写警告，原样保留在 `/tmp/gin-api-stage1-build.log`；不修改依赖/cache 权限 |
| 全仓 vet | `go vet -p=4 -tags=scaffold ./...` | 0；`/tmp/gin-api-stage1-vet.log` 无输出 |
| Core lint | `golangci-lint run ./internal/core` | 0；0 issues；日志 `/tmp/gin-api-stage1-core-lint.log`。私有 heap 的两处 int 断言用定点注释说明 `container/heap` 的 any API 与内部索引不变量，未改 linter 配置 |
| 全仓 lint | `golangci-lint run ./...` | 1；仅 `internal/platform/data/resources_test.go:270` 的 gofmt 问题；阶段外原文件未改，保留 ERR-20261021-004；日志 `/tmp/gin-api-stage1-lint.log` |
| 现代写法 | `go fix -diff ./internal/core` | 0；无输出，未自动改写阶段外文件 |
| Core 依赖闭包 | `go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./internal/core` | 0；唯一非标准库包是 `github.com/gtkit/gin-api/internal/core`，无 SDK/动态 plugin 依赖 |
| OpenSpec 严格验证 | `node <已核实入口> validate add-plugin-lifecycle-registry --type change --strict --json --no-interactive` | 0；valid=true，issues=[]；artifacts 齐备不表示全部 23 项实施任务完成，当前仅完成阶段 0–1 的 7 项 |

未运行 `make check`、全仓 race/覆盖率棘轮、`GOWORK=off` 全仓发布依赖验收、真实 MySQL/Redis 请求、容器、生产压测、信号端到端或真实回退演练。这些属于未授权的后续阶段；隔离 Core 反证使用 `GOWORK=off` 仅隔离本机 workspace，不能视为完整发布依赖验证。

### 13.4 性能、内存与安全边界

命令：`go test -run='^$' -bench='^BenchmarkPluginRegistryLifecycle$' -benchmem -benchtime=100ms -count=3 ./internal/core`，exit 0。平台 darwin/amd64，Intel Core i7-9750H 2.60GHz；输出并行参数后缀为 `-12`。每次操作包含新 Registry 的注册、Init、Start、Stop，插件为无 I/O 替身，无强制信号；原始记录 `/tmp/gin-api-stage1-bench.log`。

| 图/插件数 | 3 次样本耗时范围 | 分配范围 | allocs/op |
|---|---|---|---|
| 独立 / 6 | 17.965–20.379 μs/op | 6848–6849 B/op | 88 |
| 依赖链 / 6 | 18.905–19.506 μs/op | 6968 B/op | 98 |
| 独立 / 128 | 323.096–329.121 μs/op | 143241–143242 B/op | 1207 |
| 依赖链 / 128 | 314.841–357.182 μs/op | 146289 B/op | 1461 |
| 独立 / 1024 | 2.576–2.672 ms/op | 1185138–1185250 B/op | 10056 |
| 依赖链 / 1024 | 2.565–2.688 ms/op | 1215835–1215841 B/op | 12870 |

这是本阶段合成工作负载的分配/耗时记录，没有前后性能收益对比或生产 QPS/p99 结论。没有为 Core 设置新的运行参数、内存池、监控插件或容量配置。注册图时间 O(E+V log V)、空间 O(V+E)；验证后释放依赖图边，终态释放插件执行切片；操作结果不会在 Registry 中累计成历史队列。反射仅用于注册期 typed-nil 校验，不在请求热路径中使用。

回调同步运行，不生成超时后被遗弃的回调 goroutine。只有安装强制信号时清理阶段有一个观察协程，退出前显式取消并 join；受控时间测试验证正常完成、超时及强制取消后的退出。代码不使用 unsafe，不新增外部输入或凭据，也不改变业务存储/权限。

Core 无法硬中断不合作回调、Goexit、进程退出或插件的任意内存分配，不能承诺任意实现永不 OOM。真实 SDK 的 Close 行为、连接池/缓存容量、HTTP/worker 退出和部署内存上限仍需相应阶段的真实组件与容量证据。回滚/停止超时意味着清理尝试结束且返回失败，不意味着资源已全部释放。

### 13.5 阶段结论与合规摘要

阶段 1 的 Core 定向验收通过；全仓 lint 阻断及阶段 2–4 未验证项保留，完整项目发布准入结论为“不准入/未完成”。阶段 2 未进入，无新增组件扩展点、MQ 或动态加载。匹配 change 仅阶段 0–1 勾选，其余任务待授权，不归档。

- [x] Go 1.27 现代写法：slices、WaitGroup.Go、synctest；Core go fix 无差异。
- [x] 分层架构：Core 只依赖标准库，阶段外原改动 SHA256 不变。
- [x] 错误处理：根因、取消、panic 契约错误和多个关闭错误可判别。
- [x] 并发安全：受控 Init/Start/Stop/Register 交错与 race 验证。
- [x] 超时控制：启动共享预算、独立回滚/停止各 30 秒与父预算/强制取消契约验证。
- [x] 无硬编码敏感信息：本阶段无凭据/外部连接配置新增。
- [x] 资源泄漏检查：清理集合/次数、观察协程退出已验；真实 SDK 释放未验证。
- [x] 测试覆盖：30 个顶层测试，Core 语句覆盖率 98.0%。
- [x] 交叉验证：源码、调用方、测试、文档/匹配 change 与真实输出核对。
- [x] 无编造内容：失败、未执行与真实依赖缺口分别记录；不承诺永不 OOM。
- [ ] 全仓门禁/发布准入：lint 格式阻断仍 open；完整门禁、真实组件和容量验收待后续阶段。

## 14. 阶段 2 实施记录

### 14.1 授权与基线

用户在阶段 1 汇报后明确“我已经提交，继续”。实际核对 `pluggable`、HEAD `7434bd5`，工作树干净；本轮仅继续阶段 2，阶段 3–4 未授权。Core 不改，依赖版本、真实配置键/值、业务接口和存储结构不改。匹配 change 仍为 `add-plugin-lifecycle-registry`，CLI artifacts 齐备；具体命令与结果在各步实际完成后填写，历史结果不冒充本轮结果。阶段前记录快照：/tmp/gin-api-stage2-c_7e0kat；代码回退基点为该已提交 HEAD，定向撤回本次差异，不执行 reset/clean。 用户随后再次明确只完成阶段 2 剩余验收与实施记录、重点并发/资源/兼容性，JSON 不增加生命周期插件且不迁移版本。实际环境 Go go1.27.2 darwin/amd64、golangci-lint 2.14.0；GOCACHE=/tmp/gin-api-gocache，GOLANGCI_LINT_CACHE=/tmp/gin-api-golangci-lint。

### 14.2 步骤 2.1 改动前细则与兼容清单（原计划，实际结果见 14.2.1）

- 原范围：schema 包 load.go（现 历史路径「internal/plugin/builtin/config/load.go」（已迁移或移除））、jwt_command.go 及加载/环境/文件安全/JWT/分发模板测试迁至已批准的 `plugin/builtin/config`；原混合 load_test 的 Validate/fixture 留 schema 包，避免内部测试反向导入默认解析器。所有原测试断言与案例保留，仅因包/位置改变同步类型限定和模板相对路径。
- 新契约：`plugin.ConfigurationProvider.Value() (*config.Config, error)`，只读已校验快照；未完成/失败返回可判别错误。默认包 `config.New(loader Loader) *Plugin`，`Loader` 为 `func(context.Context) (*config.Config, error)`，nil 使用既有 Load。构造/注册无 I/O；Init 使用 once 发布成功快照或失败结果，读写同步；Start 验证就绪，Stop 无资源工作；不创建 watch/goroutine，不热更新。自定义 Loader 结果仍要校验。
- 运行配置来源：保持 `config/env/env.yml` 与 `resource.ResolvePath`；进程 `APP_ROOT` 参与路径定位但不映射为配置键；裸 APP_ 忽略；APP_ 命名空间未知键继续拒绝；双下划线分层、单下划线保留，YAML 后叠加环境。WeaklyTypedInput、duration/text hook、mapstructure tag、ErrorUnused、原 Validate 全部保持。无新配置项，无 example 值修改。
- 安全：生产按最终 env 检查配置文件，保持拒绝符号链接和 group/other 可写权限；不读取真实配置文件或凭据作为重构材料。原内存读取辅助函数继续仅用于测试。
- JWT 轻量命令：保持 APP_ROOT/工作目录根，jwt.active 部分读取，不要求完整应用配置，不按可执行文件发现其他项目；APP_ 覆盖与生产文件安全保持；私钥/公钥路径必须明确非空、绝对化且不同。
- 调用清单：`cmd/{doctor,jwt,module}.go`、`bootstrap/{runtime,migrateruntime}.go`、四个 repository integration_test 的 Load/JWTLoad 引用切至默认包，配置 schema import 仍用于类型。`migrateruntime_test` 的源码说明断言同步真实引用，server/migrate 仍经原 Bootstrap 入口，尚不将配置插件接入整个 Runtime 注册图。
- 检查/停止：先跑旧 config/plugin/log/data/Core 定向 race 基线；完成迁移后跑 schema/builtin-config/cmd/bootstrap 定向 race（scaffold tag），新增未 Init/自定义 Loader/错误/取消/并发一次加载测试；核对 schema 生产依赖无 koanf、无 import cycle，并编译带 integration 标签的四个受影响包（不连真实数据库）。任何配置语义漂移、测试责任丢失或循环依赖立即停本步修复；不通过删除断言、放宽校验或改配置值换通过。2.2–2.7 仍按第 5–8 节原序执行，各组件实际耦合清单在对应改动前补入本节。

### 14.2.1 步骤 2.1 实际结果

- 已迁移默认加载器/JWT 轻量读取及其测试；混合 load_test 中 3 个加载测试迁至 builtin/config，Validate 测试与 validConfigForTest 保留 schema；example schema coverage 保留原包独立路径常量。生产 YAML/env/权限/未知键/配置值/默认值未改，无反向兼容 wrapper。
- 新增配置能力与一次初始化插件。并发测试采用明确 channel 屏障；首版 synctest.Wait 无法等待 Mutex/Once 竞争者达到持久阻塞，短超时实测堆栈后修复夹具，保留一次加载和完整发布断言。
- 基线 `GOCACHE=/tmp/gin-api-gocache go test -race -count=1 -timeout=5m ./internal/core ./internal/platform/config ./internal/plugin ./internal/pkg/log ./internal/platform/data` exit 1：前四包通过，data 既有 sqlmock Ping 测试失败（ERR-20261021-005），留 2.3；日志 `/tmp/gin-api-stage2-baseline.log`。
- `go test -race -tags=scaffold -count=1 -timeout=5m -coverprofile=/tmp/gin-api-stage2-config.cover ./internal/platform/config ./internal/plugin/builtin/config ./internal/bootstrap ./cmd`（GOCACHE 同上）exit 0；各包输出 2.526s / 4.231s / 7.699s / 11.645s；schema 88.6%、builtin-config 88.7%、bootstrap 53.3%、cmd 73.7%，只是本命令覆盖范围。Config 插件 New/Name/Init/Value/Start/Stop 明细均 100%。日志 `/tmp/gin-api-stage2-config-race.log`。
- `go test -tags=integration -run '^$'` 四个受影响 repository 包 exit 0（无测试执行），只证明带标签编译，不证明真实数据库；日志 `/tmp/gin-api-stage2-config-integration-compile.log`。schema 生产依赖列表 `/tmp/gin-api-stage2-config-deps.log` 未引入 koanf。
- 定向 lint（schema/builtin-config/cmd/bootstrap）首次 8 个 goimports 分组问题，定向 stdin formatter 修复后真实 exit 0、0 issues；缓存环境同阶段 1。日志 `/tmp/gin-api-stage2-config-lint.log`。
- gopls 默认 cache 受限时虽 exit 0 仍实际报 packages.Load 错误，不作为引用证据；显式 GOCACHE 后，默认 build 下查到 doctor/Runtime/MigrationRuntime/默认插件 Load；scaffold、integration 的调用由 rg/源码补齐，不能把默认 build 引用列表当完整调用图。

### 14.3 步骤 2.2 改动前细则与兼容清单（原计划，实际结果见 14.3.1）

- 将默认日志实例、路径/保留选项、notifier 构造移入 `builtin/logging`；`pkg/log` 只持有项目 LogEntry/Field/Level、Logger.Log、启动期安装/撤销及必要的旧式排空转发，不依赖 gtkit/zap/news。为避免旧插件包在逐步迁移时形成环，日志能力定义在门面，plugin 包以类型别名公开同一契约，不复制第二套接口。
- LogEntry 明确级别、message、结构化 Fields、main/access、channel、进程告警标记及 caller skip；不通过任意服务定位器获取能力。只实现已使用的 Info/Infof/Warn/Warnf/Error/Errorf/Debug/Debugf/ErrorCtx/WarnCtx/HErrorf 等兼容入口，字段限定已使用的 String/Int/Int32/Int64/Duration/Any/Err/NamedError/Stack；默认实现内部转换 zap，普通错误不得触发通知。
- 生产直接引用清单按本轮检索与第 7 节一致（33 文件）；HTTP ErrorLog 目前已经使用标准库 *log.Logger 类型和 zap.NewStdLogAt。保留 net/http 强制要求的原有桥接类型，由 Writer 将内部错误转给所选日志能力；不把标准 log 或 slog 用作新的日志实现，不新增此类日志插件。
- main 选项保持原 path/basepath、level、console/file、daily、maxsize/compress、条件透传 maxage/backups；alert queue 仍 64；access 路径 _access、buffered、WithReplaceGlobals(false) 等均保持。配置 Provider 在 Init 才读取；正式实例/访问实例部分创建失败由 owner 回收；工厂以具名函数注入测试。
- 门面安装使用实例归属标记与 CAS，禁止未卸载时替换；旧清理句柄不能删除后来安装的 logger。默认 backend 在关闭前停止接受新写入，等待已进入 Log 返回，再关闭 access/main，防 SDK 关闭后的迟到写入重新开文件。Stop/独立日志 Drain 共用一次关闭状态；配置未 Init、重复安装、部分失败、并发与多次关闭均测试。
- requestId 仍由中间件生成，项目 context key 与默认 SDK 适配对应；原字段名、主/访问流、动态 channel 与 HErrorf 告警语义分别验证。访问 body 前缀限制、脱敏与中间件行为不改。GORM 参数化/忽略 record-not-found、慢查询/SQL 级别及 trace_id 字段改为选定 Logger 的固定 adapter，不从 SDK Default().Zap() 取具体实例。
- 已核实 workspace logger v2.5 与锁定 v2.3 均仅有 void Sync；它会同步等异步告警、Sync 文件并关闭资源，错误写 stderr，无法通过返回值证明全部刷盘/关闭成功，也没有 context 硬取消。此上游限制按原计划 5.3/6.3 单列受限：不引入遗弃回调协程或假造关闭成功证明，不在本步擅改上游、版本或 go.work；若需要新增上游 API，须另列兼容清单并获对应授权。完整日志 I/O/硬时限验收不勾选通过。
- Bootstrap 只同步日志构造/能力与旧排空引用，整体选择/注册六插件及 App 信号预算仍留阶段 3。测试捕获桩改为所选日志能力或默认 backend 的测试工厂，保留原告警、脱敏、字段、状态码、HTTP/worker 行为断言，不以换 import 删除测试。
- 检查：门面替代 sink、实例归属/并发、默认 options/字段/stream/channel/告警/SQL 脱敏测试；受影响包完整 race 与定向 lint，必要编译；实际覆盖与上游限制记录后才进 2.3。

步骤 2.2 细化：printf 事件保存模板、参数与格式标记，默认后端在级别过滤后才格式化，避免关闭的 Debugf 路径比原实现多做字符串分配。主/访问默认实例工厂使用 Options 中两个具名函数；日志接口别名仍单一。旧 InitWithConfig/InitAccessLog 两步装配临时由 builtin/logging 的兼容函数承接，内部共用一个 owner；访问日志初始化合并为该 owner 的原子创建，普通门面不持有 SDK 构造。Router 文件日志夹具随该拥有者迁移，保留实际日志产物断言。
GORM adapter 的已核实基线：Warn 缺省、slow threshold 200ms、参数化查询始终开启、忽略 record-not-found 错误；Trace 字段 source/elapsed/sql/rows（rows>=0）/trace_id，错误附 error、慢查询附 slow_threshold，消息分别 gorm query error / gorm slow query / gorm query。

### 14.4 步骤 2.3 改动前细则与兼容清单（原计划，实际结果见 14.4.1）

- 文件：默认 MySQL 实现从 plugin/mysql.go 迁入 builtin/mysql/plugin.go；platform/data 的 open.go、mysqlversion.go 与对应单测迁入同包；GORM 日志/观测适配暂留 data（已不依赖默认日志 SDK）。生产 OpenDB 的 doctor、MigrationRuntime 及默认注册函数同步 import；迁移的具体 ormx client 返回契约仍按原计划留阶段 3 收敛。
- 能力：DatabaseProvider 只含 DB/SQLDB/PingContext，不包含 core.Plugin；消费端拿该能力，注册仍拿同一实例的 core.Plugin。data.DatabaseHandle 保留为其唯一结构定义，plugin 以别名公开，避免逐步拆分形成反向循环。
- 默认构造使用 Options.Configuration provider，在 Init 才 Value；SelectCredentials 为具名配置选择函数，nil 选运行凭据，既有 Bootstrap 将已解析凭据作为固定选择传入；Open 具名工厂仍返回默认实现私有边界内的 *ormx.Client。GORM logger 不再由注册期创建，而是 Init 读取完整配置后构造。
- MySQL 兼容清单：host/port/name、运行/迁移凭据、PrepareStmt/SkipDefaultTransaction/TranslateError；跳过 GORM 无 ctx 的版本探测，由 SELECT VERSION()/@@version_comment 校验 Oracle MySQL/Percona 与 minimum；UTC/parseTime/time_zone +00:00、charset/collation/strictSQLMode；四个池参数保持 Effective 默认值。服务读写 min(连接寿命,30s)，迁移读写 0；建立/显式校验保留调用 ctx，迁移不继承服务驱动 deadline。无 SQL/表/业务事务改动。
- 生命周期：Init/Stop 唯一 owner 串行化并缓存结果，Start 只校验就绪及 ctx；工厂即使同时返回资源与 error、返回资源后取消、nil 成功，都进入明确失败路径；已取得的资源只由该 owner 关闭一次，关闭错误与根因 errors.Join。普通查询不持 owner 锁执行 I/O；停止仍依赖固定 HTTP/worker 先退出，不伪造任意不合作驱动的硬中断。
- 已实际 rg 确认 NewResources 生产调用为零，只有两条参数校验测试。移除其旧双 owner 装配、namedCloser 和资源预算副本，使 Resources 永久为借用视图；Requirements 的 Redis 必需性值仍用于原 Bootstrap。已废弃的私有预算/逆序聚合/并发关闭测试责任映射到阶段 1 Registry 的 StartupBudgets、StopBudgetIncludesWaiting、PanicStillCleansAndPreservesErrors、ConcurrentStopSharesErrorAndAllowsWaitCancellation 等；各新组件另测真实 close 次数，视图仍测多次/并发 Close 不动被借用连接。不会将旧代码保留为测试专用第二实现。
- 两条旧构造入参测试替代为默认 MySQL 插件缺失/失败配置 provider 与无 opener 调用断言；资源视图保留 nil/无句柄拒绝与全部存量访问/追踪场景。sqlmock Ping 夹具改为 MonitorPingsOption(true)，显式设置 Ping 失败/成功预期并核对 ExpectationsWereMet；这修复 ERR-005，而非删除失败断言。原格式问题在该已获准文件一并按 formatter 修复。
- 资源视图初始化不建连，不 Close；运行期 availability/trace/metrics 维持原行为。Redis opener 此步暂留 data，2.4 迁出；移走共享 closeAndWrap 后 Redis 默认函数在自己的文件保留同义清理 helper，2.4 再收敛；增强 errors.Is 同时保留关闭错误，不丢原错误。
- 验收：默认 MySQL 的公开生命周期/配置读取/凭据与 factory 参数、nil/部分资源/error/迟到取消/重复 Init/并发 Stop、sqlmock.ExpectClose 恰一次；版本与会话检查失败的真实 sqlmock 查询/关闭；data 借用视图与 tracing 全量 race；bootstrap/cmd 迁移和 doctor 定向回归及 lint。真实 MySQL、发布依赖与进程信号留原阶段，不把 mock 连接当实库。任何会话/DSN/owner/错误链漂移先停本步修复，不扩展仓储或 SQL 方言。


### 14.3.1 步骤 2.2 实际结果

- 默认构造/notifier 与 SDK 仅在 builtin/logging；项目门面/能力不依赖 gtkit/zap/news。33 个原生产 SDK 引用已迁，SQL 固定 adapter 脱离 Default().Zap()，HTTP 标准库类型桥接保留 net/http 约束。日志主/访问的选项、字段、channel、request ID、printf 和只有显式 HErrorf 触发告警均有对应测试。
- 关闭 admission 与 WaitGroup 保证不在 SDK Sync 之后继续写；Stop/Drain 同一 owner。新增迟到取消反证先实际 exit 1（仍可 Start），修复后纳入完整 race。`/tmp/gin-api-stage2-log-latecancel.log` 保留旧失败，不冒充最终失败。
- `go test -race -tags=scaffold -count=1 -timeout=5m -coverprofile=/tmp/gin-api-stage2-logging.cover`（GOCACHE 同 14.1）针对 pkg/log、builtin/logging、middleware、router、bootstrap、pkg/upload、cmd 最终 exit 0；覆盖率分别 92.7/92.8/83.8/96.1/53.3/96.8/73.7%，日志 `/tmp/gin-api-stage2-logging-race.log`。另 `go test -race -run TestGormLogger -count=1 -timeout=60s ./internal/platform/data` exit 0，日志 `/tmp/gin-api-stage2-sql-logger.log`；不把它当 data 全量通过。
- 定向 lint 同七包最终 exit 0、0 issues（`/tmp/gin-api-stage2-logging-lint.log`）；未改 linter 配置。contextcheck 官方源码的函数声明注释作为无 ctx 进程诊断 API 分析边界，该单函数使用定点 contextcheck/nolintlint 注释说明分析器已消费但无本地 issue；请求日志仍透传 ctx，不给调用方批量加豁免。首次创建固定 Background 变量未解决跨函数事实传播，实际失败如实保留在 ERR-011。
- 上游 void Sync 的错误不可见、不可中断 I/O 仍受限；本步未修改或升级上游，也未宣称真实刷盘/强制停止满足硬时限。旧 Bootstrap 的预算排空 goroutine 留阶段 3 处理，本步插件内部没有新增遗弃协程。

2.3 工厂边界核对补充：已读取 ormx/client.go，OpenWithDB 创建的 Client 不拥有传入 sql.DB，Close 只清自身预编译缓存；不能用它加 ExpectClose 假称插件关闭了 SQL 池。默认 MySQL 工厂因此改为返回具名 Client（DatabaseProvider + Close）的实现内部所有权契约；默认工厂仍适配 OpenDB 的 *ormx.Client，调用方能力不暴露 Client。测试用拥有 sqlmock 池的 adapter 观察真实 Close；迁移现有工厂仅返回签名适配为 Client，仍保留其阶段 3 前的具体迁移契约，不改变真实 ormx Open 的 owner。

### 14.5 步骤 2.4 改动前细则与兼容清单（原计划，实际结果见 14.5.1）

- 范围：plugin/redis.go 的默认实现、data/redis.go opener/probe/options、redis_probe_test 迁入 builtin/redis；doctor 使用同一默认 probe；plugin.RedisProvider 仅组合逻辑库/Configured/健康/池统计能力，去掉生命周期、具体 AvailabilityTracker 与重复 Ping。已有 RedisClientProvider 保留给缓存，只暴露其实际需要的窄能力，不扩展 Cluster/UniversalClient 范围。
- 构造 Options 只接 Configuration provider、Required 配置推导函数和具名 Opener，Init 才读取配置/推导 required/建连。默认工厂返回仅实现内部使用的 Client（GetClient/DefaultClient/HealthCheck/PoolStats/Close），适配 redisx。构造/注册不建连、不读配置；不产生 watch/重连 worker。
- 耦合清单：addr/password/DBs 原样传递，不传 KeyPrefix；只在 PoolSize/MinIdleConns/DialTimeoutMS/ReadTimeoutMS/WriteTimeoutMS > 0 时透传，留零沿用 redisx 默认，不复制或修改默认重试；NewClientContext 后 HealthCheck 都传入口 ctx。已用的 DefaultClient 和各 DB 句柄仍为 *redis.Client，不改变现有 repo/限流/worker 客户端形状、TTL、key 或逻辑库。
- 未配置 optional 成功但不可用；未配置 required 拒绝。已配置不可达 optional 持续降级至重启；required 返回根因。任何调用 ctx 取消/截止或工厂返回可 errors.Is 的取消/截止均失败，不被 optional 吞掉；nil 成功是契约错误。部分 client + error 先关闭一次；关闭失败必须与根因一起返回，不能将“未能回收”报告为降级成功。迟到取消先回收再返回。
- Init/Stop 由同一 lifecycle owner 串行，结果缓存；访问字段使用短锁，不持字段锁发网络请求。Stop 只关自己的 client，不调用借用方关闭。停止 ctx 的限制如实记录：redisx Close 无 ctx，不能据 mock 承诺任意网络/SDK 的硬终止。
- ResourcesFromClients 去掉 tracker 参数；在借用视图中根据实际已初始化 DB 集合生成 availability，未建立连接使用永久降级，Configured 单独保留；Runtime 只同步此必要引用。旧插件相关 availability/Ping 测试责任迁入视图测试，替代 Redis 无需实现具体 tracker。现有 tracing/health/metrics/限流消费保持原调用语义。
- 验收：保留 miniredis probe/auth 与默认插件生命周期场景；新增 fake owning Client 关闭计数、同时资源/error、cancel/deadline、nil/typed-nil、配置读取时点、并发重复 Stop。miniredis 只证明本地协议模拟；真实 Redis 和进程端到端留原阶段。data/Redis/default registration/doctor/bootstrap 定向全量 race/lint，遇到键/库/降级语义漂移先停本步。


### 14.4.1 步骤 2.3 实际结果

- MySQL 默认实现/opener/vendor 解析与测试已独立，配置在 Init 读取；DatabaseProvider 无生命周期。原 NewResources 生产调用确认为零后移除双 owner/私有预算/closers；旧预算、逆序和并发关闭责任由已完成的 Core 契约承担，data 保留视图/观测适配和不关闭借用资源的并发测试。
- sqlmock 夹具对拥有连接的测试使用明确 owned adapter，验证正常/部分返回/迟到取消/并发 Stop 的实际池 Close 一次及 errors.Is 根因/关闭错误；版本、vendor、会话查询和关闭故障亦实际走 GORM+sqlmock。OpenWithDB 借用语义已从 SDK 源码确认，未伪装其 Close 会关闭外部 SQL 池。
- `go test -race -tags=scaffold -count=1 -timeout=5m -coverprofile=/tmp/gin-api-stage2-mysql.cover ./internal/plugin/builtin/mysql ./internal/platform/data ./internal/bootstrap ./cmd`（GOCACHE 同上）exit 0；输出 2.482s / 2.861s / 11.926s / 16.441s，覆盖率 89.2/77.5/53.0/73.7%；日志 `/tmp/gin-api-stage2-mysql-race.log`。data 原 Ping 夹具现实际监测 Ping 并显式失败预期；ERR-005 的同一用例通过。
- 同四包定向 lint exit 0、0 issues，`/tmp/gin-api-stage2-mysql-lint.log`；原 resources_test 的格式阻断已在范围内格式化，完整全仓 lint 仍待阶段验收，不冒充已经通过。
- 没有连接真实 MySQL，不证明真实驱动的任意关闭硬时限/池容量或生产性能；会话/DSN 与服务/迁移 deadline 的选项及故障用例通过，运行/迁移凭据默认与既有 Bootstrap 选择保留。

### 14.5.1 步骤 2.4 实际结果

- 默认 Redis/opener/probe 已独立，接口不要求生命周期或具体 availability tracker；视图自行维护 permanent degraded / recovery 状态。逻辑库/地址/密码选项、KeyPrefix 不传、正值才透传池/timeout、required 推导与 optional 重启前持续降级规则保持。
- 新反证先复现 optional partial 清理后误置终态（exit 1），随后修复；同一表覆盖 required、optional、实际拥有的两逻辑库客户端关闭计数、nil/typed-nil、工厂取消/截止、迟到取消、关闭失败、重复 Init 和 16 个并发 Stop。部分 client 关闭失败不会被降级吞掉。
- `go test -race -tags=scaffold -count=1 -timeout=5m -coverprofile=/tmp/gin-api-stage2-redis.cover ./internal/plugin/builtin/redis ./internal/platform/data ./internal/bootstrap ./cmd`（GOCACHE 同上）exit 0；3.904s / 5.795s / 7.064s / 6.973s，覆盖率 75.2/87.8/52.9/73.7%；`/tmp/gin-api-stage2-redis-race.log`。同四包 lint exit 0、0 issues，`/tmp/gin-api-stage2-redis-lint.log`。
- 原定向命令在沙箱中 exit 0，但 probe 在 RequireLoopback 环境检查处 skip，不能写成成功/认证拒绝已执行。收尾阶段已开放回环并实际执行两案例，零 skip 结果见 14.8.1；仍是本地协议模拟，未连接真实 Redis。SDK Close 不接 ctx，硬 I/O 中断与部署容量未验证，未引入热重连或新 key/TTL。

### 14.6 步骤 2.5 改动前细则与兼容清单（原计划，实际结果见 14.6.1）

- 将旧 plugin/cache.go 默认实现迁入 builtin/cache；ResponseCache 仅 Enabled/Middleware，不包含生命周期/Stager。httpcache.Stager 作为默认实现使用的固定工具保留 pkg/httpcache，消费端 providerSet/infra/App/Runtime 均改用能力，不获取/关闭 Stager。
- Options.Configuration 在 Init 读取；Redis 是借用的 RedisClientProvider；Factory 在默认包内返回 Store（ResponseCache + Close），nil 使用现有 httpcache.New。构造无 I/O；memory/disabled 不调用 Redis provider，redis 后端只取 cache.redis_db。配置原值不变：禁用/optional 降级仅复制局部 CacheConfig.Enabled=false，不改共享快照。
- 兼容清单：enabled/store/redis_db、EffectiveTTL（未配 30s，上限校验仍 10m）、EffectiveMaxBodyBytes（未配 1MiB）、memory MaxBytes 正值有界；ristretto NumCounters=MaxBytes/100、MaxCost=MaxBytes、BufferItems=64、owned/wait；Redis prefix 为 appName+:httpcache:、read 1s/write 2s，不 Close 借用 client。无新容量参数、未凭空设置部署内存限额，不承诺超大部署配置/任意 SDK 永不 OOM。
- 保留请求准入 GET/Authorization/App-Token/Origin/Cookie/Range、仅 200、排序 query key、响应 Set-Cookie/Cache-Control/Vary/跳跃头防护与审计、MaxBodyBytes；仅显式路由挂载，不改变业务缓存/失效策略，不把普通泛型 cache 工具插件化。
- 生命周期缓存 Init/Stop 结果，Stop 清掉 capability 后只关本地 owned Store 一次；工厂部分 Store/error、nil/typed-nil/迟到取消清理，关闭错误可判别；配置 provider 不可用不调工厂。中间件在 Serve 前构建，借用方不拥有关闭权，原固定 HTTP/worker 关停纪律保持。
- 验收：迁移原插件生命周期/降级/nil/透传场景；Fake owned Store 关闭计数与并发，memory 不访问 Redis，Redis cache Stop 后借用 Redis Ping 仍可用；两个后端实际 ServeHTTP、TTL/前缀/库与存量 httpcache 全量安全案例。临时 Bootstrap 默认构造仅必要引用同步，完整选择/注册留阶段 3；实际失败先修复，不删除安全断言。

### 14.6.1 步骤 2.5 实际结果

- ResponseCache 只暴露 Enabled/Middleware；默认插件独立、Stager 仅在工具/默认实现及其直接测试中使用。providerSet/infra/App/Runtime 消费能力，借用者无 Close。内存/禁用态测试验证不会访问 Redis；共享配置未被降级逻辑改写。
- `go test -race -tags=scaffold -count=1 -timeout=5m -coverprofile=/tmp/gin-api-stage2-cache.cover ./internal/plugin/builtin/cache ./internal/pkg/httpcache ./internal/bootstrap ./cmd`（GOCACHE 同上）exit 0，3.561s / 2.054s / 6.417s / 5.306s；覆盖率 95.6/95.3/52.9/73.7%；`/tmp/gin-api-stage2-cache-race.log`。同四包 lint exit 0、0 issues，`/tmp/gin-api-stage2-cache-lint.log`。
- 原定向命令在沙箱中 exit 0，miniredis 场景实际在 RequireLoopback 处 skip；此处更正先前误报。收尾阶段开放回环后实际 ServeHTTP + miniredis 验证 DB 3、contract:httpcache: 前缀、路由 TTL 3s、匿名命中、App-Token 绕过，Stop 后借用 Redis Ping 成功，最终零 skip 见 14.8.1。原 httpcache 请求/响应准入与 owning fake 的正常/部分返回/取消/nil/关闭失败/并发 Stop 通过；不作为真实 Redis 或部署容量证据。

### 14.7 步骤 2.6 改动前细则与兼容清单（原计划，实际结果见 14.7.1）

- 新增 plugin.MiddlewareBuilder 与 MiddlewareDeps（具名 Metrics、TracingEnabled、ServiceName），返回有序 Gin 链，不嵌生命周期；builtin/httpmiddleware 读取 Configuration provider，Init 仅保存 CORS origins 的复制、访问 body 两开关、App.MaxBodyBytes，不建正式仪表、不挂路由。Start 校验 ready/ctx；Stop 无借用资源关闭，只终止后续 Build。
- Build 必须在 Init/Start 后、最终 Metrics recorder 创建后执行；默认实现拒绝 nil Recorder，禁用导出也由组合方显式提供已经构造的 no-op recorder。Build 只允许一次，包括失败尝试；第二次返回可判别 ErrMiddlewareAlreadyBuilt 并保留首错。RequestIDFactory 为默认实现内部具名测试缝，nil 使用现有 NewRequestIDGenerator；空生成器/构造错误失败，不静默忽略安全能力。
- 原 useGlobalMiddleware 固定链迁入默认实现，链序原样：外 Recovery→RequestID→可选 otelgin tracing→GinLogger→Metrics→内 Recovery→SecurityHeaders→ErrorHandler→可选 CORS→MaxBytesLimit。原 request ID 128-bit 随机前缀/64-bit 计数、48 hex 长度、强制替换入站 ID、body 前缀限制/脱敏、413/404/405/OPTIONS/业务 panic/基础中间件 panic 语义均保持。trusted proxies、RemoteIPHeaders、gin mode、404/405/尾斜线设置与各路由 auth/rate limit/cache 仍由原位置负责。
- Router.New 只消费 MiddlewareBuilder，向 Build 传最终观测依赖并安装返回链；缺能力、nil/空链、nil handler 或已有全局链均返回装配错误，不保留默认实现分支。移出 Router.Options 的 CORS/body 配置选择字段，它们由默认插件配置快照提供；Metrics/Tracing/ServiceName 作为 Build 参数保留，普通业务路由不插件化。
- 必要引用同步：旧默认 registrar 追加该默认实例注册，并把能力放 PluginSet/Runtime/infra，再由 buildEngine 传给 Router。仍沿用现有 eager config/log bridge，完整六插件 main 显式选择和进程停止预算接线属于阶段 3，不在本步实现。此处只增加组件已获准的必要生命周期接线，构造仍无 I/O。
- 测试迁移：router 原 New 调用由测试辅助函数显式注册/启动默认 middleware、提供最终 recorder，再调用真实 Router.New；CORS 选项用明确配置 provider，保留所有原响应/字段/指标断言。测试辅助 owner 在 Build 后 Stop（此插件无资源，已返回链仍引用不可变配置/生成器）。Bootstrap assembly 夹具显式提供已启动 builder 与 recorder；第二次测试缺失 route policy 时使用新的 builder，避免重复 Build 错误掩盖原策略错误。
- 验收：公开方法、配置读取时点、迟到取消、nil/失败 provider、Init/Start 前 Build、缺 recorder、生成器错误/nil、并发 Build 仅一次、重复安装拒绝；默认 tracing/CORS 组合精确链序与旧 router/middleware 完整请求观测回归；替代 builder 不依赖默认实现、Build 错误原样上抛。实际结果逐项记录后进入 2.7，不调整业务/HTTP 超时或新增通用优先级框架。

### 14.7.1 步骤 2.6 实际结果

- 中间件能力只暴露 Build；默认实现构造不读配置/不建仪表，Init 复制配置，Start 后且最终 recorder 已创建才 Build，一次尝试含失败缓存。停止不关闭借用仪表，已返回链使用不可变配置。Router 不再自行选择默认链；空/nil/重复/超过 Gin 全局链容量在修改 engine 前拒绝。
- Runtime→infra→Router 仅同步本步能力；Bootstrap 的默认实现选择、eager config/log bridge 和完整入口/停止接线留阶段 3。未调整任何业务路由、鉴权、限流、缓存挂载或 HTTP 超时。原全局链顺序、CORS、请求体上限、request ID、防护和观测异常语义由原请求测试保留核对。
- 默认组合全部语句和公开方法 100%；并发 32 次 Build 仅一次成功、factory 仅一次，其他调用返回可判别重复错误。原 Router/middleware 的 OPTIONS/413/404/405、业务 panic、观测链 panic、主/访问日志和 metrics 断言通过；替代 builder 与 Build 根因传播、原子拒绝非法链通过。
- 五包定向 race（builtin/httpmiddleware、router、middleware、bootstrap、cmd）首次最终 exit 0；覆盖率 100.0/97.2/83.8/53.0/73.7%，日志 /tmp/gin-api-stage2-middleware-race.log。这是当时受限环境包级结果，包含需回环的既有测试可能 skip；最终开放回环的全量受影响包零 skip 证据以 14.8.1 为准，不把历史百分比冒充全部案例执行。
- 定向 lint 最终 exit 0、0 issues；unused 字段和删字段后的格式失败均记录 ERR-015，未改传感器/linter 配置，未增加第七类扩展点。

### 14.8 步骤 2.7 改动前验收细则（原计划，最终结果见 14.8.1）

- 六类替换：在 plugin 的外部测试包中分别实现配置、日志、数据库、Redis、缓存、中间件替身，逐一满足 consumer interface 和唯一 core.Plugin；手工注册到同一个未修改 Core，验证注册无副作用、排序、实际门面日志、借用视图探活、Router 请求和显式缓存挂载、逆序一次 Stop。替身不使用任何 builtin、Stager、ormx/redisx/logger SDK；离线句柄与 fake 结果不冒充真实数据库。
- 回审：整段重读新的各插件 Init/Start/Stop、默认 opener、日志门面/后端、SQL adapter、consumer 与必要 Bootstrap 改动；gopls 对准确接口/声明做引用补核，rg 补构建标签/函数值。Core、go.mod/go.sum/go.work、真实配置、业务模块层级和上游内容源必须无本轮变更；保留仓储业务实现，只变必要加载器 import。
- 两个范围内边界补强：日志 Start 只认已完整发布 ready，access 路径直接选已缓存的 access logger，避免先克隆 main 再丢弃；行为由原全部日志/访问/告警回归核对，不宣称未经测量的性能收益。Router 根据已核实 Gin v1.12 abortIndex=63 的框架限制，在安装前拒绝无法容纳一个 endpoint 的全局链（>61），并保留空/nil/重复安装原子检查；业务路由仍需遵守 Gin 合并链限制，不新增配置参数或通用排序规则。
- 2.7 覆盖明细补核（改动前）：实际 go tool cover 显示 logging/options.go 的 InitWithConfig、News.Send/SendTo 为 0%，不能以工厂替身代替真实兼容入口。仅在既有 builtin/logging/options_test.go 补直接调用兼容入口的主/访问落盘、默认 SDK 恢复、缺输出失败与再次安装，以及回环 httptest 验证默认/显式告警目标和正文；不联系外部通知端点，不改生产实现或依赖。完成后重跑日志相关完整 race/lint 与公开方法明细；新测试失败只在本步修复，若需上游 API/范围扩展则停止。用户已明确 JSON 保持当前依赖/版本，不增加生命周期插件；此验收不改 JSON 代码。
- 实际受影响包由本次 git diff 与 untracked Go 文件目录汇总，保存清单；对这些包和 Core 运行含 scaffold 的完整 race/shuffle/覆盖率，按 go tool cover 明细核对所有新公开方法非零。执行正常全仓 build、vet 和 lint、现有架构传感器及 go fix -diff（只检查、差异须在本阶段范围内定向处置）；不运行阶段 4 的 make check/全仓覆盖率棘轮/真实依赖/发布双环境整套验收。
- 2.7 执行环境补核（改动前）：回环 HTTP 首次实际因 bind operation not permitted 失败；对会 skip 的 miniredis 不能只凭包 exit 0 判为运行通过。使用仅本地回环端口的测试权限重新执行相关完整 race（含 verbose/JSON 事件核对 skip），不使用真实 MySQL/Redis 服务。MySQL OpenDB 默认入口的 0% 缺口在现有 open_test.go 增加非法 min_version 先于拨号失败的公开入口反证（凭据/地址留空，不发网络），迁移预算原用例保留；不为覆盖率连接任何未授权数据库。
- 2.7 并发反证补充（改动前）：complete-test.jsonl 的随机复验实测 time.Local 写与先前 TLS conn 的 time.Now 读取 race（ERR-019）。仅修改 internal/bootstrap/app_test.go 的该时区用例，在当前测试二进制的有界子进程执行同一真实 initProcessRuntime 与时区断言；主进程不再写 time.Local，所有其他断言保留。子进程退出码与用例实际 PASS 标记均校验；失败回显完整输出，成功只输出固定诊断，避免子进程 coverage 文本被 go test 误取为整个包的覆盖率（已观察到摘要 1.4% 而 profile 实值 68.9%）。使用项目现有 runtime，不修改生产时区/HTTP/关停、不进入阶段 3。先保存这次失败，再在完整 Bootstrap race 中固定复现本次 shuffle seed 1792579847892336000（实际 JSON），并以真实捕获 seed 为准；另完整 28 包 race 随机复验。汇总全部由 JSON 实际计数生成，不写死成功数字。
- 依赖边界以生产 go list 的直接 import 和 Core/门面闭包证明，无 import cycle；root capability 不直接依赖 builtin/默认 gtkit SDK；schema 不直接依赖 koanf；门面不依赖默认 SDK/zap/news。保留其他正常工具用途，不误报整仓不再依赖 ormx paginator 等事实。
- 回读并补齐本阶段新错误记录，按实际修复/复验关闭；匹配 change 仅更新阶段 2 任务并严格验证，不归档尚有阶段 3–4 的 change。记录日志 Sync 及 SDK Close 的硬终止限制、真实依赖/容量/完整入口与进程信号未验证项。最终只给阶段 2 定向结论，完整项目仍不准入，不提交/push/发布、不进入阶段 3。

### 14.8.1 步骤 2.7 最终验收结果（2026-10-21，Asia/Taipei）

本阶段 2.1–2.7 已完成并按实际可执行范围验收；日志底层关闭结果/硬终止、完整生产入口及真实服务/容量均未作通过结论。验收期间不改 Core、go.mod/go.sum/go.work、配置键值、数据库结构或上游 gtkit 内容源。Go 生产代码最后两处仅纠正已迁移函数的旧注释；之后冻结当前 Go 文件，执行前后与解析覆盖率时的 SHA256 均一致（/tmp/gin-api-stage2-final-source-sha256.json）。

#### 范围与可复核命令

- 分支 pluggable，基点 7434bd5；本阶段 Go 路径共 119 个（含迁移前删除路径），加未变更 Core 共 28 个受影响/契约包。清单由实际 git diff 和 untracked Go 文件生成，未变更仓储业务实现，四组 integration 文件只改默认 loader import。
- Go test 真实 argv 前缀：`go test -p=4 -race -tags=scaffold -shuffle=1792579847892336000 -count=1 -timeout=5m -json -coverprofile=/tmp/gin-api-stage2-verified.cover`，后接下列 28 包；由 Python subprocess.run 逐项传参、直接传播退出码。该 seed 来自真实 race 失败，修复后另有完整 `-shuffle=on` exit 0 复验，旧失败均保留。保留原行为断言，未以放宽/删除安全或失败断言换通过，也未使用 ALLOW_NO_LOOPBACK 掩盖缺口。

```text
./cmd
./internal/bootstrap
./internal/core
./internal/middleware
./internal/module/ai/transport/http
./internal/pkg/auditlog
./internal/pkg/breaker
./internal/pkg/httpcache
./internal/pkg/log
./internal/pkg/news
./internal/pkg/response
./internal/pkg/upload
./internal/platform/config
./internal/platform/data
./internal/plugin
./internal/plugin/builtin/cache
./internal/plugin/builtin/config
./internal/plugin/builtin/httpmiddleware
./internal/plugin/builtin/logging
./internal/plugin/builtin/mysql
./internal/plugin/builtin/redis
./internal/repository/aiconversation
./internal/repository/dbtx
./internal/repository/useraccount
./internal/repository/userrole
./internal/router
./internal/runtime/module/ai
./internal/runtime/module/auth
```

| 实际检查 | 退出码与结果 | 证据 |
|---|---|---|
| 上述 28 包完整 race/受影响覆盖率 | 0；包 pass=28，测试/子用例 pass=1501，fail=0，skip=0 | /tmp/gin-api-stage2-verified-test.jsonl；verified-summary.log；verified.cover |
| 固定原失败 seed 的 Bootstrap 完整 race | 0，8.531s；新测试隔离后同一 seed 成功，最终 28 包亦成功 | /tmp/gin-api-stage2-timezone-repro.log |
| `go tool cover -func=/tmp/gin-api-stage2-verified.cover` 与方法明细断言 | 0；六类默认实现/门面/SQL adapter 的新公开函数均非零，无全分支/真实服务承诺 | /tmp/gin-api-stage2-verified-functions.log |
| `go build -p=4 ./...` | 0；一次 module stat cache 写权限警告仍在，不是编译失败，也不宣称静默成功 | /tmp/gin-api-stage2-build-verified.log |
| `go vet -p=4 -tags=scaffold ./...` | 0，无输出 | /tmp/gin-api-stage2-vet-verified.log |
| `golangci-lint run ./...` | 0，0 issues；仅已有 exclusion 未命中的提示，未改配置 | /tmp/gin-api-stage2-lint-verified.log |
| `GOCACHE=/tmp/gin-api-gocache bash scripts/check-architecture.sh` | 0，architecture check OK；首次未给 cache 的失败保留 | /tmp/gin-api-stage2-architecture-final.log |
| `go fix -diff ./...` | 0，无差异 | /tmp/gin-api-stage2-modern-verified.log |
| 生产 go list 直接依赖、Core/门面非标准库闭包 | 0；无 import cycle，Core/门面闭包仅自身，root capability 无 builtin/默认 SDK，schema 无 koanf | /tmp/gin-api-stage2-imports.json；core-facade-deps.log |
| gopls 引用 + rg/完整函数回审 | 两能力真实引用列表，无工作区加载错误；补核构建标签/函数值 | /tmp/gin-api-stage2-middleware-references.log；cache-references.log |
| 四个仓储包 integration 标签编译（配置迁移步骤） | 0，均 `[no tests to run]`；只证明编译，未执行真实数据库用例 | /tmp/gin-api-stage2-config-integration-compile.log |
| 匹配 change 严格验证 | 0，valid=true，issues=[]；只勾阶段 2，不归档 | /tmp/gin-api-stage2-openspec-validation.log |

所有 Go 命令使用 14.1 的可写 GOCACHE，lint 另指定其 cache。完整测试的回环监听已通过工具权限审核；只使用临时 httptest/miniredis，没有连接现存真实 MySQL/Redis 或外部通知服务。测试结果按 JSON 真实 Action 统计，不将子用例数冒充顶层测试数。

#### 默认实现与关键契约

| 能力/包 | 最终语句覆盖率 | 关键证据 |
|---|---:|---|
| builtin/config | 88.7% | 构造零 I/O、Init 一次完整发布、错误/取消缓存、Value 未就绪错误；原 YAML/env/安全/JWT 行为 |
| builtin/logging | 96.7% | 主/访问文件、channel/字段/关联/告警；部分失败/迟到取消、在途写入等待、旧 release 不清新 owner；真实 InitWithConfig/Send/SendTo 均 100% |
| builtin/mysql | 90.3% | sqlmock 验证版本/会话、部分 client/error/迟到取消/Close 失败；owning adapter 的实际 SQL 池 Close 一次、迁移 deadline 选项 |
| builtin/redis | 88.4% | 两逻辑库离线客户端 Close 次数、required/optional/取消；miniredis 成功/认证拒绝实际运行，默认 Probe/Open 均已执行 |
| builtin/cache | 96.7% | memory 不访问 Redis，默认 store owned；本地 HTTP 验证 key/DB 3/TTL 3s/身份绕过，Stop 后借用 Redis Ping 成功 |
| builtin/httpmiddleware | 100.0% | 32 个并发 Build 一次、首错/重复可判别、最终 recorder、精确链序；原 Router 异常/防护/观测请求回归 |

Core 98.0%、日志门面 92.7%、data 92.0%、httpcache 95.3%、Router 97.2%、middleware 86.5%、Bootstrap 68.9%、cmd 73.7%。Bootstrap 时区断言独立子进程执行，父进程 profile 不把该子进程的所有计数纳入；摘要和当前 profile 已核对，不采用曾误取的 1.4%。三个仅 integration 的仓储包本轮普通标签无可执行用例，0% 不伪装为真实仓储验收。

- `internal/plugin/plugin_test.go::TestSixCapabilitiesCanBeReplacedWithoutCoreChanges` 以六种独立替身在同一未修改 Core 手动注册，证明注册无副作用、依赖顺序、选定日志门面、借用视图探活、真实 Router 请求/显式缓存挂载、逆序一次 Stop。替身无 builtin、Stager、默认 gtkit SDK；离线健康结果没有冒充真实数据库。
- 日志 admission/在途写入与排空同一 owner；配置为只读快照契约；HTTP 组合只启动期 Build。MySQL/Redis/cache 生命周期互斥、状态发布短锁，部分资源和错误同时返回仍回收，关闭根因保留 errors.Is，borrower 无 Close 权。并发证据来自执行到的契约和 race，不声称所有调度/所有 SDK 行为均已证明。
- 本阶段新增/收窄六类组件能力接口并同步必要消费端；没有新增 SDK 依赖，业务接口/DTO、数据与存储结构未变。日志字段迁移和 SQL 参数过滤、请求/响应缓存准入、CORS/鉴权/限流相对位置、凭据/会话与 Redis 库/key/TTL 由原断言、本地回环和源码分别核对；真实 MySQL/Redis 结果仍未验证。未新增 HTTP 默认链回落、无类型 locator、热重连或动态加载。
- 错误记录 ERR-20261021-005–020 已逐项补齐；原 004 与本阶段条目均依据实际修复/复验关闭。保留 synctest 夹具超时、gopls/cache/格式/unused、迟到取消/降级反证、回环 skip 误报、时区 race、覆盖率行号失配及其原因，不将这些旧失败改为成功日志。

#### 后端验收边界与停止点

- 需求来源为用户确认的本计划和本轮“仅完成阶段 2”指令。关键不变量是能力替换不改 Core、注册无资源副作用、资源唯一 owner、借用不关闭、失败/取消回收、响应/数据兼容；本阶段可执行契约和本地请求通过。
- 本期无业务 handler/DTO、事务边界、幂等规则、授权策略、SQL 查询/索引、迁移 schema 或存储格式改动，不新增业务约束；不能据本轮组件测试声称已完成全业务数据一致性、跨实例幂等/授权或 EXPLAIN 验收。
- 日志 SDK Sync 无 error/context，MySQL/Redis/缓存底层 Close 无可强制取消契约；不能证明任意 I/O 下 30 秒硬终止，也不能把 void Sync 当全部刷盘成功。Core 的回滚/停止总预算各 30 秒及等待契约保持；旧 Bootstrap 日志预算 goroutine、入口 WithoutCancel/强制信号和存活 worker 保护的统一接线仍留阶段 3，未在此提前改动。
- 容量方面保留原连接池/缓存 MaxBytes/响应 MaxBodyBytes、日志告警有界队列；未新建后台轮询或无界任务机制。没有真实 QPS、p95/p99、堆峰值、FD/连接长稳或部署内存预算，未运行生产负载；不宣称高性能收益、无泄漏或永不 OOM。函数/语句覆盖率不代表所有分支已穷尽。
- `GOWORK=off`、完整 make check/全仓覆盖率棘轮、真实 MySQL/Redis、进程信号端到端、发布/回退演练未执行，原阶段 4 任务保持未勾。JSON 当前依赖及版本不变，不增加生命周期插件；无 MQ、std plugin/.so、依赖升级、上游修改、commit/push/tag/release。
- 结论：**阶段 2 实现与定向验收完成，已保留 SDK/环境/发布限制；完整项目不准入。停在阶段 3 之前。** 后续只有用户明确授权才进入阶段 3，继续继承这些未验证项。

## 15. 阶段 3 改动前方案与实施记录

### 15.1 授权、现状与保护

用户要求“继续按计划执行阶段 3……完成验收和实施记录后……进入下一阶段……直到所有计划都完成”，随后继续；已明确允许默认组合静态 cache→redis。沿用 0–4 编号及原 3.1–3.5/4.1–4.4 顺序，不重新拆阶段。JSON、业务路由/接口、凭据/TTL/逻辑库/配置默认值、依赖版本及上游保持原边界。阶段 2 已记录的 SDK void Sync/非 context Close 限制继续受限，不能伪造硬 I/O 中断。

实际分支 pluggable，阶段 2 差异仍在工作树；阶段 3 前快照：/tmp/gin-api-stage3-lgools3w。保护已有改动，回退仅恢复本阶段涉及文件到该快照（不 reset/clean、不恢复成阶段 1）。现有事实：Bootstrap 仍 eager 读取配置/日志、选择默认组件、丢弃停止 ctx；MigrationRuntime 暴露 ormx；旧日志预算使用可遗弃 goroutine；providers 回滚闭包可能使用已取消的启动 ctx。阶段 3 将修这些接线，不改变四方法 Core。

### 15.2 原方案与兼容清单（实际验收见 15.5）

1. **入口选择和能力**：Bootstrap.PluginRegistrar 改为只接 registry，返回具名 PluginSet（Configuration、Logging、Database、Redis、Cache、Middleware，以及仅生命周期管理用的 DrainLogs 回调）。所有构造/注册零 I/O。cmd 新增 plugins.go 的 server registrar 手动注册 config→logging→mysql/redis→cache/http-middleware；日志与配置边显式声明。Bootstrap 删除 RegisterDefaultPlugins、默认 Load/SDK import/默认回落；缺 registrar/能力、nil/typed-nil 与配置 Value 失败均报装配错误，未开始组件不伪关闭。
2. **配置与固定骨架**：默认配置插件继续在 Init 调 loader。cmd 的配置 loader 复用现有 Load，在成功读取后调用 Bootstrap 固定时区函数，保证 time.Local 在 logger 创建后台队列前设置；不增加时区插件/hook/JSON 接口。verify/现有 json.CheckJSON/Gin diagnostics 与启动告警仍是固定骨架，在选定日志可用后执行；失败由同一注册器回滚，JWT 仍一次装配，错误与清理错误一起返回。
3. **Runtime/失败路径**：先 SetForceSignal，再 registrar→整图 Init/Start→取配置/借用资源/JWT→providers/最终仪表→中间件 Build。Runtime 只持能力/registry/所选日志 owner 的 drain；不调用默认 SDK 或独立资源 Close。对初始化后 JWT/观测/模块/路由失败按扩展逆序、再插件逆序回滚，根因及关闭错误用 Join。
4. **预算与信号**：插件失败回滚/Stop 总预算各 30 秒。Runtime.Close 保留调用者更短 deadline/cancel；App/命令只在入口已取消而必须收尾的边界建立独立 30 秒 context，并接回 force signal，不重复 WithoutCancel 抹去已建立停止预算。固定清理 context helper 只有一个信号观察者并 cancel/join，不在 goroutine 中运行插件回调。providers/后段插件回滚共用一个按首次失败懒建的 30 秒预算；模块旧无 ctx Close 如实记录合作限制，不为其引入逃逸协程。
5. **正常与异常关停**：保留 producer→LB 等待→HTTP drain→consumer→bootstrap worker→模块/扩展/pprof→registry 的既有顺序。正常由 registry 逆序关 cache、Redis/MySQL、logger、config；日志 Stop 与独立 DrainLogs 共用该 logger owner 的一次关闭，不再同时 SyncAccess/Sync 或构造其他 logger。存活 worker 继续跳过其共享资源关闭，返回 ErrWorkerNotExited；只排空选定日志、保留非成功结果。独立 drain 的并发调用共享结果，等待者自己的 ctx 可取消，不重复关闭 owner。
6. **迁移和轻量命令**：MigrationPluginRegistrar/具名最小集合仅配置、日志、数据库及同 owner drain；NewMigrationRuntime 必须收到显式 registrar，保留 15 秒连接启动上限。cmd 选择 OpenMigrationDB 和迁移凭据（未配回落运行身份的原固定告警，不含凭据）；server 用原运行身份。MigrationRuntime.DB 改为借用 *gorm.DB，cmd 不再提取具体 ormx。迁移子树 ExecuteContext 传播命令 ctx，命令错误与关闭失败同时保留；关闭建独立 30 秒并接 force。doctor 默认无网络/deep 才 Probe、JWT 部分读取、module/scaffold 加载语义不变。
7. **默认实现与兼容层**：内存缓存不访问 Redis；默认图按已获准的静态依赖注册，required 推导只迁调用位置。移除 builtin/logging.InitWithConfig/fixedConfiguration 等阶段 3 后无生产消费者的临时桥，不保留 Bootstrap 默认选型副本；必要测试改为真实 Plugin.Init/所选 owner，保留原行为/失败断言。普通工具/模块、telemetry、worker 等不新增插件。

### 15.3 步骤、文件、验收和停止条件

| 原步骤 | 前置与具体文件/交付 | 检查与可复核证据（原清单；实际结果见 15.5） | 停止条件/状态 |
|---|---|---|---|
| 3.1 | 已读规则/原计划/错误/真实调用方；cmd/plugins.go（新）、bootstrap/plugin_registration.go、runtime.go、processruntime.go、app.go；默认组合测试迁到 cmd，Bootstrap 用替身 | 记录基线；注册不读 Value/不建连、配置只加载一次、六种能力对应注册实例、缺 registrar fail、无 builtin 生产 import；每个必要引用保持可编译 | 定向验收通过；证据见 15.5，原停止条件继续适用 |
| 3.2 | 3.1 接口已固定；bootstrap/providers.go/rollback.go、requirements.go 的失去调用的旧包装清理、必要新增 cleanup.go；Runtime/NewApp/模块失败回收测试 | JWT/metrics/模块/Build 后段失败的 owner 账本与逆序错误链；正式仪表先于 Build；同一次失败共享 30 秒 rollback deadline，ctx values 保留，startup 取消不吞 cleanup failure | 定向验收通过；证据见 15.5，原停止条件继续适用 |
| 3.3 | 3.2 失败路径明确；Runtime.Close/DrainLogs、App 收尾与 cmd/signals.go、相关 alert/shutdown 测试 | synctest 验证总 30 秒、更短父 deadline/取消、第二信号、并发 Stop/Drain、部分关闭失败与日志一次；HTTP/worker 原退出顺序、存活 worker 不关共享资源 | 定向验收通过；证据见 15.5，原停止条件继续适用 |
| 3.4 | 生命周期 capability 可用；bootstrap/migrateruntime.go、cmd/migrate.go/plugins.go、原 db_credentials/migration 测试与轻量命令必要引用 | 最小三插件账本、无 Redis 迁移 fake SQL、运行/DDL 凭据和 DSN deadline 原断言；命令根因+close error；help/default doctor/JWT 不初始化主应用 | 定向验收通过；证据见 15.5，原停止条件继续适用 |
| 3.5 | 上述实际结果齐全；docs/architecture.md、docs/observability.md、pkg/log/README、scripts/check-architecture.sh、对应检查 | 默认实现只在组合根；依赖边界传感器/Go list/gopls+rg；受影响全量 -race/-shuffle/覆盖率、build/vet/lint/go fix、严格 change；回读后记实施结果 | 定向验收通过；证据见 15.5，原停止条件继续适用 |

所有实现步骤保持既有依赖版本，不引入新配置项，故真实 env/example 不变；有编译或契约失败先观察根因、记录错误再修复，同一问题最多三轮。阶段 3 验收并记录后直接继续阶段 4，仍仅对匹配 change 操作。阶段 4 的具体 Makefile prerequisites/真实本机服务/发布依赖差异在执行前按已有事实细化；缺资源或权限保留未验证，不虚构已通过或归档。

### 15.4 实际执行记录（持续填写）

阶段 3 改码前基线：`GOCACHE=/tmp/gin-api-gocache go test -p=4 -race -tags=scaffold -count=1 -timeout=5m ./internal/core ./internal/bootstrap ./cmd`，退出码 0；三包实际通过，日志 `/tmp/gin-api-stage3-baseline.log`。匹配 change apply 状态 ready（23 项，14 项完成，阶段 3–4 共 9 项待办），后续结果尚未验证。

### 15.5 阶段 3 实际验收与结论（2026-10-21，Asia/Taipei）

范围为 27 个 Go 路径（含凭据测试从 Bootstrap 到 cmd 的旧路径），加架构传感器与三份说明、计划和匹配 change。入口选择六类默认插件，Bootstrap 无 builtin 生产 import；注册不加载配置/建连。MigrationRuntime 仅接三个插件并借用 GORM；配置/日志/建连共享最小启动 15 秒上限（Core 单回调仍 10 秒），DDL 使用命令 5 分钟超时，不安装服务 I/O deadline。JSON、依赖版本、配置键/值、业务/存储结构均未改；Core 与阶段 1 提交无差异。

| 实际检查 | 真实结果 | 证据 |
|---|---|---|
| 28 个受影响/契约包 `go test -p=4 -race -tags=scaffold -shuffle=1792584378927286000 -count=1 -timeout=5m -covermode=atomic -coverprofile=... -json` | exit 0；包 pass=28；测试及子用例 pass=1523、fail=0、skip=0 | /tmp/gin-api-stage3-final-test.jsonl；final-test.err；final.cover |
| `go tool cover -func` | 28 包合计 84.9%；Core 98.0%、Bootstrap 84.0%、cmd 73.1%、logging 96.6% | /tmp/gin-api-stage3-final-coverage-summary.log；JSON package output |
| 并发首次排空/等待者取消/并发 Close/回滚预算 `-race -count=100` | exit 0（3.737s）；不是容量或任意调度证明 | /tmp/gin-api-stage3-concurrency100.log |
| 全仓 `golangci-lint run --build-tags=scaffold ./...` | exit 0，0 issues；未降门禁 | /tmp/gin-api-stage3-final-lint2.log |
| 正常 `go build ./...`、`go vet -tags=scaffold ./...`、`go fix -diff -tags=scaffold ./...` | 依次真实成功（组合命令 exit 0），modern 输出 0 字节 | /tmp/gin-api-stage3-delivery-{build,vet,modern}.log |
| 架构传感器；匹配 change strict JSON 验证 | exit 0，architecture OK；valid=true，issues=[] | /tmp/gin-api-stage3-verified-architecture.log；stage3-spec.json |
| gopls references + 全仓 rg/源码逐段回审 | registrar、Runtime.Close、NewMigrationRuntime 引用实际返回；cmd/server/migrate、NewApp/providers/Run defer、生命周期实现及测试已适配；未证明其他平台和仓外消费者 | /tmp/gin-api-stage3-{registrar,close,migration}-references.log |
| 固定源码 SHA256 前后核对 | 429 个 Go 文件，changed=[]；覆盖期间未改源码 | /tmp/gin-api-stage3-final-go-sha.json |
| 隔离反证：恢复 Runtime 的 Stop(WithoutCancel(ctx)) | exit 1；shorter/canceled 均错误等满 30 秒，契约确实失败 | /tmp/gin-api-stage3-parent-counterexample.log；setup 失败另存，不当反证 |
| 隔离反证：移除 DrainLogs mutex | exit 1；3 份 DATA RACE，首次排空契约失败；原树未去锁 | /tmp/gin-api-stage3-drain-counterexample.log；副本 /private/tmp/gin-api-stage3-counterexample-bdkuk83v |

关键契约：Init/Start 中途失败、后段配置/JWT/校验器/中间件 Build 失败均聚合原始和清理错误并逆序清理；正式 metrics 先于 Build；启动取消不吞回滚，已消费 20 秒的外层预算仅剩 10 秒；Runtime.Close 不抹去父 ctx；独立 Drain 并发共享结果、等待者可取消，panic 转契约错误；存活 worker 不停共享插件；signal supervisor 与清理观察者 cancel/join。原 HTTP/worker 分阶段退出、最小命令 help/doctor/JWT/module、安全与告警断言保留。

本阶段发生的字段接线、漏迁测试调用、lint、虚拟时间夹具与工具错误记 ERR-021–028；旧失败输出保留。预算夹具改为等待 Done，原失败 seed 已实跑通过，未放宽 deadline。SDK void Sync/无 context Close 的合作限制继续存在，未宣称硬 30 秒、任意插件内存隔离或永不 OOM。阶段 3 定向验收通过，完整发布准入仍待阶段 4；按用户已给授权直接继续，不提交/push/上线。

## 16. 阶段 4 改动前实施细则与记录

### 16.1 范围与步骤（已执行，技术及归档结果见16.5–16.6）

沿用原 4.1–4.4，不新增阶段。阶段 3 已验收；Go 1.27.2 darwin/amd64、golangci-lint 2.14.0，沿用可写 GOCACHE/GOLANGCI_LINT_CACHE。保护阶段 2–3 工作树；不修改依赖版本、真实配置/存储，不隐式拉镜像/起容器，不改上游。出现仅由本轮引入的既有门禁失败，先记录实际输出/文件，计划内定向修复及相关回归；需要扩大模块、依赖或数据范围时停在相关步骤。

| 原步骤 | 前置与实际操作 | 验收证据/交付物 | 停止条件/状态 |
|---|---|---|---|
| 4.1 | 固定源码 SHA 后正常 build 与 make check；现有 prerequisites 为 architecture、capability-inventory、scaffold-isolation、tidy、package-vars、doc-refs、image-digests、security-eval、release-wiring、appmeta、hooks、build-context、tool、vet、modern、coverage；coverage 包含 scaffold、race、5m | 逐项真实命令/退出码、完整覆盖率（现门槛 72.5 不降低）、失败原文；源码冻结期间不编辑 Go，若修则全部相关检查重跑 | 通过：双环境 build/make check exit0，均82.9%；见16.5；未发布 |
| 4.2 | GOWORK=off 同样正常 build/完整门禁或对应完整测试；记录 logger 的锁定版本与本地路径差异、Core/能力/门面依赖与替换契约 | 两环境可复核日志与解析来源，不修改 go.mod/sum/work 来掩盖差异 | 通过：双环境完整门禁/依赖来源/替换契约已复核；保留 SDK 差异，见16.5 |
| 4.3 | 先查已有镜像/容器；只复用已确认本地测试服务。核实实际 loader 的非敏感连接目标，禁止在未确认本地的目标上发请求；测试只用本次独有键/数据，清理不碰既有数据 | MySQL/Redis 真实探活、HTTP/两类缓存、停止与信号结果，和 mock 分列；无授权不执行 schema 迁移/已有数据清理 | 通过：复用本地 MySQL/Redis，两环境各8 pass/0 fail/0 skip；无 DDL/生产操作，见16.5 |
| 4.4 | 4.1–4.3 证据齐全后逐项填 A-01–14，严格验证 matching change，只有全部要求满足才归档并回读 | 完整验收矩阵/失败与未验证项/实际回读；不把合作式预算当硬中断，不编造 QPS/峰值内存或 OOM 证明 | 通过：矩阵、隔离回退、严格校验、实际归档及主规格回读均完成；见16.5–16.6 |

无新配置项；不做真实 schema/生产操作。阶段 4 如发现文档/登记表因本轮移除路径失效，先把实际文件列入本节后只修这次引入的引用；不顺带清理无关历史。最终验收报告区分开发验证、真实功能验证、生产容量/发布证据。

### 16.2 已发现事实与定向补充（实施前）

首轮 make check 停在 5 个因本轮迁移失效的文档引用，exit 2；加入 SCAFFOLD.md 仅同步配置 loader/Validate 测试与数据库 opener 的真实路径，计划中的旧路径改为保留历史语义并指向现文件，不新增 doc allowlist。GOWORK=off 正常 build 已 exit 0。默认 Docker socket 受沙箱限制，只读提权后确认已运行 devilbox-mysql-1（devilbox/mysql:mysql-8.0-0.7，3306）与 devilbox-redis-1（redis:latest，6379）；只复用这两个，不拉镜像/起新容器。

为原 4.3 增加持久回归文件 cmd/plugins_integration_test.go（integration && !windows）：显式开关才运行真实依赖；使用既有 loader 在程序内加载本地认证输入，不回显或向外传凭据；强制 host/addr 为 127.0.0.1 的上述端口，配置只写测试独有 0600 临时文件。复用实际 server/migration registrar、临时 JWT 密钥、独有 AppName/缓存前缀，禁用业务模块/任务/通知/遥测，仅在临时配置中限制池和内存。用真实 HTTP 验证 memory/Redis 缓存与身份绕过、资源 Stop 后 SQL/Redis 不可再使用；预设本地未监听端口验证失败后可重新启动；迁移仅初始化和只读 Ping，不执行 DDL/DML。独立二进制进程真实 SIGTERM/第二信号验证正常/强制退出；最多45秒并在测试末尾 kill/join 自己的子进程。只删除测试独有 Redis keys，禁止 FLUSH、迁移、改既有数据。任一连接失败真实记录，不猜账号/密码、不重试未知身份；其他不依赖真实服务的验收继续。

前置：先读取本节、实际配置结构/默认实现/Root 参数和测试写法；新增测试不改生产代码或配置键/依赖。完成后冻结全部 Go 文件并重跑 make check、锁定环境完整检查和带 integration 的本项测试；修复只针对本次产生的问题，三轮同问题仍失败则停止该项并报告。真实 MySQL/Redis、日志字段/容量、OOM 与发布证据分列，不用本机小流量测试代替容量。

### 16.3 锁定 SDK 与真实夹具修正（实施前）

锁定环境第二轮 make check 停在测试 typecheck：logging/options_test.go 引用了本地新 SDK 的 ErrNoOutput，v2.3.0 没有。实际两份 logger.go 已读取：锁定版本全部输出关闭时回退 stdout，本地版本显式报错。该错误类型不是项目日志契约；不升级 SDK 或改用户输出配置，不给默认实现再造版本选择器。只修同一测试：先用已解析 SDK 的 console=false/file=false/replaceGlobals=false 构造得到真实 SDK 基线，验证项目适配器在对应成功/失败下保持发布/不发布、错误传播、释放旧绑定及后续初始化成功；所有原日志/字段/告警/关闭断言继续运行。将两版本这项既有差异如实列出，不能表述为任意参数完全同义。文件仅 builtin/logging/options_test.go，需两个环境完整该包及门禁复验。

真实验收夹具的 cfg.Log 清零后漏填 schema 要求的 path；只在 cmd/plugins_integration_test.go 填本次 t.TempDir 下路径，保持 FileStdout=0。原配置、生产代码与 SDK 不变。当前所有检查已退出，修改后重存 Go SHA，先验真实资源/日志包，再串行跑质量门禁（独立 coverage 文件），不并行争用 golangci-lint。

16.3 补充：typed cfg→临时 YAML 的映射会把 nil 墓碑 map 编为 modules.user={}，真实 loader 正确拒绝。测试明确不启用业务模块，序列化时省略整个 modules 段以保持有效的“缺席”语义；不改生产解码或墓碑校验。负向启动必须断言 init plugin "mysql"，不能由任何配置错误冒充连接失败。只改同一 integration 测试夹具，真实依赖仍待复验。

16.3 补充（池）：真实测试为减负把 Redis PoolSize=2，MinIdleConns=0 按原语义采用 redisx 的3，SDK拒绝。源码已核对，改同一测试的显式 MinIdleConns=1（不复制/改写生产默认值），失败输出区分 SQL/Redis/cache 能力，重跑该真实套件。前一轮迁移及实际进程正常/强制SIGTERM已通过，缓存未通过，不勾4.3。锁定环境 make check 已退出0、完整覆盖率82.9%；后续检查继续使用独立产物且源码冻结。

### 16.4 收尾前验证细则（待执行项单列）

双环境最终 make check 与真实本地套件已真实退出0；当前只剩最终矩阵/严格校验/归档回读。为核对回退方法，在本次独有临时目录恢复阶段3前快照的 Go 源码，使用相同本地 SDK workspace（规范路径）运行正常 build 和 Core/Bootstrap/cmd/logging 的 race；不回退当前分支或真实数据，不复制 .env/真实配置/私钥。其结果只证明该阶段代码可回到已保存基线，不代表数据库或生产回滚。

主项目 go.mod/go.sum 与阶段3前 SHA 一致。该快照实际未覆盖被忽略的 go.work，不能以 None 哈希比较声称它有改动；当前 go.work 的 mtime 为2026-08-24，未由本轮编辑，实际解析来源已分别保存。本轮最终Go源码清单为430文件，仍需收尾前复核无变化。归档步骤同时遵守用户“执行后才勾完成”：4.4 本身包含归档/回读，不提前谎报归档已完成；CLI 成功并回读实际目录后才勾该项。

### 16.5 阶段 4 最终技术验收与回退结果（2026-10-21 实际执行）

范围为本分支插件重构，基于 HEAD 7434bd5；保留用户阶段1提交及阶段2工作树。环境 Go1.27.2 darwin/amd64、golangci-lint2.14.0；GOCACHE=/tmp/gin-api-gocache、GOLANGCI_LINT_CACHE=/tmp/gin-api-golangci-lint。全部 Go 修改完成后冻结430文件；最终清单 /tmp/gin-api-stage4-final-go-sha4.json，收尾仅编辑记录和匹配 OpenSpec artifacts。以下 exit0 均来自真实进程终态，不把日志存在当作执行成功。

| 检查/实际命令 | 实际结果与证据 |
|---|---|
| GOFLAGS=-p=4 make check COVERAGE_PROFILE=/tmp/gin-api-stage4-workspace-final.cover | exit0，52个带覆盖率的测试包输出，聚合82.9%≥72.5%；/tmp/gin-api-stage4-workspace-final-check.log；包含既有全部 prerequisites、scaffold/race/5m及覆盖率棘轮，未降低门槛 |
| GOWORK=off GOFLAGS=-p=4 make check COVERAGE_PROFILE=/tmp/gin-api-stage4-locked-final.cover | exit0，52个带覆盖率的测试包输出，聚合82.9%≥72.5%；/tmp/gin-api-stage4-locked-final-check.log；两个完整门禁串行执行、coverage 独立，不争用 lint 锁 |
| go build -p=4 ./... 及 GOWORK=off go build -p=4 ./... | 均exit0；/tmp/gin-api-stage4-final-build.log、/tmp/gin-api-stage4-final-locked-build.log |
| go vet -tags=scaffold,integration ./cmd；go fix -diff -tags=scaffold,integration ./cmd | 均exit0，modern输出0字节；/tmp/gin-api-stage4-integration-vet.log、/tmp/gin-api-stage4-integration-modern.log |
| golangci-lint run --build-tags=scaffold,integration ./cmd ./internal/plugin/builtin/logging | exit0、0 issues；/tmp/gin-api-stage4-integration-final-lint.log；全仓 lint/vet/modern 已由两次 make check 执行 |
| PLUGIN_INTEGRATION=1 go test -p=4 -race -tags=integration -run '^TestPluginRegistryRealDependencies$' -count=1 -timeout=5m -json ./cmd | workspace exit0、8 pass/0 fail/0 skip（包含父/分组事件）；/tmp/gin-api-stage4-real-final.jsonl，.err为空；不是8个独立业务场景 |
| 相同真实套件加 GOWORK=off | exit0、8 pass/0 fail/0 skip；/tmp/gin-api-stage4-locked-real-final.jsonl，.err为空 |
| 两环境 logging 完整包 -race | 均exit0；/tmp/gin-api-stage4-workspace-logging.log、/tmp/gin-api-stage4-locked-logging.log；不只重跑零输出兼容单例 |
| Core/能力/门面/Bootstrap 依赖检查 | /tmp/gin-api-stage4-core-deps.log、/tmp/gin-api-stage4-capability-imports.log；Core 非标准库闭包只有自身，能力/门面无 SDK/内置实现，Bootstrap 无 builtin 生产依赖；完整架构传感器均通过 |

依赖来源：workspace logger 为 /Users/xiaozhaofu/go/src/my-gtkit-package/logger/v2；GOWORK=off 为 module cache 的 github.com/gtkit/logger/v2@v2.3.0。原始解析记录 /tmp/gin-api-stage4-workspace-logger.json 与 /tmp/gin-api-stage4-locked-logger.json。零输出时本地 SDK 报错、锁定版回退 stdout；适配器保留各自行为，测试同时验证失败不发布、成功 owner 释放及后续可初始化。不伪装为完全同义，不修改依赖版本。go.mod/go.sum 与阶段3前 SHA 一致；go.work 的证据限制见16.4。gtkit/json v1.0.0、间接 json/v2 v2.1.1 保持现状，不新增 JSON 生命周期插件。

真实验收只复用 devilbox-mysql-1（devilbox/mysql:mysql-8.0-0.7，127.0.0.1:3306）与 devilbox-redis-1（redis:latest，127.0.0.1:6379），没有拉取镜像或新建容器。cmd/plugins_integration_test.go 复用实际 loader/registrar 和0600临时配置，连接目标锁定回环，凭据不回显。覆盖 MySQL 负向握手失败后重新装配、memory/Redis HTTP 两次命中且 handler只执行一次、Bearer请求绕过、独有键前缀/正TTL≤2秒、仅清理自有键、SQL/Redis连接池停止及迁移最小组合只读 Ping。SQL Stop后 Ping 为已关闭错误且 OpenConnections=0；Redis Ping失败且 TotalConns=0。真实二进制分别观测就绪200、首 SIGTERM 正常退出；强制场景先观测503再发第二信号，非零退出。测试子进程45秒上限、末尾 kill/join；不运行 schema 迁移、DDL/DML/FLUSH、业务数据变更或生产请求。真实迁移验证装配/会话/关闭，不代表生产迁移权限或 DDL 演练。

隔离代码回退演练：/private/tmp/gin-api-stage3-rollback-zt452z_v 恢复阶段3前快照，467项保存内容配合必要公开构建文件和规范化临时 go.work，不复制真实配置/私钥。go build -p=4 ./... exit0；go test -p=4 -race -tags=scaffold -count=1 -timeout=5m ./internal/core ./internal/bootstrap ./cmd ./internal/plugin/builtin/logging exit0，四包全部ok。证据 /tmp/gin-api-stage3-rollback-build.log 与 /tmp/gin-api-stage3-rollback-race.log。没有回退当前分支；不代表阶段2完整回退、数据回滚或生产恢复时长证明。

性能/内存边界：go test -p=4 -run '^$' -bench '^BenchmarkPluginRegistryLifecycle$' -benchmem -benchtime=100ms ./internal/core exit0，/tmp/gin-api-stage4-core-bench.log。Intel i7-9750H，本轮单样本无I/O六插件：独立图27357ns/op、6848B/op、88allocs/op；链20312ns/op、6968B/op、98allocs/op。仅合成启动图分配，不是请求吞吐、改前改后统计改进或峰值堆证明。注册图不在请求热路径，无运行期无界历史/重试队列；已有缓存、请求体、连接池与队列边界按原配置保留。生产QPS/p99、峰值堆/cgroup、持续负载和部署退出宽限期未实测，不能声称永不OOM或生产Ready。

日志规则例外：用户2026-10-21明确允许 internal/bootstrap/http.go 中 http.Server.ErrorLog 必需的 *log.Logger 类型桥。log.New(httpErrorWriter{}, "", 0) 仅转发至所选日志插件，没有独立后端；不扩大其他 log/slog 使用。日志 Sync() 无context/error，部分 SDK Close不支持context，30秒为合作式总预算，无法硬终止任意阻塞I/O或证明日志送达；正常/失败/强制路径保留非成功语义，不用遗弃回调goroutine掩盖阻塞。

需求/不变量回审：六类扩展点由 cmd 显式选择、Core 四方法和拓扑/并发/回滚契约保持，默认实现不反向渗入消费端；缓存静态依赖 Redis 已获用户允许，内存实现不访问 Redis；JSON、MQ、普通工具及业务端口未额外插件化。事务/幂等/权限/SQL业务规则与schema未改，不因此宣称已重新验证所有业务数据流程；HTTP安全/身份绕过、异常响应、凭据选择与日志脱敏沿用原断言并通过本次相关全量回归。没有待修复的本次核心功能阻断；SDK限制与生产容量/发布未验证单列。结论：本次重构开发验证及功能验收通过；生产发布准入未评定。

合规检查摘要（仅指上述实际覆盖范围）：

- [x] Go1.27现代特性：双环境 modern门禁通过
- [x] 分层架构：架构传感器及导入边界通过
- [x] 错误处理：根因/关闭错误链、非成功退出契约通过
- [x] 并发安全：全仓race、受影响并发100轮及反证通过（15.5）
- [x] 超时控制：共享30秒回滚/Stop、父预算/force契约通过，SDK合作限制保留
- [x] 无硬编码敏感信息：仅临时生成测试密钥/正常loader本地认证，不回显凭据
- [x] 资源泄漏检查：唯一owner/借用保护、真实池关闭及子进程join通过，非任意SDK阻塞证明
- [x] 测试覆盖：双环境82.9%≥72.5%，真实套件无跳过
- [x] 交叉验证：需求→实现→测试→文档矩阵、隔离反证和回退证据齐全
- [x] 无编造内容：失败/修正记录ERR-20261021-029至036保留，生产/容量/SDK限制明示

本节保存技术验收完成时的事实；后续严格验证/归档的实际结果见16.6，4.4已在实际归档回读后勾选。

### 16.6 严格验证、实际归档及最终回读（2026-10-21）

复用已核实入口，命令统一前缀 OPENSPEC_TELEMETRY=0 /Users/xiaozhaofu/.nvm/versions/node/v24.22.0/bin/node /Users/xiaozhaofu/.nvm/versions/node/v24.6.0/lib/node_modules/@fission-ai/openspec/bin/openspec.js；未安装或升级工具。

1. validate add-plugin-lifecycle-registry --type change --strict --json --no-interactive：exit0，valid=true、issues=[]；/tmp/gin-api-stage4-spec-before-archive.json。
2. archive add-plugin-lifecycle-registry -y：exit0；/tmp/gin-api-stage4-archive.log 实际输出 Specs updated successfully、新增8项要求及 archived as 2026-10-21-add-plugin-lifecycle-registry，没有 Aborted。原4.4包含归档/回读本身，按用户“真实执行后才勾选”优先规则，CLI当时提示22/23并由已授权的 -y继续；未提前虚报4.4完成。
3. 实际回读 openspec/changes/archive/2026-10-21-add-plugin-lifecycle-registry/tasks.md 与 openspec/specs/plugin-lifecycle-registry/spec.md；主规格8项要求与本次delta一致。validate plugin-lifecycle-registry --type spec --strict --json --no-interactive exit0、valid=true、issues=[]；/tmp/gin-api-stage4-spec-after-archive.json。list --json exit0，/tmp/gin-api-stage4-changes-after-archive.json 中目标不再active。
4. 以上真实归档/回读后才将归档tasks的4.4勾选，23/23项完成；主规格自动生成的Purpose占位改为本次实际目标。最后仅核对主规格/记录/文档引用与差异，不修改Go或重复覆盖已通过测试产物。最终复核结果见本节收尾行。

错误记录029–036的根因与修复已完整回读并按脚本关闭。036为归档后的只读检索误指不存在的构建文件，已改用Makefile返回的真实check-doc-refs入口；不将该失败当代码问题或验收通过。没有重开其他change，不提交、不推送、不发布。当前分支仍为pluggable，Core阶段1提交未改，阶段2–4改动留在工作树供用户审阅。

最终收尾复核：GOCACHE=/tmp/gin-api-gocache make check-doc-refs 实际exit0；/tmp/gin-api-stage4-final-doc-refs.log 为自检3个死引用被捕获、16种良性形态未误报，实检32份文档通过、2项既有认领豁免，未新增豁免。填写主规格Purpose后再次严格验证exit0、valid=true、issues=[]，/tmp/gin-api-stage4-final-spec.json。git diff --check exit0，go.mod/go.sum/go.work的git差异为空；430个Go文件与最终验收清单SHA全部一致，零新增/缺失/改变。归档tasks回读23项勾选、0待办；原active目录已不存在。所有计划阶段在本次重构开发/功能验收范围完成，保留16.5所列生产容量、发布及SDK硬终止限制。
