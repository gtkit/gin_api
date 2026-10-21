# 编译期插件架构重构方案与实施计划

修订日期：2026-10-21（Asia/Taipei）。

> 本版状态：完整计划已获用户确认；当前仅获授权执行阶段 1（Core 注册器、契约测试与验收记录）。用户确认失败回滚、插件停止各使用 30 秒总预算。阶段 2–4 未授权，不修改内置组件、Bootstrap 接线或业务代码，不提交、推送或发布。
>
> 阶段 1 已按第 13 节完成 Core 定向验收；阶段 2–4 的目标行为、接口草案、拟新增路径和验收命令仍待实施/待验证。旧版执行记录单独保存在第 12 节，不能作为本版扩展目标已完成的证据。全仓 lint 存在阶段外格式阻断，不能宣称完整项目准入。

## 1. 需求来源与本次修订

需求来自用户在当前 `pluggable` 分支提出的架构约束，以及随后认可的评审方案：使用接口与插件注册器实现编译期可插拔，由业务入口显式选择和注册实现；统一执行 `Name`、`Init`、`Start`、`Stop`；按依赖排序，禁止循环依赖；只抽象有替换需求的组件。

本期完整覆盖六类扩展点：配置、日志、数据库（默认 MySQL）、Redis、缓存、HTTP 中间件。不使用 Go 标准库 `plugin` 包、动态 `.so`、运行期发现或自动扫描，不预埋 MQ 接口。

本版保留原计划阶段 0–4 的编号和主顺序：计划与基线 → Core → 内置插件 → Bootstrap/迁移接线 → 全量验收。阶段 2 扩展为六类组件，阶段 3 相应覆盖所有入口和消费端；旧版将配置、日志、中间件排除在非目标的条款不再适用。

修订还纠正两类口径：当前已有插件代码不等于完整目标已经实现；历史测试记录不等于本版代码、完整失败路径或真实依赖验收已经通过。

## 2. 当前事实、问题与未确认项

### 2.1 已核对的工作树事实

- 当前分支为 `pluggable`。工作树已有代码修改、删除和未跟踪文件，包含 `internal/core/`、`internal/plugin/`、`internal/bootstrap/plugin_registration.go` 等；它们属于本轮开始前的工作内容，不能覆盖或作为可随意丢弃的临时文件。
- 模块为 `github.com/gtkit/gin-api`，`go.mod` 声明 Go 1.27.0。文档阶段记录的工具链为 go1.27.2、本仓 `go.work`；阶段 1 已重新核对工具链与定向测试，见第 13 节。
- 当前服务装配链为 `cmd/server.go → bootstrap.NewApp → NewRuntime → 注册器 Init/Start → ResourcesFromClients → newProviders → App.Run`。旧版将 `data.NewResources` 写成当前主路径，已经不准确。
- `internal/core/plugin.go` 已有四方法接口；注册器已有依赖校验、拓扑排序、状态和停止错误聚合代码。MySQL、Redis、响应缓存已有默认插件实现，但三者仍在同一个 `internal/plugin` 包。
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
| `gtkit/logger/v2` | `go.mod` 为 v2.3.0；workspace 引用本地源码，版本常量为 v2.5.0 | 分别验证 workspace 与 `GOWORK=off`，不自动升级依赖 |

本地 gtkit 内容源为 `/Users/xiaozhaofu/go/src/my-gtkit-package`。本轮仅记录边界，不改该目录。现有 `Sync()` 无 error 返回，不能从其返回值证明日志刷盘、关闭全部成功；这项限制必须进入日志验收记录。

阶段 1 已核实本机 OpenSpec 入口、匹配 change、apply-ready 和严格验证；定向测试结果见第 13 节。真实 MySQL/Redis 验收、部署退出宽限期和生产容量仍未确认。旧计划的 CLI 故障不代表当前不可用；不编造 QPS、性能收益、真实回滚耗时或完整项目通过率。

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

### 5.1 拟定布局（尚未创建/迁移）

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

这是本版明确列出的插件实现拆分范围，不是现有目录事实。旧 `internal/plugin/mysql.go`、`redis.go`、`cache.go` 的实现迁入相应子包并更新引用，不另留一个默认实现副本。

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
- 缓存使用 Redis 时才声明 Redis 依赖；不为顺序好看而建立 MySQL → Redis 等不存在的依赖。日志依赖配置，所有需要正式日志的插件依赖日志，保证逆序关闭时日志最后。
- 正常关停保留现有 producer/LB/HTTP/consumer/bootstrap worker 阶段，再关闭模块与扩展，最后通过注册器停止缓存、Redis/MySQL、日志、配置；不重复直接调用这些资源的 Close。
- 存活 worker 时仍跳过插件共享资源关闭，返回现有可判别错误；独立日志排空由日志插件拥有的幂等排空能力承接，和正常 Stop 共用所有权状态，不另建 logger 或重复关闭。普通业务消费者不取得该管理能力。
- 迁移只选择配置、日志、数据库插件，使用迁移专用凭据和 opener，不保留具体 `*ormx.Client` 作为 Bootstrap 对外契约，不初始化无关 Redis/缓存/中间件。
- doctor 默认只加载和校验配置；deep 才创建对应探测资源并明确释放。JWT 密钥命令保持专用部分配置读取，不启动主应用。开发期 module 命令与集成测试的配置加载调用一起迁移，不意外改变 CLI 行为。

## 7. 文件范围与已有改动保护

### 7.1 当前阶段 1 允许修改的文件

仅 `internal/core/plugin.go`、`plugin_registry.go`、既有及新增 Core 契约/基准测试、本文档、匹配的 `openspec/changes/add-plugin-lifecycle-registry/` artifacts 和本次错误记录。第 7.2 节其他文件为阶段 2–4 范围，当前不得修改。保留阶段开始前工作树快照作为定向回退依据。

### 7.2 未来 Core、默认实现和装配范围

| 文件/位置 | 拟改动与边界 |
|---|---|
| `internal/core/plugin.go`、`plugin_registry.go`、对应测试 | 保留四方法，修复清理集合、状态等待、预算和错误传播 |
| `internal/plugin/interfaces.go`、拟新增六类契约文件及对应测试 | 固定最小能力与具名参数，去掉 Stager 和具体 tracker 的实现强制 |
| `internal/plugin/{mysql,redis,cache}.go`、`plugin_test.go` | 分别迁到第 5.1 节子包；原测试责任随实现迁移，不以删测试完成重构 |
| 第 5.1 节六个 `builtin` 子包及各自测试 | 新增/迁入默认插件，不增加第七类扩展点 |
| `internal/platform/config/load.go`、`jwt_command.go` 及其加载/环境/文件安全/JWT 命令/随附配置测试 | 解析器与部分配置读取迁入默认配置子包；schema、默认值、校验留原包；共享辅助函数随真实调用关系一起迁移，禁止留下反向 import 的兼容 wrapper |
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

代码阶段开始时才核对 OpenSpec CLI 和 active changes；只复用明确匹配的 change，歧义时停止选择；CLI 不可用或未 apply-ready 不改应用代码，除非用户明确授权跳过。不因旧计划的手工 artifacts 或历史 CLI 故障自行绕过。当前阶段 1 复用匹配的 `add-plugin-lifecycle-registry`，同步 artifacts 并确认 apply-ready 后修改 Core；不归档仍含阶段 2–4 待办的 change。

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

### 阶段 2：六类内置插件、能力边界与资源所有权（待实施）

阶段前置：阶段 1 验收通过并获阶段 2 授权。每个组件先按真实引用列出配置字段、路径、键/逻辑库、TTL、超时、日志与调用方兼容清单，再迁移；不凭印象补齐。由于接口/包拆分会影响现有引用，表内每一步包含它的必要调用方和测试引用同步，构成可编译的原子步骤；完整入口组合与关停切换留在阶段 3。

| 步骤 | 前置条件 | 具体改动/交付物 | 检查方法与验收证据 | 停止条件 | 当前状态 |
|---|---|---|---|---|---|
| 2.1 | 阶段前置；配置/JWT 读取和共享 helper 清单齐全 | 建立配置能力；schema/校验留原包，koanf 全部默认读取移入 builtin/config，迁移 server/migrate/doctor/JWT/module 和四组集成测试的必要引用 | 加载、APP_ 覆盖、APP_ROOT 排除、未知键/缺文件/权限拒绝、非法配置、JWT 部分配置、未 Init 取值用例；替代 loader 不改变 Core；schema 包不再导入 koanf | 需要改配置键、默认值、路径、凭据校验或形成 config↔builtin 环 | 待实施 |
| 2.2 | 2.1 配置能力可用；现有日志调用与字段/告警清单齐全 | 建立日志事件契约/门面及 builtin/logging；迁出 notifier 构造；逐项迁移第 7 节日志调用与关联测试，业务 port 保留 | 替代 logger 接收到主/访问/channel/告警和 context 字段；SQL adapter 脱敏；正常与部分构造失败关闭各一次；门面不依赖 gtkit/zap/news；无告警递归；Sync 错误限制显式记录 | 出现字段/脱敏/告警丢失，或真实上游缺口尚未获对应授权 | 待实施 |
| 2.3 | 2.1–2.2 能力可注入 | 数据库能力与 MySQL 默认实现拆开，opener/probe 移入所属默认包；数据视图不拥有连接；迁移保留用途区分 | 真正拥有连接的 fake/可关闭资源断言 Close 一次；开库后版本/会话校验失败回收；替代 Database 实现；服务与迁移 deadline/凭据保持；只读视图不关闭资源 | 需要改仓储/事务/方言、重复 owner 或绕开 MySQL 安全校验 | 待实施 |
| 2.4 | 2.1–2.2 能力可注入 | Redis 默认实现与消费能力拆开，opener/probe 移入所属默认包；可用性由视图适配，不强制具体 tracker | 未配置、required 不可达、optional 不可达、nil/部分资源+error、取消和重复关闭；逻辑库/键前缀无变化；关闭次数可观察 | 引入重连/热替换、新逻辑库/前缀、降级吞取消或出现第二关闭者 | 待实施 |
| 2.5 | 2.4 Redis 能力固定 | 默认缓存独立；consumer 只用 Enabled/Middleware，旧 Stager 留实现内部；内存/Redis 按真实依赖注册 | 两后端和禁用态；第三方替身无需创建 Stager；身份请求绕过、响应准入、显式挂载；cache Stop 不关 Redis；本地 store 只释放一次 | 自动扩大缓存范围、改变 key/TTL/容量，或借用资源被关闭 | 待实施 |
| 2.6 | 配置/日志契约稳定，已核对现有链序与观测创建时间 | 中间件组合插件及 Build 具名入参；router 只安装返回链；普通 handler 不插件化 | 事件顺序与第 5.2 节一致；仪表先于 Build；413/404/405/OPTIONS/业务 panic/观测中间件 panic 保持响应、日志、指标；不重复安装链 | 默认安全能力遗漏或排序改变，或为顺序新增通用优先级框架 | 待实施 |
| 2.7 | 2.1–2.6 完成 | 核对六类契约、独立实现包、数据借用视图与兼容层；补齐阶段证据 | 按实际包清单运行六类插件、config/data/log/httpcache/router/middleware 及本阶段改动调用方的定向全量 race 测试；mock/内存证据与真实依赖证据分开记录 | 关键组件仅测试构造/指针置空，不能证明真实资源关闭；或扩大到第七类插件 | 待实施 |

阶段交付：六类可替换契约、默认子包、必要引用迁移、资源/调用方映射与测试记录。旧 NewResources 自有构造及相关测试在确认无生产调用后迁移其测试责任并移除实现依赖，不保留永久双模式或删除故障覆盖来获得通过。

### 阶段 3：Bootstrap/迁移接线与关停回归（待实施）

阶段前置：阶段 2 通过并获阶段 3 授权；不存在未说明的日志能力缺口或 owner 冲突。若阶段 2 保留已知限制，阶段 3 必须继承该未验证项，不将其改为成功。

| 步骤 | 前置条件 | 具体改动/交付物 | 检查方法与验收证据 | 停止条件 | 当前状态 |
|---|---|---|---|---|---|
| 3.1 | 阶段前置；所有构造无资源副作用 | 调整入口 registrar 签名与能力集合；默认实现只由 main/cmd 显式选择；配置先 Init，其他插件后取 Value | 入口事件账本证明注册期无加载/建连、配置只加载一次；能力与已注册实例一致；替代组合无需改 Core；Bootstrap 不导入 builtin | 需要让 Core 隐式选实现或反射解析依赖；出现未注册却已创建的资源 | 待实施 |
| 3.2 | 3.1 入口组合确定 | Runtime/newProviders 消费能力；复用已有 telemetry/metrics 顺序；缓存/中间件不再通过具体对象取用 | NewApp 后段 JWT/仪表/模块/BuildEngine 任一点失败，扩展与插件按正确顺序回滚；Stager/具体 SDK 不泄露消费端；前后行为一致 | 正式仪表创建前绑定到 no-op，或失败路径丢弃清理错误 | 待实施 |
| 3.3 | 3.2 装配回滚通过 | Runtime/App 停止 ctx 接线，日志独立排空合并 owner，保留固定 worker/HTTP 阶段 | 正常信号、启动期信号、第二信号、HTTP 异常、worker 异常、存活 worker、部分 Stop 失败、超时；借用资源只关一次；未退出资源返回非成功结果 | 提前关闭存活 worker 所需插件资源、第二信号失效、无界等待或假成功 | 待实施 |
| 3.4 | 3.1–3.3 能力组合可用 | migrate 只选配置/日志/数据库；doctor/JWT/module 保持最小读取和探测；移除 Bootstrap 的具体 ormx client 契约 | 无 Redis 仍能完成迁移装配测试；server/migrate 身份不同规则保持；DDL 不继承服务读写 deadline；doctor JSON/退出码和默认无网络行为保持；JWT 命令不启动主应用 | 轻量命令引入无关依赖或改变 CLI/迁移安全语义 | 待实施 |
| 3.5 | 3.1–3.4 实际通过 | 同步第 7 节架构/日志文档、针对性架构判据及相关测试；记录范围内调用方复核 | 受影响 bootstrap/cmd/router/middleware/module adapter 的定向全量 race 测试；源码/需求/测试/文档逐项映射；未改目录与业务接口 | 文档描述超前、隐藏存量依赖或通过放宽门禁解决失败 | 待实施 |

阶段交付：入口手工注册、六类完整接线、最小命令组合、关停/回滚证据和反映已实现行为的文档。阶段结束仍不执行 commit/push 或上线。

### 阶段 4：全量质量、架构和验收（待实施）

阶段前置：阶段 1–3 按本版标准通过，且获阶段 4 对应验证授权。当前版本的 Makefile 和质量规范是命令/覆盖率门禁唯一来源，不降低覆盖率或削弱断言。

| 步骤 | 前置条件 | 具体改动/交付物 | 检查方法与验收证据 | 停止条件 | 当前状态 |
|---|---|---|---|---|---|
| 4.1 | 阶段前置，工具入口/版本和可写 cache 已确认 | 执行正常构建与完整质量检查，修复仅限本次引入的问题 | `GOCACHE=/tmp/gin-api-gocache go build ./...`；`GOCACHE=/tmp/gin-api-gocache make check`；覆盖含 scaffold 的 vet/lint/race/覆盖率及真实 Makefile 全部 check prerequisites，记录命令环境/退出码/结果 | 工具不匹配、既有失败无法归因或需要范围外修复 | 待实施 |
| 4.2 | 4.1 可运行 | 分别核对本地 workspace 与发布依赖结果；用替身逐类替换并检查依赖边界 | `GOWORK=off` 下重跑构建及受影响完整测试/门禁；记录 logger 解析来源；Core/契约/门面无默认 SDK 反向依赖；固定图确定排序、无 import cycle | 只能在本地较新 logger 编译或依赖缺失，不能作为发布通过 | 待实施 |
| 4.3 | 明确为本地可用测试资源；真实依赖访问授权满足 | 验证真实 MySQL/Redis 启动、请求/缓存、失败恢复与释放，保留响应与资源证据 | 先查本机已有镜像/容器并复用；实际服务请求、两类缓存及信号退出；验证逻辑库/键/配置/日志脱敏不变；mock 与真实证据分列 | 无本地依赖或指向生产；缺镜像/容器未获授权；不创建替代数据或假报通过 | 待实施 |
| 4.4 | 4.1–4.3 结果齐全且 OpenSpec 前置有效 | 对第 9 节逐项给结果，完成实际代码 change 的严格验证/符合条件的归档，更新计划事实状态 | 在获准代码流程中执行相应 CLI 严格验证与正常门禁；仅全部要求满足时完成，并核对归档实际输出/目录结果；报告限制、失败及回滚能力 | 任何核心项失败/缺证据，不勾选通过、不宣称完成/Ready | 待实施 |

阶段交付：完整证据矩阵、执行环境差异、真实资源与退出行为结果、未验证项和最终结论。完整 change 归档留阶段 4；当前阶段 1 仅同步匹配 artifacts 并执行严格验证，不提前归档尚含阶段 2–4 待办的 change。

## 9. 最终验收矩阵（分阶段填写）

| 编号 | 验收标准 | 必须保存的证据 | 当前结果 |
|---|---|---|---|
| A-01 | 唯一 Core Plugin 四方法；无动态加载、MQ 预埋、Core 基础设施 import | 文件/依赖检查及相关契约测试 | Core 唯一四方法/标准库闭包通过；完整六类目标未完成 |
| A-02 | 六类扩展点均可替换，默认实现由组合根手工选择 | 六组替身组合测试；替换时 Core 无需改动的记录 | 未验证 |
| A-03 | 图错误在任何生命周期副作用前失败，排序确定且无循环 | 重复/缺失/自依赖/环/独立插件排序测试与回调计数 | Core 契约通过（含缺失、自环、多节点环、稳定就绪顺序） |
| A-04 | Init 部分失败、Start 中途失败/取消时完整逆序清理 | 三插件失败点不在末尾、当前部分资源、多个关闭错误、各 Stop 次数/顺序 | Core 契约通过（回调计数/顺序/错误链）；真实组件释放未验证 |
| A-05 | 等待可取消、回滚独立有界、父预算只收紧、强制信号贯通 | 受控时间/阻塞 fake 测试和最外层 App 信号退出证据 | Core 契约通过；App/进程信号接线与外层退出留阶段 3 |
| A-06 | 配置 schema/校验与默认加载器分离，所有命令读取语义兼容 | YAML/env/权限/未知键/JWT 专用路径及调用方测试 | 未验证 |
| A-07 | 日志可替换且原字段、脱敏、关联、访问/channel/告警行为保持，实例只关闭一次 | 替代 sink 事件、真实日志产物/敏感值反证、部分初始化与排空测试；Sync 错误限制单列 | 未验证，存在已知上游错误返回限制 |
| A-08 | MySQL 与 Redis 各唯一 owner，缓存借用连接，Resource view 不关资源 | 实际 Close 计数和真实池/连接释放证据，不只检查字段变 nil | 未验证 |
| A-09 | MySQL 安全/会话/凭据与迁移 deadline 不变，Redis required/optional 不变 | 真实探活和失败场景；服务/迁移身份及错误链证据 | 未验证 |
| A-10 | 内存/Redis 缓存、禁用/降级、key/TTL/身份及响应准入不变 | 两后端真实请求及故障注入结果；本地 store 退出/Redis 未被 cache 关闭 | 未验证 |
| A-11 | 中间件组合可替换，顺序/安全/观测与异常响应保持 | 请求事件账本、OPTIONS/413/404/405/panic、重复安装拒绝 | 未验证 |
| A-12 | Bootstrap/HTTP/worker 既有阶段顺序和最小命令组合保持 | 外层装配失败、正常/强制退出、存活 worker、migrate/doctor/JWT/module 回归证据 | 未验证 |
| A-13 | workspace 与锁定依赖均通过所需构建/测试，质量门禁不降低 | 两环境实际命令、依赖来源、退出码、覆盖率明细与 Makefile 门禁结果 | 定向 race/build/vet/Core lint 通过；全仓 lint 失败，完整双环境门禁未执行 |
| A-14 | 文档只描述实际行为，change 验证完成，回滚边界可复核 | 需求→文件→用例→结果映射、实际验证/归档及阶段回退记录 | 阶段 1 记录/严格 change 校验通过；全 change 未完成、未归档 |

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

以下保留后续阶段的核对边界；已在阶段 1 核实的项目以第 13 节实际结果为准：

- 本版计划与阶段 1 已获授权；阶段 2–4 未授权。用户补充的稳定性、性能、并发和内存要求纳入本阶段契约及后续组件替换验收，不将普通工具或没有替换需求的业务模块插件化。
- 阶段 1 的 OpenSpec 状态已核实；未来继续时复核同一匹配 change，不修改无关 change，不把 artifacts 齐备误报为全部实施任务完成。
- 真实本地 MySQL/Redis 的可用性和实际连接目标。缺镜像/容器先说明并取得对应授权，不隐式拉取，不触达生产。
- 非 context 关闭 API 的真实行为、日志 Sync 错误限制是否需上游专项补强及其授权；未补强前保留受限结论。
- 部署退出宽限期与既有 HTTP/worker/日志预算关系；未经核对不承诺硬退出时长。容量、压测和生产灰度数据未提供，本期不改容量配置或发布。
- 旧文档曾记载 ERR-20261020-138、140、141；阶段 1 实际读取的当前错误日志没有这些条目，不能推断其已解决或仍 open，不恢复/改写历史。当前新增记录为 ERR-20261021-001–004，其中全仓 lint 格式阻断保留 open，处置以真实检查为准。

## 12. 旧版执行记录（历史保留，本版未复验）

以下保留修订前计划中的记录，来源为旧文档，不能替代本轮命令输出或本版扩展目标的验收：

| 原阶段 | 旧文档记载 | 本版解释 |
|---|---|---|
| 0 | 原 MySQL/Redis/Cache 范围计划及手工 OpenSpec artifacts 已回读 | 原范围遗漏配置/日志/中间件；本轮仅修订本文件，不同步 artifacts |
| 1 | `gofmt -w internal/core/*.go`；`GOCACHE=/tmp/gin-api-gocache go test -race ./internal/core -count=1`，旧记载 exit 0；曾记录 ERR-20261020-133 | 不证明本版 F-01/F-02/F-03、三插件中途失败、预算等待已通过 |
| 2 | 插件/data 定向 race 测试旧记载 exit 0；miniredis loopback 受限，记录 ERR-20261020-135 | 不证明真实连接释放或六类替换；不能因受限而移除必须的真实验收 |
| 3 | bootstrap/plugin/data/cmd 定向 race 测试旧记载 exit 0；原三插件接线已写入工作树 | 与当前已有改动区分；六类入口、日志消费端、最小命令与关闭修复均待本版验证 |
| 4 | 旧计划标为待开始；错误记录随后有 make check/coverage 失败信息 | 完整门禁无本轮通过证据，不把历史阶段 0–3 状态沿用为本版完成 |

## 13. 本版状态与交付记录

### 13.1 授权、环境与改动清单

2026-10-21（Asia/Taipei）在 `pluggable` 分支执行。用户已确认完整方案，仅授权阶段 1；回滚与插件停止总预算各 30 秒。阶段 2–4 未执行，未进行 commit/push/tag/release、容器、数据库操作、依赖升级或目录调整。

阶段 0 文档已实际落盘并全文回读；当时仅修改本文件，旧文档阶段的无代码/无测试边界保持为历史事实。本次在代码修改前补齐阶段 1 实施细则并回读，同步匹配的 `add-plugin-lifecycle-registry` artifacts，CLI `instructions apply` 返回 `state: ready` 后才修改 Core。

- 工具：`go version` 为 `go1.27.2 darwin/amd64`；golangci-lint `2.14.0`（built with go1.27.2）；当前 Node `/Users/xiaozhaofu/.nvm/versions/node/v24.22.0/bin/node`。
- 本机 OpenSpec 入口为 `/Users/xiaozhaofu/.nvm/versions/node/v24.6.0/lib/node_modules/@fission-ai/openspec/bin/openspec.js`，版本 1.3.0；以当前 Node 执行，不安装新工具。以下 OpenSpec 命令统一带 `OPENSPEC_TELEMETRY=0`。
- Go 检查统一使用 `GOCACHE=/tmp/gin-api-gocache`；lint 同时使用 `GOLANGCI_LINT_CACHE=/tmp/gin-api-golangci-lint`（已由 `golangci-lint cache status` 核对）。构建/vet 使用 `-p=4` 限制并行编译。
- 原工作树/Core/plan/change/错误日志快照：`/tmp/gin-api-stage1-go53obnh`，含 `worktree-sha256.json` 和 `git-status.txt`。这是临时回退依据，不是提交或永久归档；未对工作树执行实际回退。
- 本阶段代码：`internal/core/plugin.go`、`plugin_registry.go`、`plugin_registry_test.go`、新增 `plugin_registry_contract_test.go`、`plugin_registry_benchmark_test.go`。
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
