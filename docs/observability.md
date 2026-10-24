# 可观测性

## 日志

- 字段 key 稳定，用 snake_case：`user_id`、`request_id`、`duration_ms`。
- 错误日志带操作上下文，不只打印 `err`。
- 热路径避免频繁 Info；异常路径用 Warn/Error。
- **禁止打印密码、token、完整 API key、完整请求体、签名密钥。**
- 对外错误文案经 response 层映射，底层错误只进日志。

日志栈是 `github.com/gtkit/logger/v2`（沿用 go.mod 锁定版本）。
`internal/platform/logging` 仅负责构造和关闭 main（不缓冲）及 access（缓冲）实例。
main 直接调用 SDK 包级函数，结构化字段使用 `zap.Field`；access 由 bootstrap 经 router
装配参数传入 `GinLogger`，中间件直接调用原生 `*logger.Logger`，不另设日志门面。

main 和 access 共用按日期及文件大小轮转的策略（`division: both`），均使用
`log.maxsize`、`maxage`、`maxbackups` 和 `compress`。`maxsize` 的单位是 MiB；
`maxage`、`maxbackups` 为 0 时保留 SDK 默认值，不表示无限保留。

```go
logger.Info("user created", zap.Int("user_id", userID))
logger.ErrorCtx(ctx, "query failed", zap.Error(err))
```

**application 层禁止 import 任何 logger 包**——需要记录时定义 port，由 runtime 层注入实现
（如 `user` 模块的 `Auditor`）。业务逻辑绑定具体日志库，测试就必须连带准备日志环境，
而换一个库会横扫整个业务层。

### 脱敏

`internal/middleware/redact.go` 负责访问日志与 panic 日志的脱敏。敏感键分两组：

- **精确匹配**：`code` 这类短名，子串匹配会误伤 `zipcode`、`areacode`；
- **子串匹配**：`password`、`token` 这类，变体太多无法穷举。

请求体日志默认关闭；开启时前缀有界——大文件与流式响应的内存成本必须与响应总量解耦，
否则一个大上传就能让日志写爆内存。

## 指标

业务 recovery 位于访问日志、指标和 trace 的内层，先形成最终响应再完成观测后处理；
最外层另保留 recovery，兜住基础中间件自身异常。业务 handler 的 panic 必须记录
一次访问日志和一次 HTTP 请求指标；响应已提交时保留实际提交的状态。

新增外部调用、worker、缓存关键路径时考虑埋：

- 请求/任务总数：success / error / timeout / canceled
- 耗时分布
- 队列积压、租约超时、重试次数
- 缓存 hit / miss / backfill / delete_failed

母版已埋点：HTTP 请求量/时延/状态、限流判定（按策略与拒绝原因）、worker 单次执行量/耗时、
连续失败与最后成功时间、DB 连接池、认证事件。

### Worker 单次执行

| 指标 | 类型 / 单位 | 计量边界 |
|---|---|---|
| `worker.executions` | Counter / 次 | 每次实际执行结束计一次 |
| `worker.execution.duration` | Histogram / 秒 | 同一次执行从进入任务函数到返回或异常退出的耗时 |

两项共用 `worker.name`、`worker.kind`、`worker.outcome` 三个标签。
`worker.name` 必须在装配期确定；`worker.kind` 为 `one_shot`、`singleton`、`queue`，
API 收到其他 kind 时统一归入 `other`。队列按 consumer 名聚合，不按任意任务类型、task ID
或 payload 拆标签。

结果分为 `success`、`failure`、`canceled`、`timeout`、`panic`。只有纯取消/纯 deadline 错误
归入对应结果；与真实失败混合时保留 `failure`。`panic` 表示任务未正常返回，也包含
`runtime.Goexit` 这类异常退出；埋点不 recover，不改变既有 panic 处理流程。

默认装配覆盖 OneShot 的 `Run`、带 `singletonObserver` 的 singleton 实际任务函数、
`NewObservedConsumer` 创建的队列消费者每次处理尝试。队列重试会再次计数。
没有执行任务、没抢到租约、租约续期/释放、Prepare/Cleanup 均不产生执行样本；
LongRunning 的整个存活时间也不作为一次任务耗时。自定义循环内的工作需在实际处理边界
调用 `Recorder.RunWorker`，或自行调用 `RecordWorkerExecution`。

旧 `worker.runs` 保留原口径，singleton 的租约丢失也可能计入其 failure，不能用作实际
执行次数。既有健康指标 API 同样保留，新指标没有任务样本时不会伪造零次执行。

### 标签基数是硬约束

**禁止把原始 URL、用户 ID、IP、令牌、订单号写进标签。** 路由标签用路由模板（`FullPath()`），
未匹配路由归到固定常量。

序列数是标签取值的笛卡尔积——把请求方可控的值写进去，一次目录爆破就能把监控后端压垮，
而故障出现在监控侧：**最需要它的时候正好不可用。**

### 状态类指标走异步回调

连接池用量、worker 最后成功时间这类状态量用异步回调，采集时读当前值。

主动上报要在「打得太密」与「错过变化」之间选，而一个每天跑一次的任务，
主动上报意味着它的指标一天只有一个数据点。

## 埋点与导出是两件事

`internal/platform/telemetry` 只管导出。记录点（`metrics.Recorder`、otelgin 中间件）
**无条件存在**，关闭导出时走 OTel 的全局 no-op provider，每次记录都是空操作。

把埋点也做成条件的，打开导出那天才会发现某条关键路径压根没埋——
而那一天通常正是出事要查的时候。

```yaml
telemetry:
  enabled: true
  endpoint: "otel-collector:4318"   # OTLP/HTTP，不带 scheme
  insecure: true                    # 仅限同内网/边车
  metrics: true
  traces: true
  sample_ratio: 0.1
  export_interval: 30s
  shutdown_timeout: 5s              # 上限 30s
```

用 Prometheus 拉取或云厂商专有协议的监控栈，在 bootstrap 之前自行设置全局 provider
（`otel.SetMeterProvider`），记录点同样不需要改动。

### 装配顺序（硬约束）

`telemetry.New` **必须先于** `metrics.New`。`Recorder` 在构造时就从全局 MeterProvider
取 Meter；之后再换 provider，那些仪表仍挂在旧的上——表现为「接了 OTLP 但一条指标也没出去」，
**而且没有任何报错**。

### 采样与传播

- 采样器是 `ParentBased(TraceIDRatioBased(ratio))`：上游已决定采样的链路整条留下。
  只用比例采样会让调用链在本服务断掉，而那恰恰是最需要看的那种链路。
- 传播器装 `TraceContext + Baggage`。不装的话本服务的 span 与上游对不上，
  单服务内部看着正常、整条链是断的。
- 出站具名 HTTP 客户端只注入有效的 W3C TraceContext，不自动传播 baggage；
  沿用传入 ctx 的 span，不另外创建客户端 span。见 [出站请求关联](outbound.md#请求关联)。

### 关停

App 在必要收尾边界建立独立 context；telemetry.Shutdown 以自己的 flush 上限收紧它，
保留父 deadline 和强制取消。装配失败时，遥测与后续资源清理共享 30 秒回滚预算。

顺序上先关 tracer 再关 meter——指标下个周期还会再来，span 丢了就没了。
遥测 flush 排在扩展释放的最后一步：前面几步还会产出指标（关闭计数、错误），
先 flush 就把它们留在了本地。

强制关停（第二个信号）会提前放弃剩余 flush。

## Request ID

HTTP 全局中间件注册 request ID（`internal/middleware/requestid.go`）：进程前缀 + 原子计数，
**不采信客户端传入值**——采信意味着攻击者可以让两条无关请求共用一个 ID，
或用它注入日志内容。

响应头是 `X-Request-ID`。放响应头而不是 body：它对所有响应类型都适用，
包括流式响应与非 JSON 的错误页。

request ID 通过 SDK 的 `logger.ContextWithRequestID` / `logger.RequestIDFromContext` 存取。
handler、worker、外部调用尽量透传 request ID；后台任务没有 request ID 时生成 task/job 标识。

具名出站 HTTP 客户端会从请求 ctx 自动写入 `X-Request-ID`，无需逐个调用点设置 header。
它不改变入站 ID 的信任策略；后台队列也不会因此自动序列化或恢复请求 ctx。

## 告警

高风险事件必须有日志或指标：

- Redis / DB / 第三方 SDK 连续失败
- worker panic 或异常退出、连续失败
- 限流后端不可用
- 外部依赖熔断打开

告警执行点**收口在 bootstrap**（worker 异常/降级与进程退出错误）。
请求级异常刻意不接告警——一次流量异常就会形成告警风暴，淹没真正的进程级事件。
`internal/middleware/recovery.go` 因此只走日志与指标。

### 告警通道有真实对外副作用

默认入口把 `notification` 注入 logging 的 Messager。H 系列日志写入有界队列后，依次尝试机器人、邮件和各手机号短信；这是尽力通知，入队、排空返回与真实送达是不同结果。

- `news.fsurl` 非空提供默认机器人目标；空则跳过，显式目标仍可发送。覆盖地址只影响机器人，邮件/短信仍使用其配置。
- `email.host` 非空构造 SMTP 客户端，`alert_to` 非空才参与邮件告警。
- `sms.provider=aliyun` 构造短信客户端，号码和逻辑模板成对才参与告警，短信可能计费。

配置用途、默认值和关联约束见 [配置说明](../config/README.md#通知通道与告警开关)。现有配置字段和值不迁移。机器人仍支持飞书（空平台默认）、钉钉和企业微信；签名沿用各平台 SDK。

单次发送独立 10 秒，按机器人→邮件→逐手机号短信顺序运行；不增加项目重试。唯一队列是日志 SDK 的 64 条待处理队列，满时会丢弃，SDK 的 `DroppedMessages()` 可读取其计数。队列不能承担持久化、必达或去重承诺。

失败和 panic 只走普通诊断，包含通道、有限结果分类及耗时；不输出 SDK 错误原文、目标、凭据、收件人或正文。关闭前恢复原 SDK 默认实例，之后的通知诊断走该实例，不递归触发 H 系列；调用 `Notify` 的代码可通过 error 判断实际失败。

### 通知渠道与替换方式

默认通知装配由 `internal/bootstrap/resources.go` 完成：robot/mail/sms → dispatch → logging。
各渠道位于 `internal/platform/notification/`，使用已加载的 `*config.Config` 直接构造，
构造时不发送启动测试消息。渠道的 `Options.Open` 可注入 sender 和可选清理函数；
清理权转交对应 Owner，通知组合和日志只借用发送能力。sender 必须响应 ctx 取消，
不能关闭外部借用资源。供应商配置白名单、唯一日志 SDK 队列和发送预算保持不变。

### 日志的所有权

`internal/platform/logging` 使用固定的 gtkit/logger/v2，main/access 共用一个 Owner。
允许写日志的调用方直接使用 SDK；不保留自有 Level、Field、LogEntry、Install 或逐条写入代理。
纯日志可以直接使用 `logging.Open(ctx, logging.Options{Config: cfg})`，按需提供 Notification。
HTTP 中间件在最终 metrics 就绪后直接构造，并要求非 nil 的原生 access 实例。

`http.Server.ErrorLog` 要求 `*log.Logger`。`internal/bootstrap/http.go` 仅保留
`log.New(httpErrorWriter{}, "", 0)` 类型桥，将 net/http 内部错误直接写入 SDK；
项目其他日志继续使用既定 logger。

正常关闭先确认 HTTP handler、worker 和扩展日志源已停止，再释放缓存和连接、恢复原默认实例、绑定清理 context 并 Sync access/main。
随后 dispatch 拒绝新通知并取消在途发送，各渠道等待在途归零后释放自有资源。
worker 尚未退出、HTTP 仅强制关闭或其他日志源未确认结束时，只对同一 owner 的 access/main 调用 `Zap().Sync()` 刷文件缓冲，保留 SDK、通知和仍被使用的共享资源，返回原关停错误。此操作不排空通知队列，也不拒绝后续日志，不消耗后续安全 Close 的机会。
回滚和停止预算各 30 秒，第二信号取消合作式等待。SDK Sync 无 context/error 返回，
无法硬中断阻塞 I/O，也不代表日志已持久化或通知已送达。

锁定 logger/v2 v2.5.0 的 Sync 默认最多等待通知队列 5 秒，超时后回调仍可能继续。
原默认实例的恢复不构成在途日志屏障；正常关闭仍要求业务写入者先停止。
通知与各渠道的接纳保护让迟到回调在关闭后不再发起请求。
不响应取消的自定义 sender 超过预算时，Close 返回 `ErrNotDrained` 并保留资源，
重复 Close 不再次清理；由进程退出回收。

### 本实例SQL慢查询阈值与失败诊断

`log.sql_slow_threshold_ms`省略/null时为200ms，0仅关闭专属慢查询WARN，正整数按毫秒生效，负值/溢出拒绝。`log.sql`非零时普通SQL INFO保持，错误SQL仍按原级别输出；参数过滤始终生效。阈值构造时按值固定，LogMode副本不共享配置指针。prod环境拒绝debug日志级别（首尾空白规则不变），dev/test允许。

统一HTTP错误日志使用goerr1.5的Inspect保留原根错误、Join并列清理原因及已有栈；对客只输出原公开文案和request_id。诊断失败独立记录，不能覆盖主状态。初始化前使用 SDK 的 stderr-only fallback，接受其原生格式；不再保存或查询旧门面的 stderr 写失败。SDK 的 void 输出/Sync 仍受其可观测边界约束，不能把 void 返回当作持久送达证据。
