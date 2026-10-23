# 直接使用 gtkit/logger：完整方案与实施计划

日期：2026-10-23（Asia/Taipei）  
修订：第 2 版，替代上一版全部实施设计。  
状态：**D1–D3 与阶段 1–3 已完成；阶段 4 已执行。golangci-lint 已升级到 2.15.0，迁移引入的 25 项 lint 告警已修复。第 17 节 P1–P6 已完成：22 项对应修复和脚本分组入口经完整 make lint 验证，全仓及 5 组脚本均为 0 issues、exit 0；本轮仅补改一处 HTTP 测试的 context 来源及注释。独立 vet、modern、全量 race、覆盖率未执行，cmd 既有超时仍不处理。历史检查见第 15–16 节，本次结果见第 17.7 节。**

## 1. 需求来源与本次纠正

原始需求：统一使用 gtkit/logger，去掉重复日志方法封装，保留必要初始化、生命周期管理和现有行为，先落盘方案供用户确认。

本轮明确指令：

> UseMain/UseAccess 不还是换了一种封装吗？我要直接使用 gtkit/logger，不要再包一层。重新梳理如何保留必要的初始化、生命周期管理和现有行为，更新方案给我确认，先别改代码。

上一版的错误：虽然删除了同名日志方法，却设计了 UseMain/UseAccess 回调入口，仍要求每次写日志经过项目层，没有落实直接使用 SDK。影响目前仅限方案文档，未实施到源码。

本版纠正：

- **main 日志直接调用 `logger.Info/Infof/Warn/ErrorCtx/HError` 等库函数。**
- **access 日志直接调用装配时注入的 `*logger.Logger` 实例方法。**
- 不增加回调入口、代理 Logger、日志 getter 热路径、借用/释放协议、写入计数器、core/sink 装饰器或换名门面。
- 初始化和生命周期管理只在创建、装配、关停阶段执行，不经过每条日志。
- 当前 SDK 无法等价保留的能力明确列为待确认差异，不能以“保留行为”为理由重新加封装，也不能擅自删除这些要求。

上一轮只授权更新本文；本轮用户已明确批准直接调用方案及 D1–D3 并要求实施。上一版方案 B 已被拒绝，不是可恢复的获准方案。防复发：确认是否直调 SDK 要看真实写入调用链，不能只看自定义函数是否叫 Info。

## 2. 目标、边界与不变项

### 2.1 目标

1. 删除整个重复日志门面 `internal/pkg/log` 的源码、测试和过时说明，所有真实读写方同批迁移。
2. 删除自有 `Field/Level/Stream/LogEntry/Logger/LevelEnabler` 和字段转换、级别分派、caller 跳帧缓存、字段池。
3. 使用 SDK 原生 API、`zap.Field`、`zapcore.Level`、SDK request-ID 存取函数。
4. 继续在 `internal/platform/logging` 管理 main/access 构造、部分失败回收、唯一 owner、正常关闭及通知资源顺序。
5. 通过现有 App/Runtime 的停止顺序满足 SDK 的使用前提；无法证明写入者结束时不强行关闭日志文件。
6. 将“业务输出必须保持”“被删除内部 API”“SDK 行为差异”分别列清，未验证的兼容性不写成事实。

### 2.2 必须保持

- 日志消息、级别、字段 key/类型、main/access 路由、caller/source 的职责；请求及 SQL 脱敏。
- 既有路径、文件轮转、压缩、保留策略、main 不缓冲和 access 缓冲配置。
- 普通 Error 不触发告警，原显式 HError 仍发通知；通知队列容量和渠道配置不变。
- HTTP 状态、业务错误码、对客文案、响应与缓存信封、request ID 生成和入站信任策略。
- 数据库、Redis、任务、指标、认证授权及业务模块结构。
- 当前依赖版本和项目目录层级。

### 2.3 非目标

不升级/修改 gtkit/logger，不编辑 module cache，不新增日志库、队列、配置项、通用执行器或替代包；不引入全局 stderr 重定向、unsafe/linkname 或修改 SDK 私有状态。不改 application 的 port 边界，不让 application/dto/models 直接依赖日志 SDK。

文档阶段未运行任何检查。本轮实施允许必要的普通构建、相关功能测试和按项目要求维护本次独立 OpenSpec change；普通测试显式 -vet=off。lint、脚本 lint、vet、modern、全量 race、覆盖率等功能完成后另行询问；不创建容器、不触达生产、不改其他任务、不提交或发布。

## 3. 已核实事实与证据边界

### 3.1 源码基线

- 项目为 `github.com/gtkit/gin-api`；当前声明 Go `1.27.3`、logger/v2 `v2.5.0`、zap `v1.28.0`、GORM `v1.31.2`、Gin `v1.12.0`。
- 上一版核对的 HEAD 为 `4e93cfab8765b6106db48b0cef3d7504983c1bbf`，当前工作区有其他任务的未提交内容；HEAD 不能替代实际工作区。实施前重新核对，不恢复到旧 HEAD。
- 上一版文本清单有 56 份 Go 文件引用项目日志包，其中生产文件 38 份、测试 18 份；日志包自身另计。本轮已重新核对关停、SDK、access 装配和日志清理调用，不把旧计数当作实施时完整清单。
- 当前 go.work 不替换 logger；方案期已读取本机锁定版本源码，当时尚未运行构建/测试；实施结果见第 14 节，未运行 SDK 仓库自带测试。
- 证据为文件内容和文本调用追踪，不是 LSP 完整调用图、运行结果或生产容量结论。

### 3.2 实施前与本版设计直接相关的事实（完成状态见第 14 节）

| 事实 | 来源 | 含义 |
|---|---|---|
| 包级日志函数和默认实例已由 SDK 提供 | SDK default.go | main 无需项目层转发 |
| 未设默认实例时 SDK 懒创建只写 stderr 的 fallback，不写文件、不改 zap 全局 | SDK default.go | 可以直接复用“启动前无日志文件”行为，但输出格式与旧门面不同 |
| SDK Sync 关闭队列和文件；要求先结束新写入和在途调用 | SDK logger.go、methods.go | 日志函数线程安全不代表可与关闭任意并发 |
| SDK Zap().Sync 只调用底层 Core.Sync，不执行 SDK 的队列/文件关闭流程 | SDK methods.go、zap logger.go/core.go | 可以在异常退出路径刷文件缓冲，但不能声称排空通知队列 |
| access 的 BufferedWriteSyncer 用同一 mutex 保护 Write/Sync；Sync 不 Stop | zapcore/buffered_write_syncer.go | 不关闭资源的刷缓冲与正常关闭是两种操作 |
| 实施前 Owner 的 gate/WaitGroup 是逐条写入的保护 | internal/platform/logging/owner.go | 移除门面后不再保留这套逐条协议 |
| 实施前 worker 存活时 App 调用 Runtime.DrainLogs，最终会关闭 SDK | bootstrap/app.go、runtime.go、resources.go | 此路径不能照搬到直调 SDK 后，否则仍存活的写入者可能写已关闭文件 |
| HTTP 强制 Close 或 Serve 返回不等于 handler 全部返回 | bootstrap/app.go 的 shutdownHTTP/gracefulShutdown | 不能仅凭监听器关闭就宣称所有日志源静默 |
| 日志初始化在资源获取最前；正常释放顺序是 cache、Redis、DB、logging | bootstrap/resources.go | 业务资源的收尾日志仍需发生在日志关闭前 |
| 日志关闭先于 dispatch、sms、mail、robot | bootstrap/resources.go | 保留正常通知资源顺序及原渠道保护 |
| 旧全局 Drain/Sync/SyncAccessLog 在当前生产调用方未发现使用，仅测试引用 | 本轮 cmd/internal 定向检索 | 删除这组内部便捷 API 不等于删除 Runtime 的生命周期清理 |
| access 创建点经 bootstrap.buildEngine → router.newGlobalMiddleware → middleware.GinLogger | providers.go、router/init.go、router/middleware.go | 原生 access 实例应沿装配参数传入，不新增每次写日志的查找层 |

全局规则、项目规则和相关错误记录已重读；历史失败不能当作本轮执行结果。其他任务的源码、计划和规则变化保持原状。

## 4. 兼容性结论：不能用新封装掩盖差异

用户已明确确认 D1–D3 全部按下表建议处理。确认的是变更契约，尚不代表实现或验证通过。

| 编号 | 当前行为 | 直调 SDK 的事实 | 拟处理与审批状态 |
|---|---|---|---|
| D1 | 未绑定时纯消息正文加换行写 stderr，忽略字段 | SDK fallback 同样只写 stderr，但用库的编码、级别和 caller 等元数据，可记录字段 | 复用 SDK fallback；保留 stderr/no-file/no-notify，但不承诺逐字相同的旧格式。**已确认接受格式差异** |
| D2 | 项目门面保存首个 stderr 写失败，供该门面的 Drain 回传 | SDK 的普通日志函数没有 error 返回或同等失败读取 API | 这组旧内部 API 随门面退场；生产未发现读取者，但能力确实不再提供。**已确认退场范围**；不伪称移到 SDK 后仍保留 |
| D3 | 逐条接纳可在 worker 仍活跃时封闭门面、等已接纳写完，再关 SDK | 裸 SDK 不提供项目的接纳/释放屏障；改默认指针也不能撤销已经取得的旧实例 | 正常关停先确认日志源停止再关；无法确认时只刷文件、不关 SDK/渠道，保留资源供进程退出或后续安全关闭。**已确认异常路径语义变化** |
| D4 | 原 HErrorf 先格式化一次，再调用 SDK HError | 裸 SDK HErrorf 的日志和通知可分别格式化 | 直接使用 `logger.HError(fmt.Sprintf(...))`，保持一次格式化；不增加 helper |
| D5 | 项目自有 request-ID context key | SDK 使用自己的 context key，nil/空值语义也不同 | 所有真实读写方同批迁移；有效 ID、header、响应和传播保持。出现真实空值覆盖依赖则先补方案，不能猜 |
| D6 | 可以向门面安装任意 fake Logger | 删除门面后只有 SDK 原生实例和现有 factory | 测试改用真实 SDK/临时文件或现有 factory；输出、错误、路由断言保留，不维持被移除的 fake 接口 |

**完整“所有旧行为逐项不变”与“当前 SDK 版本、直接调用、零项目写入层”目前不能同时承诺。** 具体不兼容项是 D1–D3，而不是整个日志系统无法简化。用户已同意 D1–D3 的处理口径，按此实施，不再以这些已解决的取舍阻止迁移；仍不得恢复封装或升级 SDK。

被删除的内部 API 测试和仍有效的业务/生命周期测试要区分：前者随明确批准的接口退场调整，后者不允许通过改断言隐藏回归。

## 5. 直接调用设计（已实施，验证见第 14 节）

### 5.1 调用形态

```go
// main：直接使用库函数。
logger.Infof("runtime initialized with env=%s", cfg.Env)
logger.WarnCtx(ctx, "operation failed", zap.Error(err))

// access：这是装配时传进来的 *logger.Logger，不是项目代理。
accessLogger.InfoCtx(ctx, path, zap.Int("status", status))

// 保持原显式告警的一次格式化。
logger.HError(fmt.Sprintf("process exiting with error: %v", err))
```

不出现 UseMain/UseAccess/WithLogger/Acquire/Release 之类入口；不复制 Info/Error，不在 core/sink 下隐藏写入代理。不通过闭包、函数值 alias 或新接口把日志重新转回项目层。

### 5.2 初始化与唯一资源 owner

继续使用 `internal/platform/logging.Open`、`Owner`、`options.go`：

1. 保存 SDK 原默认实例；在现有初始化串行边界维护唯一 owner，重复 Open 失败，不覆盖已活跃实例。这个保护只在 Open/Close 执行，不参与普通日志。
2. 沿用 main/access 现有 factory、配置和构造顺序；access 继续 WithReplaceGlobals(false)。
3. 两个实例均成功且 ctx 未取消后才 `logger.SetDefault(main)`；发布前后取消、nil、panic、部分成功继续回收自己创建的资源，保留原错误。
4. 暴露 `Owner.AccessLogger() *logger.Logger` 供**装配阶段一次性取原生实例**；不新增 MainLogger 或每次记录前的 getter。main 调用直接使用 SDK 包级函数。
5. 不把 SDK Sync/Undo 交给普通调用方；拥有者负责关闭，借用者只写。
6. 删除 Owner.Log/Enabled、项目事件转换、逐条 gate/WaitGroup、caller 表、字段池和项目 binding。只保留初始化和关闭所需状态。
7. 正常结束仅在默认实例仍等于自身 main 时恢复保存的 previous；旧 owner 不能覆盖后来者。

构造路径仍使用现有 SDK，不修改其资源和队列实现。唯一 owner 的保护不能被描述为运行中热切换能力。

### 5.3 access 原生实例的装配路径

沿现有结构增加必需参数，不改变目录或依赖方向：

- bootstrap 的 `logOwner` 生命周期测试接口增加 `AccessLogger() *logger.Logger`；`processLogs` 原样返回自身日志 owner 的 access 指针。
- `resolveInfra` 在装配时取得该指针，保存为 `infra.accessLogger`。
- `buildEngine` 设置 `router.Options.AccessLogger *logger.Logger`。
- `router.New`/newGlobalMiddleware 验证非 nil，再传给拟定签名 `GinLogger(access *logger.Logger, opts AccessLogOptions) gin.HandlerFunc`。直接误传 nil 的中间件构造 fail-fast，router 装配则返回明确错误。
- middleware 内直接调用这个实例；没有新的 access 全局变量、getter、封装函数或借用协议。
- 缺少 access 实例不能悄悄写 main。测试为新增构造参数提供原生测试实例，生产始终来自 owner。

GORM 不需要访问 access。main 的业务调用、GORM 和进程诊断使用包级 SDK API；现有 application Auditor port 继续由 runtime adapter 实现，不把 SDK 引入业务层。

### 5.4 正常关停：从停止日志源入手

只在**已确认没有在途写入者**时调用 SDK Sync；不再尝试在每条日志上计数。

1. 沿用现有关停阶段：停 producer、摘流量、HTTP Shutdown、停 consumer/bootstrap worker、模块/客户端/扩展清理。
2. 显式区分 HTTP Shutdown 成功、超时/强制 Close 和仅 Serve 返回。只有第一种或根本未启动 HTTP 才能作为 HTTP 已排空的证据；listener 关闭不作为证据。
3. 核对 worker 最终退出以及会产生日志的模块/客户端后台任务是否结束；收尾失败且无法确认静默时转入 5.5，不以取消 ctx 代替完成。
4. 退出告警仍在日志资源关闭前调用。所有业务资源的最终日志仍在本阶段内输出。
5. 在已静默的前提下，仅在默认实例仍归自身时恢复 previous，使后续普通诊断不用已关闭的 main。**SetDefault 不是在途屏障**，不能单靠这一步声称旧指针安全。
6. 保留 notification 的实际 drain ctx 绑定，再调用 access.Sync、main.Sync；随后按原顺序关闭 dispatch、sms、mail、robot。
7. Close 幂等，重复正常关闭不重复释放。SDK 自身 Sync 无 error/ctx 返回的边界保持如实报告。

SDK 队列回调也可能产生日志。实施前必须核对它们的退出/转向时序；在 main.Sync 超时后仍可能执行的回调，不能假称已结束。已有渠道的迟到回调保护要保留，若无法证明不会继续写旧 main，就按 D3 的未解决兼容问题停止，不以默认指针切换作为证明。

这改变的是日志资源可以安全关闭的前置条件，不增加每条日志调用的封装，也不把暂停新业务流量等同于暂停一切日志。

### 5.5 异常关停：保留仍被使用的实例

用户已确认对 D3 采用以下处理：

- worker 未退出、HTTP 只强制关闭或其他日志源未确认结束时，保留 main/access 和通知渠道，不调用会关闭文件的 SDK Sync。
- `Owner.Drain(ctx)` 在这条路径只按 access→main 调用原生 `instance.Zap().Sync()`，刷文件缓冲并聚合实际 flush 错误/ctx 错误；不执行自有写入代理。
- 这个操作**不封闭新日志、不关闭文件、不排空 SDK 通知队列、不等于完整日志关闭**。继续保留 ErrWorkerNotExited/原关停错误，不能报告退出告警已经送达。
- flush 无法硬中断底层 I/O，不新开 goroutine 把阻塞遗弃。现有预算只作合作式限制。
- Runtime 可继续合并重复异常 Drain 的结果；异常 Drain 不能消耗 Owner 的最终 Close-once。日志源后来确实退出时仍允许正常 Close。
- 正常业务与通知继续使用原生实例直到安全关闭或进程退出。该异常期间会继续记录日志，区别于旧门面封闭后的纯 stderr 回落，已经列入 D3。
- 这是异常退出时保留使用中资源的生命周期选择，不是新建统一日志层；资源最终回收与刷缓冲、通知送达必须分别报告。

### 5.6 输出、字段和专用协议适配

| 原调用/类型 | 直接迁移 |
|---|---|
| Info/Warn/Error、*f | 相同 SDK 包级方法，保留消息、参数求值顺序和级别 |
| WarnCtx/ErrorCtx | 同名 SDK 方法，原 ctx 原样传入 |
| HErrorf | 在调用点一次 fmt.Sprintf 后直接 SDK HError，不新增格式化 helper |
| Field/String/Int/Int32/Int64/Duration/Any | zap.Field 和对应原生构造；不保留别名、转换器或自有类型 |
| Err/NamedError | zap.Error/zap.NamedError；nil 错误保持跳过语义 |
| Stack | 调用点使用 zap.String(key, string(debug.Stack())) 保留原始栈文本语义 |
| Level、Stream、LogEntry、Log、Enabled | 删除；特殊路径自己选择原生实例与 API；GORM/HTTP 直接读原生 Core.Enabled，动态值不缓存 |
| ContextWithRequestID/RequestIDFromContext | SDK 同名 API，生产者、响应、缓存、出站和测试同批迁移 |
| Sync/SyncAccessLog 等项目别名 | 删除；资源 owner 统一负责关闭，测试用自己持有的实例/owner 清理 |

gincache 的 Logger 接口仅要求 Errorf/Debugf，原生 *logger.Logger 已满足；删除仅转发同名方法的 cacheLogger，在现有 Middleware 装配时传入 logger.Default()，不保留不必要的 adapter。

GORM 的 LogMode/Info/Warn/Error/Trace/ParamsFilter 是 GORM 协议实现，保留其必要适配；内部直接调用 SDK。HTTP writer、Gin logWriter、net/http ErrorLog、Auditor 也只保留各自框架/业务协议所需的适配，不把它们扩成新的通用日志层。

GORM 保留 fc 在级别过滤之后执行、SQL 参数隐藏、slow_threshold、负 rows、忽略 RecordNotFound、source 与 caller 的区别；不替换成契约未经核对的另一个 adapter。

access 保持最终状态 <400/4xx/5xx 对应 Info/Warn/Error，原 body 捕获边界、writer 能力、字段命名和脱敏不变。

request ID 继续由现有生成器产生 48 位 ID；X-Request-ID、request.id、request_id 和 GORM legacy trace_id 的实际值不变。不趁机重命名字段或扩展 tracing。

导入使用依赖的真实包名 `logger`，不使用 sdk/lib 等泛化别名；先检查局部变量遮蔽。旧零值 Field 占位改用原生 `zap.Skip()`，不传入零值 zap.Field；动态空 key 若有真实调用，保持原调用方的跳过语义。启动前 SDK fallback 会处理字段，需按 D1 核对已有脱敏规则，不因为换库入口扩大敏感信息输出。

## 6. 配置、资源与外部耦合清单

| 项目 | 保持/处理 |
|---|---|
| main 路径 | 空白回落 ./logs/app_client，相对路径继续由 resource.WriteRoot 锚定 |
| access 路径 | 配置 path 非空时追加 _access，否则 ./logs/access |
| 输出 | console/file/level、daily、压缩、MaxSize/MaxAge/MaxBackups 沿用现值；全部关闭仍返回 SDK ErrNoOutput |
| 保留规则 | MaxAge/MaxBackups 未配置时不透传 0，保留原默认逻辑 |
| 缓冲 | main 不缓冲；access 原 SDK buffer size、flush interval 不变 |
| 通知 | 64 条 SDK 待处理队列；默认 5 秒 SDK drain；现有发送预算和 30 秒进程清理预算不变，不增重试 |
| 安全 | query/body/敏感 header 脱敏，普通 Error 不发 H 通知，失败诊断不递归告警 |
| SDK 全局 | SetDefault 与 zap 全局是两种状态；只在启动/关闭处理，不承诺运行期热替换 |
| 数据 | 不改表、事务、查询、Redis key/DB/TTL、缓存格式、HTTP API、任务投递或指标 |
| 兼容差异 | D1–D3 单列，不以“配置不变”掩盖运行行为差异 |

无新增配置项，不改实际配置或 example。若实施必须更改配置、依赖或预算，先更新本文并取得对应授权，不由实施者自行扩展。

## 7. 文件范围（实施清单）

### 7.1 移除/保留的基础文件

- 拟删除 `internal/pkg/log/logger.go`、`logger_test.go`、`README.md`，前提是全部调用方和文档已迁移；不在原目录新建替代门面。
- 改造 `internal/platform/logging/owner.go`：只保留资源生命周期，增加原生 access 装配取值，去掉写入路径。
- 保留并核对 `internal/platform/logging/options.go` 的实际选项；不升级库或额外修改配置。
- 迁移 logging 下 owner_test、options_test、hotpath_contract_test、notification_test、benchmark_test；业务断言保留，被移除内部 API 的断言按第 4 节处理。

### 7.2 日志和 request-ID 调用方

与上一版实际清单一致；短路径按各行目录解释：

| 目录 | 已识别文件 |
|---|---|
| cmd | signals.go |
| internal/bootstrap | app.go、http.go、listener.go、migrateruntime.go、pprof.go、processruntime.go、providers.go、ratelimit.go、resources.go、runtime.go、singleton_worker.go、worker_scheduler.go、workermanager.go |
| internal/middleware | error.go、logger.go、ratelimit.go、recovery.go、requestid.go、routetimeout.go |
| internal/pkg | auditlog/auditlog.go、breaker/breaker.go、cache/cache.go、console/console.go、httpcache/cache.go、httpcache/envelope.go、httpclient/propagation.go、news/delivery.go、news/news.go、response/response.go、singleton/runner.go、upload/uploader.go |
| internal/platform | data/init.go、logging/owner.go、redis/owner.go、responsecache/owner.go |
| 其他 | internal/router/health.go、internal/runtime/module/auth/adapters.go |

### 7.3 本版新增的必需装配范围

- `internal/router/init.go`、`middleware.go`：原生 AccessLogger 依赖及传参。
- 已在表中的 bootstrap resources/runtime/providers/app：access 来源、静默前提、异常 flush-only 与正常关闭的区分。
- `internal/bootstrap/lifecycle_contract_test.go`：原生 access 测试依赖及清理时序；不改它的其他资源契约。
- `internal/middleware/logger_test.go`、logger_level_test、logger_benchmark_test、routetimeout_test、maxbody_test：实际调用 GinLogger 的原生参数适配。
- router 的 middleware_construction_test、health_test、engine_baseline_test、panic_observability_test 及真实 router.Options 构造方：补原生 access 测试实例，保留原 HTTP 断言。

其他已发现测试读写方包括 bootstrap/alerting_test、notification_test；middleware/requestid_test；pkg/console/console_test、httpcache/envelope_test、httpclient/propagation_test、response/response_test、upload/background_error_test；platform/data/gorm_logger_contract_test、gorm_logger_benchmark_test。实施时必须重新按真实引用核对，不能把此文本清单冒充所有构建条件下的完整引用集。

### 7.4 文档与保护范围

实施时仅同步 docs/observability.md 的日志/所有权章节、docs/architecture.md 对旧门面的描述及本文；已存在的其他任务内容不覆盖。旧历史计划不改。

scripts/check-architecture.sh 中 application/dto/models 和外部 pkg 的禁止依赖规则仍可保持，不为直调 SDK 放宽。旧路径作为脚本测试字符串出现时不能当成真实 import 删除。

不修改 go.mod/go.sum/go.work、实际配置、AGENTS/CLAUDE 或其他任务的未完成源码。错误日志仅按现有规则记录、关闭本任务真实发生的问题。删除旧包是本需求明确的冗余移除，不移动任何现有模块或建立新目录。

## 8. 验收标准与已确认差异

| ID | 验收内容 | 证据 |
|---|---|---|
| A1 | 生产日志写入直接到 SDK；无项目回调/代理/获取释放层，无旧类型和导入 | 实际源码调用链、差异与文本引用；必要协议 adapter 单列 |
| A2 | main/access 路径、字段、级别、caller/source 和脱敏不变 | 真实 SDK 临时文件及解析结果，分别断言两条流 |
| A3 | request ID 生成、HTTP/响应/cache/outbound 传播一致，无并发串值 | 现有 requestid、response、cache、propagation 和 HTTP 测试 |
| A4 | 普通级别动态变化有效；GORM/access 过滤前不格式化查询/构造昂贵字段 | 原生 Core.Enabled 与计数/故意 panic 的值；不是缓存布尔值 |
| A5 | 显式告警一次格式化，普通 Error 不通知，通知失败普通诊断不递归 | 可控 sender、Stringer 计数和实际消息 |
| A6 | 失败初始化、重复 Open、取消、部分资源、默认实例恢复正确 | factory 失败/取消用例，默认实例身份与资源关闭计数 |
| A7 | 正常 Shutdown 实际等完日志源再关闭；重复 Close 幂等 | 明确屏障及实际 HTTP/worker 生命周期，不能只看 listener 已关 |
| A8 | 异常路径不会对仍被写入的 SDK 调用资源关闭；flush 之后仍可写；后续安全 Close 可执行 | 活跃 worker、未返回 handler、迟到通知、重复 Drain/Close 用例 |
| A9 | 不修改 HTTP 状态/错误码/业务/数据/配置/依赖/目录结构 | 逐份差异和已有回归 |
| D1–D3 | 第 4 节的格式、内部错误读取、异常关闭语义 | **处理口径已确认；实现与功能测试已完成，证据见第 14 节** |

尤其不能把“删除门面后，手持原生实例在 Sync 后继续写也会被拒绝”写进验收；当前 SDK 没有该保证。对应旧测试需要明确区分用户批准退场的协议和继续必须满足的实际业务生命周期。

编译、mock、文件测试不能证明真实通知送达、生产磁盘持久性或容量。本节为验收目标；功能验收见第 14 节，后续获准检查见第 15 节，生产送达/容量仍未验证。

## 9. 分阶段实施计划

阶段编号沿用 0–4，重写被拒绝方案的内容；未曾获准的实现不保留为既定步骤。确认方案与允许执行哪个阶段分别记录，不重复询问已经明确授权的范围。

| 阶段/步骤 | 前置条件 | 具体动作 | 检查方法与验收证据 | 停止条件 | 交付物与当前状态 |
|---|---|---|---|---|---|
| 0.1 修订 | 本轮仅文档授权 | 重读全局规则、原方案、SDK 和直接相关装配/关停；在本文记录上版错误 | 实际读取内容和本版事实表 | 依赖事实不足不编造 | 当前只读核对完成 |
| 0.2 落盘 | 0.1 完成 | 本文替换旧推荐及回调方案，并完整回读 | 文件内容、无残留的生效旧设计 | 只允许改本文 | 文档阶段已更新并完整回读（历史记录） |
| 0.3 确认 | 文档可审阅 | 提交直接调用设计及 D1–D3 处理口径 | 用户明确回复 | 未确认差异，不进入源码 | 已明确确认 D1–D3 并授权实施 |
| 1.1 复核 | 设计、差异口径及阶段 1 已获准 | 重读计划/错误记录/最新源码；逐项确认全部日志生产者、access 注入和退出证据 | 文件/调用方清单，SDK 实际签名；明确未覆盖平台 | 发现仍无法证明静默的原生日志路径，先更新方案 | 已完成（调用链/SDK/装配核对） |
| 1.2 基线 | 1.1 一致 | 保存受影响文件的当前工作区快照，不覆盖已有差异；按第 10 节运行获准的普通基线 | 原件/SHA、实际命令、exit、失败/跳过 | 旧失败未解释或缺依赖，不修无关代码 | 已完成（19 包基线，exit 0） |
| 2.1 装配 | 阶段 1 通过且阶段 2 获准 | owner 增加原生 access 取值；bootstrap→router→middleware 显式传实例；配置不变 | 所有构造方与 nil 分支回读 | 需要新全局/代理/每次 getter | 已完成；实际证据见第 14 节，受控检查除外 |
| 2.2 调用 | 2.1 一致 | main 改 SDK 包级调用，access 改原生实例，字段/ID 所有读写方同批迁移 | A1–A5；GORM 和框架 adapter 不误删 | 消息/字段/ctx 语义无依据 | 已完成；实际证据见第 14 节，受控检查除外 |
| 2.3 关停 | D3 口径已确认 | 正常静默后关闭；强制/存活路径仅 flush；异常 Drain 不消耗最终 Close | A6–A8，特别核对 SDK 回调仍存活的情况 | 需要写入门面才能“通过”，或无法证明正常关闭前提 | 已完成；实际证据见第 14 节，受控检查除外 |
| 2.4 退场 | 全部真实调用方已迁移 | 删除旧日志包、API 及内部实现专属测试；同步仍有效行为测试和指定文档 | A1/A9；保留的业务断言逐条对应 | 通过删有效测试掩盖兼容差异 | 已完成；实际证据见第 14 节，受控检查除外 |
| 2.5 局部验收 | 所有接线连通 | 只格式化本次 Go 文件，build 与相关普通测试 | 实际输出、测试名、最终差异 | 同问题 3 轮未解决/无关失败/范围扩大 | 已完成；实际证据见第 14 节，受控检查除外 |
| 3.1 功能验收 | 阶段 2 完成且阶段 3 获准 | 真实 SDK 文件、HTTP、worker、通知和异常关闭验证；按已授权范围做定向 race | A1–A9 实际矩阵；D1–D3 批准记录单列 | 未证明的关键行为不能报兼容 | 已完成；实际证据见第 14 节，受控检查除外 |
| 3.2 交付 | 3.1 有真实证据 | 按 backend-acceptance 核对影响范围，更新本文结果并回读 | 明确源码、功能、生产证据各自界限 | 未运行被写成通过 | 已完成；实际证据见第 14 节，受控检查除外 |
| 4.1 受控检查 | 全部功能完成后，用户另行明确批准具体检查 | 只运行获准的 lint/脚本 lint/vet/modern/全量 race/覆盖率 | 工具版本、完整命令、exit 与原始输出 | 用户停止、工具阻断、无关失败；不绕过 | 已执行；首次结果见第 15 节，工具升级后 lint 与脚本检查见第 16 节；整体尚未全通过 |
| 4.2 收尾 | 已有 4.1 实际结果 | 仅修本次问题并在原授权范围复验、记录 | 定向补丁及对应重跑结果 | 需要扩大范围则先说明 | 已修复迁移引入的 25 项测试告警并通过 8 包普通测试；22 项存量告警、脚本入口及 cmd 超时保持未处理 |

阶段 1–3 与 D1–D3 按原授权完成；阶段 4 已在全部功能完成后取得用户独立授权，实际结果见第 15 节。

## 10. 验证方式（真实执行结果见第 14 节）

普通测试显式 `-vet=off`，防止隐式执行未授权 vet。使用实际可用的本机 Go，锁定依赖与 workspace 分开记录，不用 Docker，不下载或安装缺失工具。

```sh
GOWORK=off GOTOOLCHAIN=local GOPROXY=off go build ./...
GOWORK=off GOTOOLCHAIN=local GOPROXY=off go test -vet=off -count=1 -timeout=3m \
  ./internal/platform/logging ./internal/platform/data \
  ./internal/middleware ./internal/pkg/response ./internal/pkg/httpcache \
  ./internal/pkg/httpclient ./internal/pkg/console ./internal/pkg/upload \
  ./internal/bootstrap ./internal/router ./internal/runtime/module/auth \
  ./internal/pkg/news ./internal/pkg/auditlog ./internal/pkg/breaker \
  ./internal/pkg/cache ./internal/pkg/singleton ./internal/platform/redis \
  ./internal/platform/responsecache
```

迁移前基线还包含尚未删除的 internal/pkg/log，迁移后不再对不存在的旧包运行命令。cmd 默认配置测试有历史启动真实服务并超时的记录，先核对日志相关用例，不能为验证日志迁移直接运行会启动依赖的整个包。

针对真实 SDK 的测试使用现有 factory、临时文件、可控 sender 和 HTTP/worker 屏障；动态级别、正常/异常关闭和重复调用分别验证。不要仅用旧门面的 fake 替代直调 SDK 的验证。

所有结果记录实际 cwd、Go/SDK 版本、命令、环境参数、退出码、失败/跳过、日志路径；旧历史结果单列。用户未授权的门禁不通过 make check 或其他聚合入口间接执行。

## 11. 风险、失败处理与回滚

| 风险 | 应对 |
|---|---|
| 为保留每条写入保护重新造门面 | 明确禁止；无法满足的 SDK 契约列缺口并停下 |
| 错把 Serve/Close/取消当排空 | 以实际 Shutdown/任务完成为前提；未证实不资源关闭 |
| 默认实例恢复被当作同步屏障 | 明确它不能撤销已取得的指针，核对源任务和迟到回调 |
| access 缺失回退 main | 构造期非 nil 验证，测试和生产都显式注入 |
| 队列等待与文件 flush 混淆 | 分别记录：文件刷写、队列关闭、通知送达，不能互相替代 |
| 字段/ID/一次格式化变化 | 所有真实读写方同批迁移，保留原字段/消息测试 |
| 其他任务已修改同一文件 | 基于当前工作区做最小定向补丁，保留原有和并行改动 |
| SDK 无硬 I/O deadline | 如实保留合作式限制，不增加丢弃后台关闭的 goroutine |

同一问题连续 3 轮未解决停止并报告，不继续猜测。发现业务/接口/依赖/配置需要扩大变更时先补方案，不能通过放宽断言过关。

回滚步骤：

1. 保留失败原始输出与当前差异。
2. 使用实施前当前工作区快照，逐份反向撤销本任务的 import、调用、access 接线、生命周期变化和测试修改。
3. 同批恢复旧日志包及全部原调用方，不能只恢复半条链；保留其他任务的既有/后续修改。
4. 在已有授权范围内复验；未执行不宣称回滚成功。无数据、依赖版本、配置迁移需要回滚。
5. 不 reset --hard、不 git clean、不改历史、不提交或发布。

## 12. 文档阶段历史记录与审批状态

- 已完成只读核对：全局/项目约束、原完整方案、相关错误记录、SDK 关闭/默认实例/刷缓冲实现、App/Runtime/通知/access 装配。
- 已将上版“回调仍是封装”的错误及纠正记录在第 1 节；本次仅修改指定方案，不另改错误日志。
- 直接调用是用户已明确的约束，无需再询问是否接受封装。
- D1–D3 已由用户明确确认；阶段 1–3 获准执行，阶段 4 等待功能完成后单独授权。
- 本轮未修改源码、测试、配置、依赖、其他文档或 OpenSpec，未运行测试/build/门禁、容器、外部通知、提交或发布。
- 本文已完整回读，后续文字修订已逐段回读；本轮写入仅此文件。这不代表已实现或已通过兼容验收。

## 13. 2026-10-23 实施授权与当前记录

用户原话：

> D1–D3 都按你的建议处理。把确认结果更新到方案里，然后按方案开始实施，直接使用 gtkit/logger，不要再加日志封装。lint、vet、modern、全量 race 和覆盖率检查等功能完成后再单独问我。

- D1：接受 SDK stderr 原生格式，保留 no-file/no-notify 和脱敏约束。
- D2：删除旧门面逐条 stderr 错误留存/查询；生命周期和 flush 的真实错误仍处理。
- D3：正常先停完日志源再关闭；未停止或无法确认时只刷文件、保留 SDK 和通知渠道，后续可安全关闭。
- 本次按已读取的 AGENTS OpenSpec 规则创建独立基础设施 change，不接手已有通知/横幅/harness 任务；只对本次 artifacts 做验证。
- 已确认本机 Go 1.27.3 darwin/amd64，普通检查使用 GOWORK=off、GOTOOLCHAIN=local、GOPROXY=off。
- 阶段 1 已完成：快照目录 `/var/folders/_w/xwv4k3t12vn9054592gsk5200000gn/T/gin-api-logger-direct-lqbc1ufo`，119 份文件及 SHA256。相关 19 包的普通基线测试已通过（`-vet=off -json -count=1 -timeout=3m`，exit 0，原始输出 baseline.jsonl）。
- 阶段 2 已完成：已改为原生 API、access 装配与异常 flush-only；首轮普通构建 exit 0。迁移第二轮 9 包普通测试 exit 0（migration-second.txt）；首轮接线与测试失败已如实记录，未将失败标为通过。
- 本轮独立 OpenSpec change 为 `use-gtkit-logger-directly`，实施前 proposal/design/specs/tasks 已全部生成、CLI 确认 apply-ready；已通过严格验证，按基础设施规则以 --skip-specs 归档到 `openspec/changes/archive/2026-10-23-use-gtkit-logger-directly/`，未修改基线 specs。
- 新增测试文件仅用于原生 SDK 输出捕获和 HTTP 生命周期验证，不提供生产日志门面。补入文件范围：相关包 `native_logger_test.go`、bootstrap/logger_shutdown_test.go、middleware/logger_bound_test.go、router 测试构造方。
- 受控检查仍未运行；阶段 3 本次功能验收已完成，结果与限制见第 14 节。

## 14. 2026-10-23 实施与功能验收结果

### 14.1 实际交付

- 删除 `internal/pkg/log` 的源码、测试和 README；生产 Go 文件无旧包 import、LogEntry/Level/Stream、UseMain/UseAccess、caller 缓存或逐条接纳计数。
- `platform/logging.Owner` 仅有 Open、AccessLogger、Drain、Close：AccessLogger 只供装配取原生实例；Drain 调用原生 core flush，Close 单独幂等关闭。
- main 直接使用 SDK 包级 API；access 经 bootstrap → router → GinLogger 传原生实例；字段改为 zap.Field，request ID 读写方同批迁移。
- 同批移除 gincache 的同名 Errorf/Debugf 转发体，直接传 SDK 实例。GORM、HTTP/Gin writer 和 Auditor 保留其必需协议适配。
- 正常 HTTP Shutdown 成功才确认排空；强制关闭、仅 Serve 返回和扩展未确认结束时保留资源，只刷日志文件。真实 HTTP 用例验证 handler 此后仍能写 main/access，并在结束后完成安全 Close。
- `go.mod`、`go.sum`、`logging/options.go` 与实施前快照逐字相同；无配置或依赖版本变更。工作区原有 worker 指标、请求传播等改动保留。
- 同步本文、`docs/observability.md` 和 `docs/architecture.md`。本任务错误记录 ERR-20261023-108/110/112 已补齐原因和处置；其他任务记录未修改。

### 14.2 实际命令与证据

cwd：`/Users/xiaozhaofu/go/src/Project/gin_api`。Go：`go1.27.3 darwin/amd64`；logger/v2：`v2.5.0`；zap：`v1.28.0`。
所有 Go 命令均使用 `GOWORK=off GOTOOLCHAIN=local GOPROXY=off`，普通测试均显式 `-vet=off`。
原始输出根目录：`/var/folders/_w/xwv4k3t12vn9054592gsk5200000gn/T/gin-api-logger-direct-lqbc1ufo`。

| 执行 | 结果 | 原始证据 |
|---|---|---|
| 迁移前 19 包 `go test -vet=off -json -count=1 -timeout=3m`（第 10 节范围加旧 log 包） | exit 0，全部包通过 | baseline.jsonl |
| 迁移后第 10 节 18 包 `go test -vet=off -count=1 -timeout=3m` | exit 0，全部包通过 | functional.txt |
| logging/bootstrap 8 个重点用例 `go test -vet=off -v -count=1 -timeout=3m -run <用例集合>` | exit 0，包含真实 HTTP 三分支，均 PASS、未 skip | acceptance.txt |
| `go test -vet=off -count=1 -timeout=3m ./internal/platform/notification/...` | exit 0，5 包通过 | 本轮工具输出（notification/dispatch/mail/robot/sms） |
| 移除 cacheLogger 后 `go test -vet=off -count=1 -timeout=3m ./internal/pkg/httpcache ./internal/platform/responsecache ./internal/bootstrap ./internal/router` | exit 0，4 包通过 | cache-native.txt |
| `go build ./...` | 最终改动后 exit 0 | 本轮工具输出 |
| 本次 OpenSpec change 严格验证 | exit 0，1 项通过、0 issues | CLI `validate use-gtkit-logger-directly --type change --strict --json --no-interactive` |

新增重点用例：`TestNativeFallbackWritesOnlyStderr`、`TestExplicitAlertFormatsOnceForOutputAndNotification`、`TestAbnormalFlushDoesNotDrainOrCancelNotification`、`TestDrainKeepsNativeInstancesUsableUntilClose`、`TestHTTPProducersKeepNativeLogsUntilHandlersReturn`。另复验 `TestNativeCallerFieldsRequestIDAndStreams`、`TestFilteringPrecedesValuesAndTracksDynamicStreams` 和 `TestNotificationLiveWorkerOnlyDrainsLogging`。

### 14.3 A1–A9 对照

| 验收项 | 实际证据与结论 |
|---|---|
| A1 直接使用 | 源码调用链及定向引用核对；无旧门面，gincache 也直接注入 SDK；完成 |
| A2 输出与脱敏 | 原生 JSON/文件、main/access 分流、caller/source、query/body 脱敏测试通过 |
| A3 request ID | middleware、response、httpcache、httpclient 的原有传播测试通过 |
| A4 动态过滤 | GORM fc 计数为 0/1、原生动态 SetLevel、HTTP 最终状态级别测试通过 |
| A5 告警 | 一次格式化、64 队列上限、普通 Error 不告警、失败诊断不递归且无敏感目标测试通过 |
| A6 初始化/所有权 | nil/失败/部分成功/panic/取消、重复 Open、恢复 previous 与不覆盖新 default 测试通过 |
| A7 正常关闭 | HTTP/worker 收尾、资源逆序、幂等结果测试通过；不把 Serve 返回当排空 |
| A8 异常保留 | HTTP timeout/forced/Serve 退出三分支、活跃 worker、阻塞通知测试通过；flush 后仍可写、仍可正常 Close |
| A9 范围兼容 | 依赖/配置构造选项与快照相同；保留业务与 HTTP/缓存断言；只改本次接线和生命周期范围 |

D1–D3 是用户批准的契约变化；上表不表示旧门面所有行为逐字保留，也不表示原生实例在 Sync 后继续写会被拒绝。

### 14.4 证据边界与下一阶段

按 backend-acceptance 完成本次日志基础设施的功能验收。没有新增业务规则、事务/表/索引、权限分支、消息格式或重试策略；这些业务面不额外修改。真实 SMTP/SMS/机器人送达、真实数据库集成、生产磁盘持久性和容量未验证，不作生产发布准入结论。

SDK 的关闭仍是合作式 I/O；迟到通知的渠道接纳保护已用真实 SDK + 可控 sender 验证，但不把 native Sync 返回当作通知送达或硬期限保证。生产信号来源仍沿用现有监督者；默认实例恢复只处理之后的包级诊断，不提供通用在途写入屏障。

**lint、脚本 lint、vet、modern、全量 race、定向 race、覆盖率和聚合门禁均未运行。** 全部功能完成后再向用户询问阶段 4，不将当前实现授权解释为检查授权。未安装工具、未拉取/构建镜像、未创建容器、未执行 commit/push/发布。

### 14.5 收尾记录

- 最终 `GOWORK=off GOTOOLCHAIN=local GOPROXY=off go build ./...` 返回 exit 0。
- OpenSpec 严格验证返回 valid=true、0 issues；随后 `archive use-gtkit-logger-directly --skip-specs -y` 明确返回已归档。已回读归档产物与勾选状态；基线 specs 未改。
- 对照实施前快照确认依赖和 logging/options.go 未变化；只移除本任务指定旧包，不覆盖其他任务已有源码/文档改动。新增测试仅捕获或驱动原生 SDK，不给生产调用增加代理。
- 本方案的确认结果、实际阶段状态、失败及修复记录、命令结果与未验证项已回读；等待用户对阶段 4 的独立授权。

## 15. 2026-10-23 阶段 4：获准检查与限定修复

用户最新授权：

> 可以，执行 lint、vet、modern、全量 race 和覆盖率检查，只修复本次日志迁移引入的问题，不要改无关代码。完成后把实际结果更新到方案里并汇报。

第 14 节及其“未运行”状态是上一轮功能交付记录；首次检查结果单独记在本节，不覆盖历史结果。后续工具升级、lint 重跑及限定修复见第 16 节；本节的版本阻断为升级前历史状态。

### 15.1 前置事实、执行顺序与边界

- 当前 HEAD 为 `109ccd4`（日志迁移提交）；本轮开始时 `git status --porcelain=v1` 为空。原迁移前快照仍可用于逐文件归因。
- 本机 `/usr/local/bin/go` 为 go1.27.3 darwin/amd64；PATH 中 golangci-lint 为 2.14.0，`security/toolchain.mk` 要求 2.15.0。先运行真实项目入口，保留版本阻断，不安装/替换工具、不绕过门禁。
- 执行独立入口：`make lint`、`make vet`、`make check-modern`、`make check-coverage COVERAGE_PROFILE=/Users/xiaozhaofu/go/src/Project/gin_api/.cache/logger-unification-gates-20261023-4qkhl_ex/coverage.out`。
- 所有入口加 `GOWORK=off GOTOOLCHAIN=local GOPROXY=off`；race/coverage 使用项目默认 `scaffold` 构建标签，`ALLOW_NO_LOOPBACK=0`。覆盖率入口本身执行全量 `go test -race -count=1 -timeout=5m -covermode=atomic`，无需重复跑同一全量 race。
- 原始输出保存在 `.cache/logger-unification-gates-20261023-4qkhl_ex/`；不复用旧 profile，不把失败测试的部分 profile 当作完整覆盖率。
- 每项前置条件是本节授权与实际工具入口已核对；检查命令/退出码/输出构成证据。无关代码问题只记录、不修复；缺工具或重复旧失败不改项目约束、不删除测试、不降阈值、不跳过用例伪造全绿。
- 如发现本次日志迁移引入的问题，先在本节记录具体位置、归因和最小修复步骤，再改对应文件，重跑受影响模块及获准检查。检查三轮仍失败、无法明确归因或需扩大范围时停止对应修复。
- 未运行 `make check` 的其他传感器/发布/容器/集成入口，未改配置、依赖、架构或其他任务代码，未提交或发布。本轮未修改源码。

### 15.2 实际结果

以下日志均位于第 15.1 节的证据目录；实际入口、环境变量和退出码另存 `results.json`。

| 项目 | 本轮结果 | 证据 |
|---|---|---|
| lint | make exit 2；工具版本门禁阻断，实际 Go lint/脚本 errcheck 未开始 | lint.log |
| vet | make exit 0；go vet -tags scaffold ./... 通过 | vet.log |
| modern | make exit 0；go fix -diff -tags scaffold ./... 无改写输出 | modern.log |
| 主工作区全量 race | make exit 2；cmd/TestServerUsesDefaultConfigurationLoader 在 5m0s 超时，cmd 包 307.251s；其余 48 个有测试包通过，输出未出现数据竞争告警 | race-coverage.log |
| 主工作区覆盖率 | 全量测试失败，阈值比较未执行；coverage.out 仅为失败运行的部分数据，不作为通过证据 | race-coverage.log、coverage.out |
| 同提交隔离环境全量 race | make exit 0；49 个有测试包通过，输出未出现数据竞争告警 | isolated-race-coverage.log |
| 同提交隔离环境覆盖率 | 实际阈值检查通过：82.7% >= 72.5%，未修改原阈值 | isolated-race-coverage.log、isolated-coverage.out |

失败归因与范围处理：

- lint：项目要求 2.15.0，本机为 2.14.0，与旧 ERR-20261023-100 一致；`security/toolchain.mk` 相对迁移前提交 `4e93cfa` 无变化。本轮记录为 ERR-20261023-113，保持 open。未绕过版本校验，也未安装/替换工具。
- 主工作区超时：与旧 ERR-20261023-091/101 一致。测试将 APP_ROOT 指向空临时目录，但配置查找会继续回落到源码目录内的未入库本地运行配置；堆栈显示测试进入 App.Run/awaitShutdown 等待关停。`cmd/runtime_assembly_test.go`、`internal/platform/config/load.go`、`internal/runtime/resource/path.go` 相对 `4e93cfa` 均无变化；干净同提交环境通过，进一步支持环境归因。本轮记录为 ERR-20261023-115，保持 open。
- 已执行检查未发现需在本次授权范围内修复的日志迁移新增问题；不修改上述既有工具或配置问题。未把“无数据竞争告警”当作失败运行已通过，也不以隔离结果覆盖主工作区失败。

### 15.3 同提交的隔离验证

主工作区 full race/coverage 未被替换或提前终止，已等待其真实失败结果。基于旧配置回落问题，追加同一提交的干净本地工作树作环境对照：

1. 实际执行 `git worktree add --detach /Users/xiaozhaofu/go/src/Project/gin_api/.cache/logger-unification-gates-20261023-4qkhl_ex/isolated 109ccd4`，exit 0；保留同一提交全部代码/测试，未复制主工作区未入库的运行配置。
2. 在该目录执行 `GOWORK=off GOTOOLCHAIN=local GOPROXY=off ALLOW_NO_LOOPBACK=0 make check-coverage COVERAGE_PROFILE=/Users/xiaozhaofu/go/src/Project/gin_api/.cache/logger-unification-gates-20261023-4qkhl_ex/isolated-coverage.out`，exit 0；标签仍为 scaffold，覆盖范围仍为 ./...，含完整 race 与 72.5% 原覆盖率门槛。cmd 包 10.014s 通过，logging 包覆盖率 97.9%，项目总覆盖率 82.7%。
3. 两边 HEAD 均为 `109ccd47432d9339beaad2e2e83ff9ecd23eeace`；两边 `*.go`、go.mod/go.sum 相对 HEAD 均无差异，隔离工作树 git status 为空。结果记录于 `source-comparison.json`。不删测试、不改配置、不修复与迁移无关的配置查找行为。
4. 完成后执行 `git worktree remove /Users/xiaozhaofu/go/src/Project/gin_api/.cache/logger-unification-gates-20261023-4qkhl_ex/isolated`，exit 0；临时工作树已移除，日志、两个 profile 和结果记录保留在证据目录。未修改业务目录/模块层级、主工作区源码或 Git 历史。

### 15.4 收尾与未解决项

- 本轮仅更新此方案及项目错误记录，保存检查证据；未修改任何 Go 源码、测试、配置、依赖或其他任务文档。
- vet/modern、同提交隔离环境的全量 race/覆盖率已有实际通过证据；lint 尚未运行到代码检查，主工作区全量测试仍失败，整体门禁不标为全通过。
- 未解决项是本机 golangci-lint 2.14.0 与项目要求 2.15.0 不匹配，以及主工作区本地配置影响缺配置测试。未将本次检查授权扩大为安装工具或修复无关代码的授权。
- 两项失败均已记录实际命令、输出和归因；本次没有源码修复补丁，因此没有修复后的额外重跑。后续若处理既有阻断，应另行明确对应范围。

## 16. 2026-10-23 golangci-lint 2.15.0 升级与 lint 重跑

用户后续授权：

> 把 golangci-lint 升级到项目要求的 2.15.0，重跑 lint 并把实际结果更新到方案里；cmd 的既有超时暂时不处理，不要改无关代码。

### 16.1 目标、事实与实施边界

- 目标：将 PATH 当前使用的 `/Users/xiaozhaofu/go/bin/golangci-lint` 从 2.14.0 升至项目要求的 2.15.0，实际重跑 `make lint`，如实记录 Go lint 与脚本 errcheck 结果。
- 已核对目标为普通可执行文件（非符号链接），旧程序报告 2.14.0、Go 1.27.2；本机 Go 为 1.27.3，GOOS/GOARCH 为 darwin/amd64。
- 官方 [v2.15.0 Release](https://github.com/golangci/golangci-lint/releases/tag/v2.15.0) 已确认存在、非 draft/prerelease。系统 curl 通过 HTTPS 取得 release API：darwin-amd64 归档 SHA256 为 `767b899a28285e88ded2823d5dcc4bc5ef681173eb2d80f029a7b17ee4ece3b8`，官方 checksums 文件 SHA256 为 `364f56cb7ef4555e42bb1754b432b0f40bd914477d962c5888c8a8b5378ce8b6`。最终下载内容已按两份官方摘要校验。
- Python urllib 首次查询因本机证书链校验失败，未下载/安装程序；系统 curl 保留 TLS 校验后查询成功。不关闭 TLS 校验，不修改系统证书或网络配置。
- 变更获准的工具文件、本文及必要项目错误记录；发现迁移新增告警后，按第 16.4 节仅修复本次迁移的 15 个测试文件。原始证据与旧程序备份保存到 `.cache/logger-lint-upgrade-20261023/`。不修改项目依赖、工具版本要求、lint 规则、配置、目录架构或其他任务文件；不重跑 cmd 测试、独立 vet、modern、race、覆盖率或聚合门禁。
- 兼容边界：保持原 PATH 安装位置和执行权限，只升级工具至已有固定版本，不改变应用运行接口、存储、业务行为。lint 若报告问题，先记录具体位置和归因；只处理已确认属于本次日志迁移的问题，无关问题仅报告。

### 16.2 步骤、失败处理与回滚

| 步骤 | 前置条件 | 具体操作 | 检查与可复核证据 | 停止/失败处理 | 状态 |
|---|---|---|---|---|---|
| 16.2.1 保存依据 | 授权、规则、原方案及官方版本已核对 | 回读本节；创建证据目录，备份旧程序并记录 SHA256、版本及工作区状态 | 原程序备份、版本输出、差异快照 | 无法备份则不替换 | 已完成；备份 SHA256 为 815ada10a5bc5ca1b40e84e7c33310148f633a4dd7473ecb787855ecd3de03ff |
| 16.2.2 准备候选 | 旧程序可恢复 | 从官方 Release 下载固定版本归档与 checksums；同时验证 API digest 与 checksums；仅提取归档中的目标普通文件 | 下载命令退出码、SHA256、候选 `version` 输出 | 下载、校验、架构或启动失败则保持旧程序 | 已完成；16000778 字节完整归档双重摘要校验通过，候选 version exit 0 |
| 16.2.3 安装 | 候选校验通过，准确报告 2.15.0 | 在原目录临时落盘后原子替换目标，保留原执行权限 | PATH 解析位置、安装后版本及项目 check-tool-version 退出码 | 安装后校验失败则从备份恢复旧程序；不伪造版本输出 | 已完成；实际 2.15.0、Go 1.27.0，版本门禁 exit 0，权限仍为 0755 |
| 16.2.4 重跑 lint | 实际安装版本满足 2.15.0 | `GOWORK=off GOTOOLCHAIN=local GOPROXY=off make lint`，保留全部输出 | 命令退出码、Go lint/逐脚本 errcheck 实际结果 | 非零先归因，不改无关代码；不降规则或跳过文件 | 已执行；第一次 47 项、限定修复后 22 项，make 均 exit 2；脚本另行检查结果见 16.5 |
| 16.2.5 记录 | 已取得实际结果 | 更新本节与阶段状态；仅在版本阻断确已消除时关闭本任务 ERR-20261023-113 | 回读方案、错误记录、最终工作区差异 | 未完成项不标为通过，ERR-20261023-115 保持未处理 | 已记录；113/116/117/122 已关闭，120/123 的存量阻断与 115 保持 open |

安装准备过程保留真实失败：直连 Release 域名超时（两个请求均为 curl exit 28）；官方 API 单流归档在 60 秒后中断（963382/16000778 字节，exit 28）。下载未完成时曾错误地提前启动校验，校验 exit 1 拒绝不完整文件，该轮未生成候选或安装。错误与纠正记录为 ERR-20261023-116/117。

根据已观测的约每秒 33 KB 速率估算，改用同一官方资产的分段传输：保存首 8000000 字节后停止本任务原续传进程（exit 143）；8000000–11999999 段在 3959850/4000000 字节处超时（exit 28），随后补齐最后 40150 字节（exit 0）；12000000–16000777 段 exit 0。已逐段核对 Content-Range 与长度，按顺序合成；完整归档匹配官方 release API SHA256 和 checksums。未切换第三方来源或关闭 TLS 校验，未执行不完整文件。

已原子替换 `/Users/xiaozhaofu/go/bin/golangci-lint`，安装后 SHA256 为 `e04962742507a5f37a4002a7a8b2b549f6864f17807eb5d534d947f6311a051f`；实际输出 `golangci-lint has version 2.15.0 built with go1.27.0 from 25c5369a on 2026-10-16T11:03:09Z`。项目版本门禁 exit 0；`make lint` 的实际结果见 16.5。ERR-20261023-113/116/117 已按实际处置关闭，cmd 的 ERR-20261023-115 未处理。

若候选或安装后验证失败，只回滚工具文件；lint 的真实源码告警不作为自动回退工具版本的理由。备份保留便于用户复核或恢复。

### 16.3 第一次 lint 与归因

`GOWORK=off GOTOOLCHAIN=local GOPROXY=off make lint` 已返回 exit 2，共 47 项：bodyclose 4、canonicalheader 5、contextcheck 1、errcheck 20、goimports 4、staticcheck 11、whitespace 2。Go lint 失败后 Makefile 尚未执行脚本 errcheck。原始输出为 `lint.log`，问题清单为 `lint-issues.json`。

对照迁移前快照 `/var/folders/_w/xwv4k3t12vn9054592gsk5200000gn/T/gin-api-logger-direct-lqbc1ufo`：25 项由日志迁移引入；其余 22 项位于迁移前已存在的 `internal/pkg/httpclient/propagation_test.go`、`internal/bootstrap/worker_execution_metrics_test.go`、`internal/pkg/metrics/worker_execution_test.go`、`internal/pkg/tasks/execution_metrics_test.go`。快照的 `status-before.txt` 已列出这四个文件；propagation_test 的迁移差异仅为 logger import/ContextWithRequestID，所报问题行均已存在；bootstrap 指标测试与快照逐字相同。不按单次提交包含的全部文件推定归属。

### 16.4 仅修复日志迁移引入的 25 项

沿用第 15 节用户已明确的“只修复本次日志迁移引入的问题”授权；本次工具升级用于完成该受控检查，不扩展到四个存量问题文件或 cmd 超时。修复仅限已有测试，生产调用、SDK 使用方式、依赖、配置、接口、存储和目录架构不变。

| 步骤 | 前置条件与具体范围 | 改动 | 检查、停止条件与交付物 | 状态 |
|---|---|---|---|---|
| 16.4.1 保存 | 已逐项对照 lint 输出、迁移前快照和最新源码 | 保存下述 15 个测试文件的修改前副本、四个存量文件的 SHA256 | 备份与文件清单可回读；不能确认归属则不修改 | 已完成，source-before-lint-fix/ 与 unrelated-source-hashes.json |
| 16.4.2 错误检查（8 项） | `internal/bootstrap/logger_shutdown_test.go`；`internal/middleware/native_logger_test.go`；`internal/pkg/{console,httpcache,response}/native_logger_test.go`；`internal/platform/data/native_logger_test.go` | 给 io.Copy、Body.Close、Builder.Write 和初始化失败后的文件 Close 增加测试错误报告；保留原测试流程与资源关闭顺序 | 不忽略真实 I/O 错误；不修改 logger 调用或引入封装 | 已完成，对应告警全部消失 |
| 16.4.3 冗余格式化（11 项） | `internal/bootstrap/alerting_test.go`、`internal/bootstrap/notification_test.go`、`internal/platform/logging/notification_test.go` | 仅把无参数 `fmt.Sprintf` 的固定字符串直接传给 logger.HError，移除已无用途的 fmt import | 保留消息原文和通知断言，保留真正需要格式化的调用 | 已完成，对应告警全部消失 |
| 16.4.4 格式（6 项） | `internal/bootstrap/{assembly_contract,lifecycle_contract}_test.go`、`internal/router/{fixture,middleware_construction}_test.go`；`internal/pkg/console/console_test.go`、`internal/platform/logging/owner_test.go` | 归位迁移加入的 imports，删除迁移删除语句后遗留的两处空行，仅格式化这 15 个文件 | 不自动格式化或修改其他文件 | 已完成，gofmt exit 0，对应告警全部消失 |
| 16.4.5 复验 | 上述局部补丁完成 | 重跑 make lint；运行上述 8 个受影响包普通测试（`-vet=off -tags scaffold -count=1 -timeout=3m`）；Go lint 若被存量告警阻断，另按 Makefile 对 scripts/*.go 逐个执行 `golangci-lint run --enable-only=errcheck`，保留 make 的失败结论 | 不跑 cmd、全量 race/覆盖率；存量告警只记录，不降低规则或跳过文件；同一修复三轮失败则停止 | 已执行，8 包普通测试通过；全仓 lint 与脚本入口仍因存量问题失败 |
| 16.4.6 收尾 | 已有实际输出 | 更新最终结果与剩余告警，核对四个存量文件未变，回读方案 | 有剩余问题不得标为全仓 lint 通过；只关闭真实解决的错误 | 已完成，四个文件 SHA256 相同；未改任何生产 Go 文件或无关代码 |

本次修复风险仅为测试内错误报告新增失败分支；已通过相关普通测试验证。若局部补丁导致不相关行为或无法确认结果，按逐文件备份回退本次补丁，不撤销旧迁移或其他任务改动。此节在源码修改前落盘并回读；实际补丁仅涉及 15 个测试文件，40 行新增、28 行删除。

### 16.5 最终实际结果与证据

cwd：`/Users/xiaozhaofu/go/src/Project/gin_api`；所有 Go/lint 检查均使用 `GOWORK=off GOTOOLCHAIN=local GOPROXY=off`。本轮未重新执行独立 vet、modern、race、覆盖率，也未运行 cmd 测试。

| 执行 | 退出码与结果 | 证据（.cache/logger-lint-upgrade-20261023/） |
|---|---|---|
| 候选及安装后 `golangci-lint version`、项目 `check-tool-version.sh golangci-lint 2.15.0` | 均 exit 0，安装版本准确为 2.15.0 | integrity.json、install.json、version-candidate.txt |
| 第一次 `make lint` | exit 2，47 项，版本门禁已通过 | lint.log |
| 仅修复日志迁移新增问题后 `make lint` | exit 2，剩余 22 项：bodyclose 4、canonicalheader 5、contextcheck 1、errcheck 12；本次 25 项全部消失 | lint-after-fix.log、final-verification.json |
| `go test -vet=off -tags scaffold -count=1 -timeout=3m ./internal/bootstrap ./internal/middleware ./internal/pkg/console ./internal/pkg/httpcache ./internal/pkg/response ./internal/platform/data ./internal/platform/logging ./internal/router` | exit 0，8 个包全部通过 | tests-after-fix.log |
| 按 Makefile 同样的单文件参数逐个执行 `golangci-lint run --enable-only=errcheck scripts/<file>.go` | 前 3 个 exit 0；第 4 个 check-repository-context_test.go exit 1、4 处 typecheck 错误；其后 clean.go/security-eval.go 未运行 | script-errcheck.log、script-errcheck-results.json |
| 最终范围核对 | 15 个变更 Go 文件均为本次测试；4 个存量告警文件 SHA256 未变；go.mod/go.sum、.golangci.yml、Makefile、相关脚本及 cmd 无差异 | final-verification.json、工作区 diff |

脚本失败归因：现有入口逐个传入 `.go` 文件，`check-repository-context_test.go` 需要同目录 `check-repository-context.go:51` 的 `repositoryContextFindings`，单独加载时未定义。迁移前状态已列出这两个脚本，本轮未改脚本/入口或通过改参数制造全绿。记录为 ERR-20261023-123（open）。全仓剩余 22 项与限定修复记录为 ERR-20261023-120（open）；一次补丁上下文不匹配已纠正并关闭 ERR-20261023-122。

本轮结果：工具版本升级成功，日志迁移新增告警已修复，相关普通测试通过；**全仓 lint 和脚本 lint 尚未通过**。cmd 既有超时明确暂不处理。旧工具备份保留在 `golangci-lint-2.14.0.backup`；未提交、推送或发布。所有实际状态与未执行项均按以上证据记录，未把第 15 节历史 race/覆盖率结果当作本次修复后的重新验证。

## 17. 剩余 22 项告警与脚本 lint：已批准的修复方案

### 17.1 本轮授权、依据与范围

本节制定时的用户要求：

> 把剩余 22 项存量告警和脚本 lint 失败的具体原因、涉及文件及建议修复方式列出来，更新到方案里给我确认，先别改代码，cmd 的既有超时仍不处理。

本节最初仅制定方案：当时已读取第 16 节留存的真实 lint 输出、四个测试文件、相关生产契约、Makefile 与两个配套脚本，未修改代码或运行检查。用户随后批准 P1–P5，实施授权及实际结果见第 17.6 节；以下原因与建议保留为原方案依据，不能单凭方案视为 lint 已通过。

下表及第 17.2–17.4 节的行号均为方案制定时的原行号；检查结果来自上一轮 `lint-after-fix.log` 与 `script-errcheck.log`，不是实施后重跑。当前锁定 Go 为 1.27.3，OpenTelemetry 为 1.47.0，golang.org/x/tools 为 0.52.0，golangci-lint 为 2.15.0；本次未升级或新增依赖。

| 文件 | 现有告警数 | 建议修改范围 |
|---|---:|---|
| `internal/pkg/httpclient/propagation_test.go` | 11 | HTTP 替身响应与关闭、Header API 字面量、trace 提取的父 context、测试 fixture 解析错误 |
| `internal/bootstrap/worker_execution_metrics_test.go` | 2 | 指标数据的受检类型断言 |
| `internal/pkg/metrics/worker_execution_test.go` | 6 | 指标类型断言及预期 panic/Goexit 的返回值处理 |
| `internal/pkg/tasks/execution_metrics_test.go` | 3 | 指标类型断言及预期 panic 的返回值处理 |
| `Makefile` | 独立的脚本入口问题 | `tool` 目标中按独立脚本及其配套测试组合检查 |

22 项均位于测试文件：bodyclose 4、canonicalheader 5、contextcheck 1、errcheck 12。其中 12 项 errcheck 是 **8 处未检查的类型断言、3 处预期异常控制流中被丢弃的 error、1 处 TraceState 解析 error**，不是 12 处生产调用错误。脚本的 4 条 typecheck 诊断另计，不包含在 22 项内。

### 17.2 HTTP 请求传播测试：11 项

文件：`internal/pkg/httpclient/propagation_test.go`。

| ID | 行号 / 规则 | 具体原因 | 建议修复方式 |
|---|---|---|---|
| R01 | 69 / bodyclose | `RoundTrip` 的响应被 `_` 丢弃，分析器看不到 Body 关闭路径。该用例的替身实际返回 `(nil, cause)`，不是已证实发生了真实 Body 泄漏。 | 显式接收 response 与 err；若意外得到非 nil Body，先关闭并检查关闭错误，再断言 response 为 nil 且 `errors.Is(err, cause)`。保留原错误传播语义。 |
| R02 | 92 / bodyclose | 成功路径丢弃响应；其替身在第 90 行返回 `(nil, nil)`，没有构造符合成功路径语义的 HTTP 响应。 | 替身返回 `StatusCode: http.StatusNoContent, Body: http.NoBody` 的非 nil 响应；调用方检查 err、response/Body，并显式关闭 Body、检查关闭错误。保留无关联 context 时调用方 header 不变的断言。 |
| R03 | 116 / bodyclose | 与 R02 相同，第 114 行替身返回 `(nil, nil)`，调用方未处理响应 Body。 | 构造同样的最小合法响应并显式关闭；保留六个原始大小写键未变、输出只有三个关联键的断言。 |
| R04 | 136 / bodyclose | 并发用例的第 130 行替身返回 `(nil, nil)`，32 次调用均丢弃响应。 | 每次返回最小合法响应，并在对应 goroutine 内检查、关闭 Body；错误用 `t.Errorf` 加 return 报告，不在该 goroutine 中调用 `t.Fatal`。保留 32 个 request ID 不串值及原请求未变的断言。 |
| R05 | 46 / canonicalheader | `Header.Set("traceparent", ...)` 使用未规范化的 Header API 字面量。 | 改为 `"Traceparent"`；值 `old-trace` 不变。 |
| R06 | 47 / canonicalheader | `Header.Set("tracestate", ...)` 使用未规范化的 Header API 字面量。 | 改为 `"Tracestate"`；值 `old=value` 不变。 |
| R07 | 72 / canonicalheader | `Header.Get` 使用小写字面量；本行同时读取 traceparent、tracestate，lint 输出以本行第一处定位。 | 本行两处分别改为 `"Traceparent"`、`"Tracestate"`，保留原始 header 未被改写的断言。 |
| R08 | 87 / canonicalheader | 对收到的 header 与原 header 调用 `Get("traceparent")`，同一行有两处。 | 两处均改为 `Get("Traceparent")`，保留两边值相等的比较。 |
| R09 | 188 / canonicalheader | 真实 HTTP client 用例调用 `Get("traceparent")`。 | 改为 `Get("Traceparent")`，保留实际服务端收到关联 header 的断言。 |
| R10 | 58 / contextcheck | 第 56 行用独立的 `t.Context()` 提取 header，再使用提取结果；检查器将其判为未继承当前请求 context 的来源。 | 从 `got.Context()` 派生提取用 context，但先用 `trace.ContextWithSpanContext(got.Context(), trace.SpanContext{})` 清空已有 span，再传给 `TraceContext.Extract`。保留请求 context 身份和取消传播断言，并增加缺失/非法 traceparent 时提取结果无效的断言。 |
| R11 | 31 / errcheck | `correlationContext` 将 `trace.ParseTraceState("vendor=value")` 的 error 丢给 `_`。固定输入不等于可以绕过已启用的错误检查。 | 为这个私有测试 fixture 加 `testing.TB` 参数与 `t.Helper()`，显式检查解析错误；仅在测试主 goroutine 调用。更新本文件第 42、100、135、183 行四处使用，其中并发用例先在循环中构造 context，再交给 `wg.Go`，避免 fixture 内的 `t.Fatal` 在工作 goroutine 执行。 |

必须保留的细节：

- R05–R09 只规范化 Header.Set/Get 的参数。第 101–104 行直接构造 map 的 `x-request-id`、`traceparent`、`TRACESTATE` 是刻意覆盖重复大小写键的反例，不改成统一大小写。
- R10 不能直接把第 56 行替换成 `Extract(got.Context(), ...)` 就结束。已读取的 OTel 1.47.0 源码表明：header 无效时 `Extract` 直接返回传入的父 context；而 `got.Context()` 已带期望的 span，这样可能在 header 根本没传播时仍通过测试。先清空 span 可保留取消与其他 context 值，同时使断言真正依赖 header。
- R01 的 `(nil, cause)` 是有效错误用例，保留；R02–R04 才改为成功响应。只修测试替身与资源处理，不改 `propagationTransport.RoundTrip` 的生产行为。

### 17.3 指标与异常控制流测试：11 项

`.golangci.yml:84–90` 已启用 `check-type-assertions: true`、`check-blank: true`，所以 `value.(T)` 和 `_ = 返回error的方法()` 都会被 errcheck 报告。建议遵守现有规则，不关闭检查器或增加路径排除。

| ID | 文件 / 行号 | 具体原因 | 建议修复方式 |
|---|---|---|---|
| R12 | `internal/bootstrap/worker_execution_metrics_test.go:47` | 直接断言 `metric.Data.(metricdata.Sum[int64])`，类型变化时 panic，未检查 ok。 | 先接收 Sum 与 ok；不匹配用 `t.Fatalf` 输出指标名与实际 `%T`，再遍历 DataPoints。 |
| R13 | 同文件 :94 | 同 R12，第二个 worker 启动路径断言未检查。 | 同样显式检查类型；保留 required/optional 各一次、cleanup 各一次的断言。 |
| R14 | `internal/pkg/metrics/worker_execution_test.go:35` | worker.executions 的 Sum 类型断言未检查。 | 先保留采集结果，再用 comma-ok 断言；失败明确报告类型，不跳过该指标。 |
| R15 | 同文件 :36 | worker.execution.duration 的 Histogram 类型断言未检查。 | 显式检查 `metricdata.Histogram[float64]`；保留序列数量与 2 秒时长断言。 |
| R16 | 同文件 :73 | 预期 callback panic 的路径仍写成 `_ = recorder.RunWorker(...)`。 | 显式接收并检查返回 error；保留 defer/recover 对原 panic 对象的比较。若调用意外正常返回，也判测试失败；不在生产 RunWorker 中吞掉 panic。 |
| R17 | 同文件 :103 | 32 次并发执行后的 Sum 类型断言未检查。 | comma-ok 检查后继续验证 calls、DataPoints 长度和计数均为 32。 |
| R18 | 同文件 :115 | 预期 `runtime.Goexit()` 终止 goroutine，却直接丢弃 RunWorker 的 error。 | 写出对意外返回 error 的检查；若调用正常返回 nil 也用 `t.Error` 判失败。保留 `defer close(done)`、等待 done 和后续指标断言，不使用会作用于错误 goroutine 的 `t.Fatal`。 |
| R19 | 同文件 :121 | Goexit 后采集 Sum 的类型断言未检查。 | comma-ok 检查后取 DataPoints；保留一次记录及 `worker.outcome == "panic"` 的既有契约。 |
| R20 | `internal/pkg/tasks/execution_metrics_test.go:50` | queue 执行计数的 Sum 类型断言未检查。 | 显式检查 Sum；保留单一序列、三个标签、consumer/failure 标签和两次尝试计数。 |
| R21 | 同文件 :61 | queue 时长 Histogram 类型断言未检查。 | 显式检查 Histogram 再遍历；保留 durationCount == 2。 |
| R22 | 同文件 :83 | 预期 handler panic，却直接丢弃 `wrapped.ProcessTask(...)` 的 error。 | 显式检查意外返回的 error，保留 recover 对原 panic 对象的比较；正常返回 nil 也必须失败。不修改 production middleware 的 panic 传播。 |

这 8 处受检类型断言不建议抽成新的公共工具包；在各测试内就地写出类型和失败信息即可。不要用类型不符时 continue 或返回零值来消除告警，否则会削弱测试。panic/Goexit 路径的目标仍是原控制流，不把它们改成普通成功或 error 返回路径。

### 17.4 脚本 lint 失败：一个入口原因，四条 typecheck 诊断

实际来源是 `Makefile:124–126`：它对 `scripts/*.go` 的每个文件分别执行 `golangci-lint run --enable-only=errcheck "$source"`。

| 位置 | 已确认原因 | 建议处理 |
|---|---|---|
| `scripts/check-repository-context_test.go:75、117、134、142` | 该测试文件四次引用 `repositoryContextFindings`；单独传入测试文件时，没有加载定义所在的配套源码。 | 与 `scripts/check-repository-context.go` 作为同一个编译单元交给 lint。保留现有测试与全部四处断言。 |
| `scripts/check-repository-context.go:51` | 函数已经存在，不是缺少实现；该源码文件单独 lint 已通过。 | 不补一个同名假函数，不复制定义到测试，也不修改检查器算法。 |
| 两个文件第 1 行 | 均带 `//go:build ignore`，普通 `./...` 检查不会自动覆盖这组独立脚本；当前入口用显式文件参数补充覆盖。 | 保留 ignore 标签及显式脚本检查，不通过删除 build tag 改变包布局。 |
| `Makefile:124–126` | 通配符把配套 `_test.go` 当作独立程序检查。其余脚本也都为带 main 的独立程序，不能把整个 `scripts/*.go` 一次性交给 lint，否则会形成多个 main 的冲突。 | 按“非 `_test.go` 主文件 + 同名 `_test.go`（存在时）”分组；不同独立脚本继续分开运行，失败仍立即返回非零。对没有配套主文件的 `_test.go` 明确报错，不能静默漏检。 |

建议 Makefile 检查分组如下；每个现有文件恰好覆盖一次：

| 编译单元 | lint 参数中的文件 | 上轮实际状态 |
|---|---|---|
| 文档引用检查器 | `scripts/check-doc-refs.go` | 单文件通过 |
| 包变量检查器 | `scripts/check-package-vars.go` | 单文件通过 |
| repository context 检查器及测试 | `scripts/check-repository-context.go` + `scripts/check-repository-context_test.go` | 主文件单独通过；测试单独失败；组合尚未执行 |
| 清理工具 | `scripts/clean.go` | 因前项失败，尚未执行 |
| 安全评估工具 | `scripts/security-eval.go` | 因前项失败，尚未执行 |

建议只修改 Makefile 的文件组合方式，不新增目录、公共封装、模块或依赖；也不预先修改后三个编译单元的源码。组合后的 lint 结果及最后两项结果目前未知，不能提前宣称全部通过。

### 17.5 已批准实施顺序、风险、回滚与验收

建议实施范围为上述 **4 个测试文件 + Makefile**。生产 HTTP/指标/队列实现、脚本检查器算法、go.mod/go.sum、.golangci.yml、目录结构和已有 15 个迁移修复文件均不在新增修改范围。原 request ID、header 不变性、32 路并发隔离、panic 身份、Goexit、指标标签和重试次数断言必须保留。

| 阶段 | 前置条件 | 具体工作 | 预期证据 / 停止条件 | 当前状态 |
|---|---|---|---|---|
| P1 复核与快照 | 用户明确确认实施本节 | 重读本节、最新错误记录、源码和工作区差异；保存 5 个文件的当前副本 | 原件与差异可复核；文件/告警已变化时先核对，不覆盖其他任务 | 已完成；5 个目标文件快照、15 个既有 Go 改动 SHA256 已保存 |
| P2 HTTP 测试 | P1 一致 | 按 R01–R11 修正 fixture、响应关闭、Header API、context 来源与解析错误 | 11 项逐项有对应补丁；不弱化大小写反例或 trace/header 断言 | 已完成；普通回归通过，P6 补正 context 静态来源后 lint 通过 |
| P3 指标测试 | P1 一致 | 按 R12–R22 补受检类型断言与异常返回分支 | 11 项逐项有对应补丁；panic/Goexit/计数契约不变 | 已完成；三个相关包普通回归与 P6 lint 通过 |
| P4 脚本入口 | 已核对现有 6 个脚本文件的归属 | 在 Makefile 按上表组成 5 个独立编译单元；检查孤立测试文件；保留失败传播 | 不漏掉测试、不合并多个 main；发现额外脚本或依赖变化则先补充明确分组 | 已完成；真实 make lint 的 5 组脚本全部通过 |
| P5 功能回归 | 用户已授权实施及相关普通回归 | 运行四个受影响包的普通测试（`-vet=off`）；显式文件方式运行 repository context 检查器的配套测试 | 保留原业务断言；新增无效/missing traceparent 检查和意外正常返回失败路径有真实证据；任一失败先归因，不改无关逻辑 | 已完成；基线、修改后普通回归均 exit 0，7 个反证均得到预期失败，最终范围核对通过 |
| P6 受控 lint 与记录 | P2–P5 功能完成，且用户另行明确授权 lint | 运行原 `make lint` 完整入口，记录全仓结果和 5 个脚本编译单元结果 | 22 项旧诊断消失、4 条脚本 typecheck 消失且 6 个文件均被覆盖，方可报告 lint 通过；若出现新告警先限定归因，不擅自扩大范围 | 已完成；首轮 exit 2、两条 contextcheck，经限定修复后 exit 0、六条 0 issues；见第 17.7 节 |

主要风险及处理：R10 若保留父 context 中的期望 span 会造成假通过；R11 若在工作 goroutine 内调用 FailNow 会改变测试控制流；R16/R18/R22 若只检查 error 而接受正常返回，会失去对 panic/Goexit 的约束；脚本分组若只跳过 `_test.go` 会减少检查覆盖。按上文的具体处理与验收避免这些变化，不以禁用规则或忽略错误作为修复。

若实施后需要回滚，只恢复 P1 保存的这 5 个文件的本次修改，不回滚已完成的日志迁移、2.15.0 工具升级或其他任务。发现当前原件与快照之外的并行修改时停止恢复并说明差异，禁止整仓 reset/clean。

**上述范围已获批准，P6 已在功能完成后单独获准并执行。cmd 的既有超时不处理，其他受控检查仍未执行。**

### 17.6 P1–P5 实施授权与执行记录（P6 结果见 17.7）

用户已明确批准：

> 可以，按第17节方案实施，修复剩余22项告警和脚本lint入口，只改这4个测试文件及Makefile，保留原有测试语义，cmd的既有超时不处理。完成后更新方案并汇报，lint等受控检查再单独问我。

因此 P1–P5 已获准；P6 仍未获准，不运行实际 lint、脚本 lint、vet、modern、race、覆盖率或聚合入口。只允许改动以下代码文件，另更新本文与必要的项目错误记录，验证产物保存在忽略目录：

- `internal/pkg/httpclient/propagation_test.go`
- `internal/bootstrap/worker_execution_metrics_test.go`
- `internal/pkg/metrics/worker_execution_test.go`
- `internal/pkg/tasks/execution_metrics_test.go`
- `Makefile`

实施前读取的新 harness 会话契约要求原生请求状态。以运行端 `CODEX_THREAD_ID` 取得的真实会话 `01a166ec-68c6-7b50-add3-73ccf788d436` 调用 workflow.py status，实际 exit 1：`no request state for this session; submit the current user request first`；未伪造或导入状态。用户随后明确回复 **“允许仅本次跳过，按第 17 节继续”**，授权本次跳过会话工作流及 OpenSpec 门禁；该例外不扩展文件范围，也不授权任何受控检查。

P1 已保存五个目标文件及既有改动的 SHA256；普通基线/回归使用本机 Go 1.27.3 darwin/amd64、`GOWORK=off GOTOOLCHAIN=local GOPROXY=off`，四个包测试带 `-vet=off -tags scaffold -count=1 -timeout=3m`，脚本配套测试使用显式两个文件并带 `-vet=off -count=1 -timeout=3m`。Makefile 仅核对实际源码与 dry-run 展开，不执行其中的 lint 命令；新入口真实检查留到 P6 获准后。

五个目标文件的补丁已完成。HTTP 测试处理全部四处响应 Body、五处 Header API 诊断、继承请求 context 并清空 span，以及 fixture 的解析错误；保留原始大小写 map 反例，将并发 fixture 构造移到测试主 goroutine。指标测试的八处类型断言均检查 ok，三处预期 panic/Goexit 调用明确拒绝意外正常返回。Makefile 在 lint 主循环前拒绝孤立 `_test.go`，再按五组显式文件调用，保留原版本门禁、errcheck 和非零退出传播；未改脚本实现或生产日志调用。

P5 的异常返回反证使用 Go 已有 `-overlay` 能力：只把上述测试文件的临时变体存入 `.cache/logger-remaining-lint-20261023/`，分别让预期 panic/Goexit 的回调返回 nil 或 error，以及取消提取前清空 span。七次对应单测全部 exit 1 且命中预期失败信息，这是反证成功，不是工作区测试失败。该反证未写生产文件或工作区测试文件，未执行任何受控检查。

实际命令与结果（本轮执行，不沿用历史结果）：

| 操作 | 实际结果 | 证据文件（相对 `.cache/logger-remaining-lint-20261023/`） |
|---|---|---|
| 保存原件与初始状态 | 五个目标副本、目标 SHA256、既有 15 个 Go 改动 SHA256 已保存 | `before/`、`manifest-before.json`、`status-before.txt` |
| 四个包的修改前普通测试 | exit 0，四包通过 | `baseline-packages.log` |
| repository context 配套测试基线 | exit 0 | `baseline-script.log` |
| `gofmt -w` 四个目标测试文件 | exit 0；未格式化其他 Go 文件 | 最终 diff、`scope-verification.json` |
| 四个包的修改后普通测试 | exit 0；httpclient 2.125s、bootstrap 7.587s、metrics 1.487s、tasks 2.546s | `tests-after-fix.log` |
| repository context 配套测试回归 | exit 0，0.954s | `script-tests-after-fix.log` |
| `make -n tool` | exit 0，只展开命令；实际 lint 未执行 | `make-tool-dry-run.log` |
| 七个测试文件 overlay 反证 | 各 exit 1，均命中预期断言；反证驱动 exit 0 | `negative-case-manifest.json`、`negative-case-results.json`、各同名 `.log` |
| 最终范围核对 | 五个目标发生预期变化；既有 15 个 Go 改动 SHA256 不变；四文件原测试函数名全部保留；Makefile 仅本轮脚本循环变化 | `scope-verification.json`、`makefile-this-task.diff` |

普通测试完整命令，修改前后各执行一次：

```sh
GOWORK=off GOTOOLCHAIN=local GOPROXY=off go test -vet=off -tags scaffold -count=1 -timeout=3m ./internal/pkg/httpclient ./internal/bootstrap ./internal/pkg/metrics ./internal/pkg/tasks
GOWORK=off GOTOOLCHAIN=local GOPROXY=off go test -vet=off -count=1 -timeout=3m scripts/check-repository-context.go scripts/check-repository-context_test.go
```

反证完整命令已逐项写入 `negative-case-results.json`；统一参数为 `go test -vet=off -tags scaffold -count=1 -timeout=30s -overlay <该例 overlay.json> -run <对应测试名> <对应包>`，同样设置上述三个环境变量。

| 反证场景 | 必须观察到且实际出现的失败信息 |
|---|---|
| 不清空继承的 span | `missing traceparent inherited the original span` |
| worker 预期 panic，回调返回 nil / error | `RunWorker returned instead of panicking: <nil>` / `original failure` |
| worker 预期 Goexit，回调返回 nil / error | `RunWorker returned instead of exiting goroutine: <nil>` / `unexpected return` |
| queue 预期 panic，回调返回 nil / error | `ProcessTask returned instead of panicking: <nil>` / `handler panic` |

范围与未验证项：

- `cmd`、`scripts`、go.mod/go.sum、`.golangci.yml` 无工作区 diff；生产代码未新增修改。原有 15 个日志迁移测试改动均保留。
- Makefile 的既有 `port` / `kill-port` 重复目标在 dry-run 中产生覆盖警告，已与实施前副本比对，本轮未新增、未修改这部分内容。
- 脚本六个文件已静态核对为五组且各覆盖一次；dry-run 和普通脚本测试不等于实际脚本 lint，孤立文件拒绝及非零退出传播本轮仅回读核对。
- lint、脚本 lint、vet、modern、全量 race、覆盖率和聚合检查全部未执行；旧 22 项及脚本 typecheck 是否已消失仍待 P6。cmd 既有超时未重跑、未修复。
- ERR-20261023-120 / 123 追加本轮实施结果，但因尚未执行 lint 保持 open；准备阶段的路径检索错误已记录并纠正（128 closed），原生会话状态缺失仅获一次例外（129 open），不声称工作流已修复。
- 本轮 P1–P5 已完成；无提交、推送、安装、Docker 或发布操作。下一步单独请求 P6 的 `make lint`（含脚本 lint）授权。

### 17.7 P6 lint 单独授权与执行记录

用户在 P1–P5 完成后明确批准：

> 可以，执行 make lint（包含脚本 lint），只修复本次改动引入的问题，不改无关代码，cmd 的既有超时仍不处理。完成后把实际结果更新到方案里并汇报，其他受控检查先不执行。

本轮仅执行项目原有 `make lint`：保留锁定的 golangci-lint 2.15.0、现有配置和五组脚本参数，不运行独立 vet、modern、全量 race、覆盖率、`make check` 或其他聚合门禁。沿用同一第 17 节任务已取得的一次性工作流/OpenSpec 例外，不伪造原生状态或扩展授权。Makefile 分类拆分仅答复建议，本轮不实施目录、文件分拆或目标迁移。

执行前已回读计划、错误记录和实际 Makefile，核对五个目标文件与 P5 交付 SHA256 一致，并保存当前源码/配置哈希。若发现本次补丁引入的新诊断，只在原四个测试文件及 Makefile 内处理；若属于存量、其他并行改动或需要扩大范围，则记录事实并停止对应修复。实际命令为 `GOWORK=off GOTOOLCHAIN=local GOPROXY=off make lint`，本轮共执行两次，均保留完整输出和退出码。

P6 首次执行的实际结果：`make lint` exit 2，全仓部分仅剩 `internal/pkg/httpclient/propagation_test.go:63、75` 的两条 contextcheck，脚本循环尚未执行。其他原诊断未再出现，不将这次失败报告为 lint 通过。

这两条来自本次 R10 补丁：实际二进制依赖的 contextcheck v1.1.6 会把捕获 `ctx` 的闭包按 EntryWithCtx 分析，此模式只追踪传入/捕获的 context，不继承 `got.Context()` 的静态来源；源码中的请求 context 身份断言不参与其静态分析。已读检查器 `checkIsCtx`、`checkIsEntry`、`collectCtxRef` 和 `checkFuncWithCtx` 确认该分支。先记录错误并落盘限定修复方案，再将清空 span 的来源改为闭包中已捕获的 `ctx`；原有 `got.Context() != ctx` 断言、清空 span、缺失/非法 header 反例和取消断言全部保留。两者通过身份断言保证是同一个 context；未增加包装、nolint 或配置豁免。随后完成 httpclient 包普通回归（`-vet=off`）、这一处的 trace 反证和第二次 `make lint`。

本轮环境：本机 Go 1.27.3 darwin/amd64，golangci-lint 2.15.0（以 Go 1.27.0 构建）。完整证据目录：`.cache/logger-lint-p6-20261023-q9nc9trz/`。

| 实际操作 | 退出码及结果 | 证据文件（相对上述目录） |
|---|---|---|
| 第一次 `GOWORK=off GOTOOLCHAIN=local GOPROXY=off make lint` | exit 2；仅两条 contextcheck；脚本未执行 | `make-lint.log`、`make-lint-result.json` |
| `GOWORK=off GOTOOLCHAIN=local GOPROXY=off go test -vet=off -tags scaffold -count=1 -timeout=3m ./internal/pkg/httpclient` | exit 0，1.615s | `httpclient-after-context-fix.log` |
| 用临时测试 overlay 去掉清空 span，再运行 `TestPropagationPreservesOriginalRequestAndContext` | 目标测试 exit 1，命中 `missing traceparent inherited the original span`；反证驱动 exit 0 | `trace-parent-fallback.log`、`trace-parent-fallback-result.json` |
| 第二次相同 `make lint` | **exit 0；全仓和 5 组脚本合计六次 `0 issues.`** | `make-lint-after-fix.log`、`make-lint-after-fix-result.json` |
| 444 个已跟踪源码/配置文件的 SHA256 对比 | 仅 `internal/pkg/httpclient/propagation_test.go` 改变；本轮 Makefile、其他 Go 文件、依赖、配置不变 | `hashes-before.json`、`final-verification.json`、`source-change.diff` |

脚本覆盖按原入口的循环顺序、遇错退出行为、六条 `0 issues.` 及最终成功标记确认，不是另外换参数运行的结果：

| 编译单元 | 本次实际 lint 结果 |
|---|---|
| 全仓 `./...`（保留配置中的 sonic/scaffold 标签） | 0 issues |
| `scripts/check-doc-refs.go` | 0 issues |
| `scripts/check-package-vars.go` | 0 issues |
| `scripts/check-repository-context.go` + `scripts/check-repository-context_test.go` | 0 issues，原四条 typecheck 消失 |
| `scripts/clean.go` | 0 issues |
| `scripts/security-eval.go` | 0 issues |

结论：22 项原诊断及此次补丁引入的两条 contextcheck 均已消失，脚本入口完整通过。关闭已验证的 ERR-20261023-120、123、131。lint 输出仍包含既有的无匹配排除规则提示，以及 Makefile 中 `port` / `kill-port` 重复目标警告；这些不是此次改动引入，未修改相关代码或配置。

未执行独立 vet、modern、全量 race、覆盖率及其他聚合检查；`.golangci.yml` 内已有的 govet 分析器属于获准的 `make lint`，不能当作独立 `go vet` 已执行。cmd 仅随全仓 lint 做静态分析，既有超时测试未重跑、未修复。P1–P6 完成不代表项目全部质量门禁或生产验收通过。

关于 Makefile 分类：当前 423 行已混合开发、构建、检查、迁移、发布与脚手架职责，建议后续按这些职责拆成少量 `.mk`，由根 Makefile 显式 `include`，保留公共变量和聚合入口、现有命令名与默认目标；现有 `project.mk` / `security/toolchain.mk` 继续沿用，复杂流程留在 `scripts/`。这只是本轮问题的建议，未建立拆分计划、未新增子文件、未移动目标或改变目录。
