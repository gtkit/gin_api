# 可选出站 HTTP

统一通过 `-tags optional` 编译接线，按各能力配置启用；默认构建不导入可选包。
验证入口为 `GOWORK=off go run -trimpath scripts/check-optional-wiring.go`，直接在原项目检查
默认/optional 的依赖图和构建，不复制源码、不启动服务。完整说明见 [可选能力接入](../../../docs/optional-capabilities.md)。

本目录包含具名 HTTP 客户端、请求关联头、重试、熔断、配置与测试。
默认主程序不导入本包；optional 构建读取 outbound 配置，普通构建将这些键作为未使用键诊断。

| 文件 | 职责 |
|---|---|
| `config.go` | 客户端配置、默认值和 Validate |
| `factory.go` | 复用具名客户端及连接池 |
| `downstreams.go` | 客户端与熔断组合入口 |
| `retrying.go` | 显式幂等调用的有界重试 |
| `propagation.go` | 传播 context 中的 Request ID 和 TraceContext |
| `breaker/` | 各下游独立的熔断实例 |

## 启用接线

使用 `-tags optional` 构建，把 `config/optional.yml.example` 的 outbound 段合并到运行配置。
clients 为空时不创建客户端；配置真实具名下游后，在模块装配中将 inf.outbound 注入服务复用。
配置校验、连接池创建与关闭已接入。直接使用包构造函数时仍需处理构造错误。

## 调用

`outbound.NewDownstreams(cfg)` 返回共享入口：

- `Do(ctx, name, attempt)`：执行一次调用，适合非幂等写请求。
- `DoIdempotent(ctx, name, retry, attempt)`：仅用于幂等调用，带有界退避重试。
- `Client(name)`：取得原始客户端，不带熔断，只用于组合入口覆盖不了的场景。
- `Close()`：释放空闲连接。

默认单次请求超时 10 秒、响应体上限 8 MiB；应按实际下游设置更合适的值。
重试总耗时还包括每次尝试和退避，需要调用方设置总 context 预算。
完整用法、错误分类与请求关联说明见 [出站调用](../../../docs/outbound.md)。

### 重试边界与未来客户端

`Attempts` 是首次调用之后的**最多**重试次数（1..3），不保证全部执行；成功、错误分类、取消和等待预算会提前结束。`WaitTime`/`MaxWaitTime` 均须在 0..30s，且正的 MaxWaitTime 不小于 WaitTime。MaxWaitTime=0 使用 30s 上界；WaitTime=0 保留零本地等待。超过 30s 的旧策略现会在调用前校验失败。

本地指数退避采用全抖动。重试循环的单次及累计等待上限均为 30s；这个预算不含实际 HTTP 请求耗时，调用方仍需设置整个操作的 context deadline。服务端 Retry-After 支持整数秒及 HTTP-date，等待取其与本地抖动的较大值。合法但太大、整数溢出、超剩余等待预算或无法放进父 deadline 时，直接返回最后状态与原错误；不截短后提前重试。无 deadline 时的 86400 秒头也会立即放弃。非法语法回落本地退避，等待中取消保留原错误与 context 错误。

响应元信息经现有 transport 按每次尝试单独收集；请求头、响应体和连接池所有权不变。重试引擎保持私有，Do/DoIdempotent/Attempt 签名不变。

未来可能接入 `github.com/gtkit/restyx`，本次没有引入或迁移它。接入时再评估公开适配入口，确定唯一重试所有者，并核对取消、超时、响应上限、连接池、追踪和日志；不得叠加 restyx 自有重试与本引擎后宣称次数上限仍然成立。

## 停用与删除

清空 clients 后重启可停用出站客户端；默认构建不包含本包。
物理删除包须同时清理 optional 装配、配置字段、相关测试和文档引用，不能只删目录后继续 optional 构建。
