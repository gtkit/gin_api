# 直接使用 gtkit/logger：完整方案与实施计划

日期：2026-10-23（Asia/Taipei）  
修订：第 2 版，替代上一版全部实施设计。  
状态：**D1–D3 已确认，阶段 1–3 已实施并完成本次功能验收。lint、脚本 lint、vet、modern、全量 race、覆盖率等受控检查仍未授权。**

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

编译、mock、文件测试不能证明真实通知送达、生产磁盘持久性或容量。本节为验收目标；本次真实执行结果见第 14 节，未授权检查与生产送达/容量仍未验证。

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
| 4.1 受控检查 | 全部功能完成后，用户另行明确批准具体检查 | 只运行获准的 lint/脚本 lint/vet/modern/全量 race/覆盖率 | 工具版本、完整命令、exit 与原始输出 | 用户停止、工具阻断、无关失败；不绕过 | 未授权、未执行 |
| 4.2 收尾 | 已有 4.1 实际结果 | 仅修本次问题并在原授权范围复验、记录 | 定向补丁及对应重跑结果 | 需要扩大范围则先说明 | 未授权、未执行 |

本轮已明确授权按方案实施阶段 1–3，D1–D3 也已确认；阶段 4 须等全部功能完成后另行询问，不自动运行。

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
