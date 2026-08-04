# 熔断与降级设计文档

## 一、概述

本项目实现了两套互补的服务保护机制：

- **熔断（Circuit Breaker）**：保护调用方不被故障的外部服务拖垮，加在 client 侧
- **降级（Degradation/Fallback）**：在服务不可用时给用户返回友好的兜底数据，加在 HTTP 响应层

两者协同工作，确保任何基础设施故障（DB 宕机、Redis 不可用、LLM 服务超时）都不会让用户看到 500 错误页面。

## 二、熔断机制

### 2.1 使用场景

熔断器适用于**高频调用外部服务**且**故障时会导致请求堆积**的场景。

| 外部服务 | 是否加熔断 | 理由 |
|---------|-----------|------|
| LLM Provider | **是** | 用户每次聊天触发，超时 60s，故障时大量 goroutine 堆积 |
| 微信/支付宝 | 否 | 支付是关键路径，宁可等也不能误拒 |
| 阿里云短信 | 否 | 低频，用户主动触发，失败直接重试 |
| 百度语音 | 否 | WebSocket 长连接，不存在请求堆积 |
| SMTP 邮件 | 否 | 后台任务，不阻塞用户请求 |

**核心判断标准**：高频调用 + 故障时请求堆积 = 需要熔断。

### 2.2 实现架构

```
用户请求 → Gin Handler → LLM Service → BreakerProvider → LLM API
                                            ↑
                                       熔断器在这里
                                  （client 侧，不是入口侧）
```

熔断器加在 **client 侧**而非 HTTP 入口中间件。原因：

1. 入口侧熔断会把整个接口断掉，连返回业务错误码的机会都没有
2. client 侧熔断只断外部调用，Handler 仍可返回降级响应
3. 不同外部服务独立熔断，互不影响

### 2.3 核心组件

#### `internal/pkg/breaker/breaker.go`

基于 [sony/gobreaker](https://github.com/sony/gobreaker) v2 的泛型熔断器封装。

**状态机**：

```
     ┌─────────────────────────────────────┐
     │              Closed                  │
     │  (正常放行所有请求，统计错误率)         │
     └───────────┬─────────────────────────┘
                 │ 请求量 >= MinRequests 且
                 │ 错误率 >= ErrorRatio
                 ▼
     ┌─────────────────────────────────────┐
     │               Open                  │
     │  (所有请求直接拒绝，快速失败)          │
     │  等待 RecoverTimeout 后进入 HalfOpen  │
     └───────────┬─────────────────────────┘
                 │ 等待 RecoverTimeout
                 ▼
     ┌─────────────────────────────────────┐
     │            HalfOpen                  │
     │  (放行 MaxRequests 个探测请求)        │
     │  成功 → Closed  |  失败 → Open       │
     └─────────────────────────────────────┘
```

**默认配置**：

| 参数 | 默认值 | 含义 |
|------|--------|------|
| `MinRequests` | 10 | 触发熔断判断的最小请求量，防低流量误熔断 |
| `ErrorRatioPercent` | 60% | 错误率阈值 |
| `RecoverTimeout` | 15s | 熔断后多久尝试半开恢复 |
| `MaxRequests` | 3 | 半开状态允许的探测请求数 |
| `CountInterval` | 30s | 统计窗口周期 |

**业务错误过滤**：实现 `IsBusinessError` 接口的 error 不计入熔断统计，防止参数校验失败等业务错误触发熔断。

```go
type IsBusinessError interface {
    IsBusinessError() bool
}
```

#### `internal/module/llm/application/provider_breaker.go`

LLM Provider 的熔断装饰器，实现 `Provider` 接口，对业务代码零侵入。

**熔断后的降级行为**：返回 `degradedStream`，前端 SSE 流程不变，用户看到的是一条正常的聊天消息：

> 抱歉，AI 助手暂时繁忙，请稍后再试～

```go
// degradedStream 实现 Stream 接口
// 首次 Recv 返回预设消息，之后返回 io.EOF
type degradedStream struct {
    msg  string
    sent bool
}
```

### 2.4 使用示例

```go
// 创建熔断器
b := breaker.New[*Response]("llm-provider",
    breaker.WithMinRequests(10),
    breaker.WithErrorRatioPercent(60),
    breaker.WithRecoverTimeout(15 * time.Second),
)

// 通过熔断器执行外部调用
result, err := b.Execute(func() (*Response, error) {
    return client.Call(ctx, req)
})
// 熔断打开时 err = gobreaker.ErrOpenState，fn 不会执行
```

**扩展其他服务**：如果将来其他外部服务需要熔断，写一个装饰器实现对应接口，内部调用 `breaker.Execute` 即可。

---

## 三、降级机制

### 3.1 使用场景

降级适用于**变更低频的读接口**，在 DB/Redis 不可用时返回上一次的成功数据。

| 路由 | 是否降级 | 降级策略 | 理由 |
|------|---------|---------|------|
| `GET /common/navbar` | **是** | 本地缓存兜底 | 导航栏是 App 骨架，挂了页面空白 |
| `GET /products` | **是** | 本地缓存兜底 | 商品列表是购买入口，变更低频 |
| `GET /articles` | **是** | 本地缓存兜底 | 内容列表是核心浏览页，变更低频 |
| `POST /llm/chat/stream` | **是** | 降级消息流 | 熔断后返回友好提示 |
| `POST /orders` | **否** | - | 支付不能降级，不能给用户假数据 |
| `POST /phone/code` | **否** | - | 发不出就是发不出，不能假装成功 |
| `GET /common/shortcut` | **否** | - | 硬编码数据，不查 DB |
| `GET /common/banner` | **否** | - | 硬编码数据，不查 DB |

### 3.2 实现架构

#### 三级缓存体系

```
请求 → FallbackMW(L3 本地内存) → gincache(L1 Redis) → Handler(数据源 DB)
         ↑                          ↑                      ↑
    最后一道防线              高速缓存层                 数据源
    TTL 10 分钟              TTL 5-30 秒               实时查询
    32MB 内存上限            Redis 存储
```

**中间件顺序至关重要**：`FallbackMW → gincache → Handler`

- FallbackMW 必须在 gincache **外层**
- gincache 内部会替换 `c.Writer` 来拦截响应，如果 fallback 在内层会被绕过
- 外层的 fallback 能拦截到 gincache 写出的最终响应（无论命中还是未命中）

#### 核心组件

**`internal/pkg/cache/fallback.go`** — 本地内存缓存

基于 `allegro/bigcache` 实现，进程内存级别，无网络开销：

- TTL：10 分钟（可接受的数据陈旧上限）
- 内存上限：32MB
- 无锁设计，并发安全

**`internal/middleware/fallback.go`** — Gin 降级中间件

使用 `bufferedWriter` 缓冲整个响应体，延迟到决策后才写出：

```
1. 替换 c.Writer 为 bufferedWriter
2. 执行 c.Next()（gincache + Handler）
3. 检查状态码：
   - 200 → 更新本地缓存，写出响应
   - 5xx + 有缓存 → 丢弃错误响应，写出缓存数据
   - 5xx + 无缓存 → 写出友好的空数据响应 {"code":0,"msg":"OK","data":[]}
   - 4xx → 原样写出（业务错误不降级）
```

### 3.3 降级响应格式

降级响应与正常响应格式完全一致，用户无感知：

```json
// 正常响应
{"code": 0, "msg": "OK", "data": [{"id": 1, "title": "..."}]}

// 降级响应（有缓存，只是数据可能稍旧）
{"code": 0, "msg": "OK", "data": [{"id": 1, "title": "..."}]}

// 降级响应（无缓存，服务刚启动就遇到故障）
{"code": 0, "msg": "OK", "data": []}
```

降级响应会附带 `X-Degraded: true` 响应头，前端可据此展示"数据可能不是最新"的提示。

---

## 四、完整流程图

### 4.1 LLM 聊天请求

```
用户发送消息
    │
    ▼
Gin Handler (StreamChat)
    │
    ▼
BreakerProvider.ChatStream()
    │
    ├── 熔断器 Closed ──→ 调用 LLM API
    │                         │
    │                    ├── 成功 → 正常流式返回
    │                    └── 失败 → 返回错误，失败计数 +1
    │                              │
    │                              ├── 错误率 < 阈值 → 保持 Closed
    │                              └── 错误率 >= 阈值 → 切换到 Open
    │
    └── 熔断器 Open ──→ 不调用 LLM API（快速失败）
                            │
                            ▼
                    返回 degradedStream
                            │
                            ▼
                    用户看到: "抱歉，AI 助手暂时繁忙，请稍后再试～"
```

### 4.2 读接口请求（以 /products 为例）

```
GET /v1/products
    │
    ▼
FallbackMiddleware（替换 c.Writer 为 bufferedWriter）
    │
    ▼
gincache (Redis 缓存)
    │
    ├── Redis 命中 → 直接返回缓存数据（X-Cache: HIT）
    │                   │
    │                   ▼
    │              fallback 看到 200 → 更新本地缓存 → 写出响应
    │
    └── Redis 未命中 → 执行 Handler
                          │
                          ▼
                    Handler 查询 DB
                          │
                     ├── DB 正常 → 200 + 数据
                     │       │
                     │       ▼
                     │  fallback 更新本地缓存 → gincache 写入 Redis → 响应
                     │
                     └── DB 异常 → 500
                              │
                              ▼
                         fallback 拦截 500
                              │
                         ├── 本地有缓存 → 返回缓存数据 + X-Degraded: true
                         └── 本地无缓存 → 返回 {"code":0,"data":[]} + X-Degraded: true
```

---

## 五、前端对接

### 5.1 降级检测

```javascript
const response = await fetch('/v1/products')
const data = await response.json()

// 检测是否为降级数据
if (response.headers.get('X-Degraded') === 'true') {
    showToast('数据可能不是最新，请稍后刷新')
}

// data 格式与正常响应完全一致，无需特殊处理
renderList(data.data)
```

### 5.2 LLM 聊天降级

前端 SSE 逻辑不需要任何改动。熔断时收到的降级消息与正常 LLM 回复走同一个 stream 通道，只是内容是预设文案。

---

## 六、日志与告警

### 6.1 日志关键字

| 日志内容 | 级别 | 含义 |
|---------|------|------|
| `circuit breaker state changed` | WARN | 熔断器状态变更（关注 → open） |
| `serving degraded response from local cache` | WARN | 读接口正在返回降级数据 |
| `serving empty degraded response` | WARN | 无缓存的降级（服务刚启动） |

### 6.2 建议告警规则

```yaml
# 降级日志频繁出现
- alert: DegradedResponsesHigh
  expr: rate(log_messages_total{msg=~"serving.*degraded.*"}[5m]) > 0.1
  for: 3m
  labels:
    severity: warning
  annotations:
    summary: "读接口频繁降级，检查 DB/Redis 状态"
```

---

## 七、文件清单

| 文件 | 职责 |
|------|------|
| `internal/pkg/breaker/breaker.go` | 通用泛型熔断器，封装 gobreaker 与状态变更日志 |
| `internal/pkg/breaker/breaker_test.go` | 熔断器单元测试 |
| `internal/pkg/cache/fallback.go` | 本地内存兜底缓存，基于 bigcache |
| `internal/pkg/cache/fallback_test.go` | 缓存单元测试（5 个用例） |
| `internal/middleware/fallback.go` | Gin 降级中间件，bufferedWriter 缓冲响应 |
| `internal/middleware/fallback_test.go` | 降级中间件测试（11 个用例） |
| `internal/middleware/fallback_gincache_test.go` | gincache + fallback 集成测试（3 个用例） |
| `internal/module/llm/application/provider_breaker.go` | LLM Provider 熔断装饰器 + degradedStream |
| `internal/module/llm/application/provider_breaker_test.go` | LLM 熔断集成测试（10 个用例） |
| `internal/bootstrap/providers.go` | FallbackCache 创建 + 模块注入 |

---

## 八、用户体验总结

| 故障场景 | 用户看到的 | HTTP 状态码 |
|---------|-----------|------------|
| 一切正常 | 实时数据 | 200 |
| DB 挂了，有本地缓存 | 上次成功的数据（可能稍旧） | 200 |
| DB 挂了，无本地缓存 | 空列表，前端渲染"暂无内容" | 200 |
| Redis 挂了，DB 正常 | 实时数据（gincache miss 穿透到 DB） | 200 |
| Redis + DB 都挂了 | 本地缓存兜底 | 200 |
| LLM 服务超时/故障 | 错误透传，前端展示错误信息 | 200 (SSE) |
| LLM 持续故障触发熔断 | 聊天气泡："AI 助手暂时繁忙，请稍后再试～" | 200 (SSE) |
| 参数错误（4xx） | 正常业务错误提示（不降级） | 4xx |
| 支付/短信失败 | 正常错误提示（不降级，不能给假数据） | 对应错误码 |

**任何降级场景下用户都不会看到 500 错误页面或技术报错信息。**
