# 可选出站 HTTP

接线编译入口：在项目根运行`GOWORK=off go run ./scripts/check-optional-wiring.go -case outbound`。
工具按manifest中的wiring标记在自有临时副本恢复该能力，主工作区保持注释基线；
只编译主程序和带integration标签的cmd/bootstrap/config测试，不启动业务或执行测试二进制。
完整七例用`make check-optional-wiring`，它是独立目标，不在默认check/check-ci或CI中。
编译通过不等于配置、外部依赖或业务接入通过；已自行修改接线的派生项目须同步维护manifest。

本目录包含具名 HTTP 客户端、请求关联头、重试、熔断、配置与测试。
默认主程序未导入本包，根配置也不接受 `outbound:` 或 `APP_OUTBOUND__...`。

| 文件 | 职责 |
|---|---|
| `config.go` | 客户端配置、默认值和 Validate |
| `factory.go` | 复用具名客户端及连接池 |
| `downstreams.go` | 客户端与熔断组合入口 |
| `retrying.go` | 显式幂等调用的有界重试 |
| `propagation.go` | 传播 context 中的 Request ID 和 TraceContext |
| `breaker/` | 各下游独立的熔断实例 |

## 启用接线

从项目根目录定位：

```powershell
rg -n OUTBOUND internal/bootstrap internal/platform/config cmd config/env.yml.example
```

1. 在 `internal/platform/config/types.go` 恢复 outbound import 和 `Outbound outbound.Config` 字段。
2. 在 `internal/platform/config/validate.go` 恢复 `c.Outbound.Validate()` 的错误处理块。
3. 去掉 `internal/bootstrap/outbound.go` 中包围实现的那一对 `/*`、`*/`。
4. 在 `internal/bootstrap/providers.go` 恢复 outbound import、`infra.outbound` 字段和 `buildOutbound` 调用。
   客户端在 service 之前构造，关闭与启动失败回收已登记。
5. 在 `config/env.yml.example` 的 OUTBOUND-BEGIN/END 之间去掉每行最前面的 `# `，
   将实际下游配置写入运行环境。客户端为空时不构造连接池。
6. 在业务装配中把 `inf.outbound` 注入需要调用下游的服务并复用。
   若启用 Linux integration 测试，恢复 `cmd/plugins_integration_test.go` 的 OUTBOUND import 和配置清空行。

派生项目修改了 module 名时，注释内 import 也要手动更新。
如果不走根配置而直接构造，请先调用 `cfg.Validate()` 并处理错误。

```powershell
gofmt -w internal/bootstrap internal/platform/config
go build ./...
go vet ./...
go test -race ./internal/optional/outbound/...
go test -race ./internal/platform/config -run 'Test(Outbound|ShippedExample|ExampleCoversConfigStruct)'
```

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

## 删除

默认未接线，可直接删除本目录，再执行 `go mod tidy`。
`make build` 构建主程序时不会编译本包；`go build ./...` 仍会编译本目录。

需要清理模板时删除 `internal/bootstrap/outbound.go`，
以及根配置、providers、集成测试和 YAML 中的 OUTBOUND 注释。
`internal/platform/config/outbound_optional_test.go` 不导入本模块，删目录后仍可运行，
也可随模板一起删除。最后移除 SCAFFOLD 和 docs 中的相关说明。
