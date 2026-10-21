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

`internal/pkg/news` 挂在 `gtkit/logger/v2` 的 Messager 上：**进程记一条告警级日志，
就会向已配置的全部通道各发一条消息**——webhook、SMTP 邮件、短信。

因此它的误配置后果是**真实发送**，而不是一条错误日志：

- `email.alert_to` 非空即启用邮件通道，收件人会收到真实邮件；
- `sms.alert_phones` 与 `sms.alert_template` 成对配置即启用短信通道，**按条计费**；
- webhook 配错群地址，告警内容会发进那个群。

接手部署时应先确认这三处的取值，再让服务在有真实流量的环境启动。

单次发送有独立超时（告警发生在日志调用路径上，通道故障不能拖住调用方），
异步队列深度有限，超出即丢弃并计数——告警只承载进程级事件，
积压几十条本身就已经是告警风暴。

**发送失败只记 Error 级日志，不触发 Messager**——否则一次平台故障会变成无限递归的告警风暴。

webhook 底层是 `gtkit/msgbot`：

```yaml
news:
  fsurl: "https://open.feishu.cn/open-apis/bot/v2/hook/xxx"
  platform: ""        # 留空按飞书处理 / dingtalk / wecom
  secret: ""          # 平台侧开启加签时必填
```

`platform` 留空必须与该字段引入之前的行为一致（按飞书处理），否则一次升级就会让所有
存量部署的告警发不出去。有测试钉住这条。

## 日志插件的所有权

`cmd/plugins.go` 选择并注册日志插件，配置 Init 在其之前完成。业务、数据层和中间件经
`internal/pkg/log` 的事件门面转发给所选实例；默认 gtkit/logger/v2 实现在 builtin/logging，
main/access 共用一个生命周期 owner。HTTP 中间件在最终 metrics 仪表就绪后 Build。

`http.Server.ErrorLog` 要求 `*log.Logger`。经用户于 2026-10-21 明确允许，
`internal/bootstrap/http.go` 仅保留 `log.New(httpErrorWriter{}, "", 0)` 这个类型桥，
把 net/http 内部错误转发到所选日志插件；它不创建独立日志后端。该例外不扩大到其他
标准库 log 或 slog 用法，项目其他日志继续遵守现有日志规则。

正常由注册器在缓存/连接之后 Stop 日志插件，先停止接收新写入，等待已接收写入，再 Sync
access/main。若 worker 未退出，只执行同一 owner 的 Drain，并返回资源未关闭错误。
回滚/停止总预算各 30 秒，第二信号取消剩余合作式等待；SDK Sync 没有 context 或 error 返回，
不能承诺在阻塞 I/O 上硬中断，也不能把 void Sync 当作日志已成功送达的证据。
