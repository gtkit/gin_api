# 出站调用

覆盖出站 HTTP 客户端（`internal/pkg/httpclient`）、熔断（`internal/pkg/breaker`）及两者的组合。

**架构传感器禁止使用 `http.DefaultClient` / `http.Get` / `http.Post` / `http.Head`**：
它们用的是同一个进程级 client，`Timeout` 为 0（没有任何超时），Transport 全局共享
（一个依赖的连接池耗尽会波及所有其他依赖），且无处设置单依赖的超时或连接池。

## 默认入口：`httpclient.Downstreams`

`gtkit/httpc` 的具名客户端 + 按同一批下游名登记的熔断实例，装配期由 bootstrap 构造并注入。
组合关系一次定死：**熔断在外、重试在内、重试复用同一个客户端**——调用点不再自己拼装三件事。

```yaml
outbound:
  defaults:
    timeout: 10s
    max_response_bytes: 8388608     # 8 MiB
  clients:
    billing:
      base_url: https://billing.internal
      timeout: 3s
    catalog:
      base_url: https://catalog.internal
      max_conns_per_host: 32
  breaker: {}   # 零值取保守默认：连续失败 5 次熔断、开启 60s、半开放行 1 个探测
```

**响应体上限不可省**——下游返回 HTML 错误页或被中间设备劫持时，没有上限就会被整个读进内存。

```go
// 写调用 / 非幂等调用：Do（一次尝试，熔断包住）
status, err := downstreams.Do(ctx, "billing",
    func(ctx context.Context, client *httpc.Client) (int, error) {
        return client.PostJSON(ctx, "/charges", req, &resp)
    })

// 幂等只读调用：DoIdempotent（熔断在外，有界退避重试在内）
status, err := downstreams.DoIdempotent(ctx, "catalog",
    httpclient.RetryPolicy{Attempts: 2, WaitTime: 50 * time.Millisecond, MaxWaitTime: time.Second},
    func(ctx context.Context, client *httpc.Client) (int, error) {
        return client.GetJSON(ctx, "/items/"+id, nil, &out)
    })

switch {
case breaker.IsOpen(err):
    // 下游已被熔断：直接降级，连重试都不该发生
case errors.Is(err, httpclient.ErrUnknownClient):
    // 名字没在配置里登记 —— 编码或配置错误
case errors.Is(err, httpclient.ErrDownstreamServerError):
    // 下游以 5xx 回答：入口把它归一化成错误，让状态码进入熔断统计
}
```

### 约束

- **名字即故障域**：客户端与熔断实例在构造时用同一批名字，「这个下游的连接池」
  与「这个下游的熔断器」恒指同一件事；未登记的名字返回 `ErrUnknownClient` 且不执行调用。
- **`DoIdempotent` 只用于幂等调用**：重试判据涵盖「请求已发出但响应没回来」——
  对非幂等请求这正是最危险的一类，一次网络抖动就是两笔订单。
  写调用走 `Do`，重放由业务的幂等键处理。
- **重试执行体拿到的就是同一个客户端**：连接池、总超时、响应体上限全部随之生效，
  不存在「重试路径用了另一个没有防护的客户端」。
- 重试判据是 `httpclient.RetryableFailure(status, err)`：传输层错误、429、5xx 重试；
  4xx 与 501 不重试；调用方取消/超时不重试（没人在等它的结果了）。
- **超时是每次尝试的超时**，不是全部重试的总预算：最坏耗时约 `(Attempts+1) × Timeout` 加退避。
  排端点预算时按这个算。
- `Client(name)` 可取原始客户端，只用于 `Do`/`DoIdempotent` 覆盖不了的编排
  （流式下载、自定义中间件）——**它不带熔断**，常规调用别用它。
- 关停时 `Close` 释放全部空闲连接；滚动重启时旧实例不再占着下游的连接配额。

## 请求关联

`Factory` 创建的具名客户端（包括 `Downstreams` 内的客户端）在发送时读取该次请求的
标准 `context.Context`，自动附带内部 `X-Request-ID` 和有效的 W3C `traceparent` / `tracestate`。
调用时持续传入入口的 ctx 即可，前面的 `Do` / `DoIdempotent` 示例已满足这一要求。

- 每次发送复制 request/header，不把关联字段写入客户端共享的默认 header，也不改调用方的原请求。
- context 有对应值时覆盖该字段；没有内部 Request ID 或有效 span context 时保留显式 header，
  不自动生成 ID 或 trace。使用 `context.Background()` 会失去入口关联。
- 只传播 TraceContext，不自动转发 baggage、Authorization、Cookie 或其他入站 header。
- 这层只传播现有 span context，不创建客户端 span，不更改采样、重试、熔断或超时预算。
- 入站仍生成本服务的内部 Request ID，不采信来路的 `X-Request-ID`；因此本服务的出站日志
  可用同一 ID 关联，但接收方若也重新生成 ID，需要结合 trace ID 串联整条链路。

## 熔断（`internal/pkg/breaker`）

`sony/gobreaker/v2` 的薄适配：具名实例、配置阈值、状态变更进日志。不自研状态机。

需要在 `Downstreams` 之外单独使用时：

```go
breakers := breaker.NewRegistry(breaker.Config{
    ConsecutiveFailures: 5,                // <=0 取 5
    OpenTimeout:         30 * time.Second, // 熔断后到半开探测的等待；<=0 取 60s
    HalfOpenMaxRequests: 1,                // 半开期探测配额；<=0 取 1
    IsFailure: func(err error) bool {
        return breaker.DefaultIsFailure(err) && !errors.Is(err, ErrInsufficientBalance)
    },
}, "billing", "inventory")   // ← 下游集合是启动期事实

err := breakers.Do("billing", func() error { return callBillingAPI(ctx) })
```

### 约束

- **实例必须复用**：熔断状态跨请求累计，每次调用新建 Registry 的「熔断」永远不会熔断。
  装配层构造一次、注入使用方。
- **下游名必须装配期登记**：`Do` 遇未登记名字返回 `ErrUnknownDownstream` 且不执行调用。
  按运行期传入的名字隐式建实例会让注册表随请求内容无界增长，
  而一个拼错的名字会悄悄得到一个永不跳闸的熔断器。
- **按下游命名**：一个 name 对应一个故障域；把多个下游塞进同一个 name，
  一个下游的故障会把其他下游一起熔断。
- **失败判据要与「下游健康」对齐**：调用方取消/超时默认已排除；
  业务错误（HTTP 4xx、「余额不足」）应经 `IsFailure` 排除——
  把它们计进去，一批正常的参数错误就能熔断掉一个健康的下游。
- **用 `IsOpen` 分流**：熔断快速失败与下游真实错误的处置完全不同——
  前者降级，后者按下游语义处理。
- 状态变更事件自动进日志（Warn 级）。排查「为什么突然全部快速失败/恢复」从这里入手。

## 为什么熔断在外、重试在内

两者分工不同，缺一不可：**重试处理偶发失败**（一次抖动、一台后端实例挂了），
**熔断处理持续失败**（整个下游不可用）。

只有重试没有熔断，下游全挂时每个请求都会重试满次数——等于给一个已经倒下的依赖再加 N 倍压力。
只有熔断没有重试，一次网络抖动就会计入失败计数，几次抖动就能把一个健康的下游熔断掉。

嵌套顺序是**熔断在外**：跳闸之后连重试都不发生。反过来（重试在外）意味着每次重试
都去问一次熔断器，那 N 次快速失败仍然会各自消耗一轮退避等待，把调用方白白拖住。

整轮重试对熔断计一次结果；退避等待可被调用方 ctx 取消，取消时最后一次真实失败
与 ctx 错误聚合返回（熔断的失败判据据此仍能看到真实故障）。
