# 可观测性

## 日志

- 字段 key 稳定，用 snake_case：`user_id`、`request_id`、`duration_ms`。
- 错误日志带操作上下文，不只打印 `err`。
- 热路径避免频繁 Info；异常路径用 Warn/Error。
- **禁止打印密码、token、完整 API key、完整请求体、签名密钥。**
- 对外错误文案经 response 层映射，底层错误只进日志。

日志栈是 `gtkit/logger/v2`，进程级初始化在 `internal/pkg/log`：主实例（不缓冲）+
access 实例（缓冲）。

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

母版已埋点：HTTP 请求量/时延/状态、限流判定（按策略与拒绝原因）、worker 连续失败与
最后成功时间、DB 连接池、认证事件。

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

### 关停

App 在必要收尾边界建立独立 context；telemetry.Shutdown 以自己的 flush 上限收紧它，
保留父 deadline 和强制取消。装配失败时，遥测与后续插件清理共享 30 秒回滚预算。

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

handler、worker、外部调用尽量透传 request ID；后台任务没有 request ID 时生成 task/job 标识。

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

失败和 panic 只走普通诊断，包含通道、有限结果分类及耗时；不输出 SDK 错误原文、目标、凭据、收件人或正文。日志门面释放后仍使用普通后备诊断，不递归触发 H 系列；调用 `Notify` 的代码可通过 error 判断实际失败。

### 通知插件与替换方式

现有 `core.PluginRegistry` 管理所有生命周期；构造和 Register 不读配置、不发消息。默认三个通道的 `Open` 可分别注入，返回 sender、可选 `func(context.Context) error` 清理函数和 error。资源的清理权只转交对应通道；通知组合和日志只借用能力。

下面是入口中替换机器人 sender 的装配片段；`configuration` 是已注册的配置插件，`openRobot` 符合 `robot.Opener`。邮件和短信保持默认：

```go
robotChannel := robot.New(robot.Options{Configuration: configuration, Open: openRobot})
mailChannel := mail.New(mail.Options{Configuration: configuration})
smsChannel := sms.New(sms.Options{Configuration: configuration})
for _, channel := range []core.Plugin{robotChannel, mailChannel, smsChannel} {
    if err := registry.Register(channel, configuration.Name()); err != nil { return err }
}
alerts := notification.New(notification.Options{
    Configuration: configuration, Robot: robotChannel, Mail: mailChannel, SMS: smsChannel,
})
if err := registry.Register(alerts, configuration.Name(), robotChannel.Name(), mailChannel.Name(), smsChannel.Name()); err != nil { return err }
logs := logging.New(logging.Options{Configuration: configuration, Notification: alerts})
if err := registry.Register(logs, configuration.Name(), alerts.Name()); err != nil { return err }
```

这些包位于 `internal/plugin/builtin`。纯日志组合不注册上述通道/组合，直接构造 `logging.New(logging.Options{Configuration: configuration})`，并仅依赖配置插件。默认 server 与 migration 共用 `cmd/plugins.go` 的通知装配。替换不新增供应商配置、第二个队列或全局注册表；sender 必须响应 ctx 取消，自有资源由其 Open 返回清理函数，不得关闭外部借用的共享资源。

## 日志插件的所有权

`cmd/plugins.go` 选择并注册通知和日志插件，配置及通道/组合 Init 先于 logging 完成。业务、数据层和中间件经
`internal/pkg/log` 的事件门面转发给所选实例；默认 gtkit/logger/v2 实现在 builtin/logging，
main/access 共用一个生命周期 owner。HTTP 中间件在最终 metrics 仪表就绪后 Build。

`http.Server.ErrorLog` 要求 `*log.Logger`。经用户于 2026-10-21 明确允许，
`internal/bootstrap/http.go` 仅保留 `log.New(httpErrorWriter{}, "", 0)` 这个类型桥，
把 net/http 内部错误转发到所选日志插件；它不创建独立日志后端。该例外不扩大到其他
标准库 log 或 slog 用法，项目其他日志继续遵守现有日志规则。

正常由注册器在缓存/连接之后 Stop 日志插件：关闭新写入，绑定实际清理 context，等待已接收写入，再 Sync access/main，最后幂等解除取消绑定。随后 notification.Stop 拒绝新通知并取消在途发送，各通道等待在途归零再释放自有资源。若 worker 未退出，只 Drain 同一日志 owner，保留共享通道并返回未关闭资源错误。
回滚/停止总预算各 30 秒，第二信号取消剩余合作式等待；SDK Sync 没有 context 或 error 返回，
不能承诺在阻塞 I/O 上硬中断，也不能把 void Sync 当作日志已成功送达的证据。

锁定 logger/v2 v2.3.0 的 Sync 等待队列消费结束；本机 workspace SDK 默认等候 5 秒后可能返回，但后台回调仍继续。通知与每个通道均保护接纳和在途，迟到回调在停止后不会新发请求。无视取消的自定义 sender 超过预算时，Stop 返回 `ErrNotDrained` 并保留资源，重复 Stop 不再次清理；由进程退出回收。强制信号只能取消合作式工作，不能保证任意文件 I/O 或不合作 SDK 在硬 30 秒内退出。

### 本实例SQL慢查询阈值与失败诊断

`log.sql_slow_threshold_ms`省略/null时为200ms，0仅关闭专属慢查询WARN，正整数按毫秒生效，负值/溢出拒绝。`log.sql`非零时普通SQL INFO保持，错误SQL仍按原级别输出；参数过滤始终生效。阈值构造时按值固定，LogMode副本不共享配置指针。prod环境拒绝debug日志级别（首尾空白规则不变），dev/test允许。

统一HTTP错误日志使用goerr1.5的Inspect保留原根错误、Join并列清理原因及已有栈；对客只输出原公开文案和request_id。诊断失败独立记录，不能覆盖主状态。无日志实现时的stderr写出失败保留为有界进程状态，由Drain返回；不递归写失败日志器。默认gtkit SDK的void输出/Sync仍受SDK本身可观测边界约束，不能把void返回当作持久送达证据。
