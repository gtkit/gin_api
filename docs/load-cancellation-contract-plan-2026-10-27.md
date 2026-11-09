# 压测取消统计与 OpenAPI 拒绝边界修复方案

日期：2026-10-27（Asia/Taipei）
状态：实现与全部验收通过；Git 交付以本地 delivery.json 与远端引用留证。

## 来源与范围

用户已批准最终讨论方案，并要求本轮先落盘、实现并补齐回归，验证后直接提交和 push。明确限制：派发边界只改 README，不改调度实现；本轮不补 runtime 回归。起点为 `c34ec580adba306311b52a146dc3f0a4ffd352c8`，分支 develop，远端 gtkit。

前一轮方案为 `docs/metrics-load-contract-repair-2026-10-27.md`。本轮只修改 `scripts/load-api.go`、`scripts/load-api_test.go`、`scripts/README.md`、`internal/bootstrap/openapi_contract_test.go` 和本方案。不修改 runtime/指标、业务路由、依赖、配置、存储或架构；不触碰用户原有 ADR；不创建容器、不连接生产。

版本已核对：Go 1.27.3、Gin v1.12.0；golangci-lint 2.15.0。go.mod/go.sum 保持不变。OpenSpec 使用独立 change `clarify-load-cancellation-contract`，不接手其他 change。

## 当前事实

- 请求从调用方 ctx 派生独立 timeout；生成窗口结束后等待 worker 收尾。当前全部错误仍计入 transport_errors，全部请求都进延迟桶，CLI 输出 JSON 后返回 nil。
- CLI 的父级 ctx 来自 signal.NotifyContext，当前没有父级 deadline；直接调用/测试仍可能提供 deadline。本机 Go 源码确认信号 cause 可经 errors.Is 匹配 context.Canceled，HTTP/1 的取消错误保留 cause，Client.Timeout 可匹配 DeadlineExceeded。
- 终止原因必须在 workers.Wait 后读父 ctx，才能识别排空阶段取消。
- OpenAPI 当前将路径级 $ref 和 nil map 静默跳过；空对象是允许的。schema 内 $ref 不由该提取器解析。

## 最终行为

### 取消、统计与退出码

新增 JSON 字段：`canceled_requests`（uint64）和 `termination_reason`（completed / canceled / deadline_exceeded）。请求只有同时满足以下条件才归为调用方取消：err 非 nil、父 ctx.Err() 可匹配 context.Canceled、err 可匹配 context.Canceled。父级 deadline 不计 canceled_requests，仍作为传输失败；真正的 I/O 错误不能仅因父 ctx 后续取消而被隐藏。

`completed` 继续计全部已结束请求。收到响应头即保留 status 计数，包括随后取消或正文超时。调用方取消不计 transport_errors，也不进入延迟桶；真实失败和请求超时仍计错误并进入桶。验收不变量：

- completed = sent；offered = sent + generator_dropped。
- generator_dropped = scheduler_dropped + worker_dropped。
- 延迟桶计数之和 = completed - canceled_requests。
- status 各项之和 ≤ completed；status 与 transport_errors 不互斥。

workers.Wait 后根据父 ctx 的状态设置 termination_reason。CLI 先编码 JSON；编码失败优先返回编码错误。编码成功后，completed 返回 nil，canceled/deadline_exceeded 分别返回对应 context 错误，由现有 main 错误分支以 1 退出。参数错误同样退出 1，但没有结果 JSON；本程序退出码不代表 GNU timeout 等外层工具的退出码。

### 派发边界仅写明已有语义

保持调度循环代码不变。README 说明：计划发送时刻 ≤ 调度判定时刻 < 截止时间；抢占可能让一次已接受的派发延后完成，该延迟仍进入 scheduled_latency。请求 timeout 从 worker 开始处理时起约束生命周期，不承诺整个进程严格在 duration+timeout 的墙钟上限内结束。不会把 worker 改用生成窗口 ctx。

### OpenAPI

路径值为 null 或存在路径级 $ref 时明确报错（包括空/无效引用值），不解析引用。空路径对象 {} 继续允许。保留其他合法路径元数据、八种 HTTP 操作、x-auth-mode 过滤与生产 engine.Routes 双向对照；schema 内 $ref 不受影响。

## 阶段与验收

| 阶段 | 前置条件 | 步骤 | 证据与交付物 | 停止条件 | 状态 |
|---|---|---|---|---|---|
| 0 准备 | 最新授权、源码、错误记录已读 | 保存并回读方案，记录 ADR 与调度区间指纹，完成 OpenSpec artifacts | 本方案、apply ready | 事实/范围冲突时先核对 | 已完成 |
| 1 回归与实现 | 阶段 0 完成 | 添加必要 wire 字段供测试编译；新增取消分类/终止、CLI JSON、OpenAPI 边界回归并记录红灯；实现条件和退出语义，更新 README | 红灯日志、精确 diff、普通整包与文件清单回归 | 同问题连续自纠 3 次失败则报告 | 已完成 |
| 2 验证 | 全部功能完成，用户已授权验证 | bootstrap 整包；独立工具普通/race/build/errcheck；全量 build/vet/modern/lint/架构/race+coverage；本机二进制 SIGTERM 排空中止验证 | 命令、退出码、stdout JSON、覆盖率、指纹对照 | 不跳过失败用例、不放松门禁 | 已完成 |
| 3 交付 | 验证通过 | 回读函数与文档、strict validate 和归档；仅提交本轮文件并 push gtkit/develop，核对远端哈希 | Git/归档输出、本地 delivery.json | 不强推、不覆盖 ADR | 验收材料已备；Git 结果以命令留证为准 |

回归涵盖：生成期间和排空期间取消；响应头后取消；父级 deadline；请求在头/正文阶段真实超时；真实 I/O 错误与父取消交错；取消错误但父级未取消；CLI 中止 JSON/错误、编码失败优先和参数错误；OpenAPI 路径 null/$ref 拒绝、空对象和 schema 引用允许。runtime 不新增测试。

验证日志保存于 `.cache/load-cancellation-repair-20261027/`：

```sh
GOTOOLCHAIN=local go test -vet=off -count=1 -timeout=5m ./internal/bootstrap
GOTOOLCHAIN=local go test -vet=off -count=1 -timeout=2m scripts/load-api.go scripts/load-api_test.go
GOTOOLCHAIN=local go build ./...
GOTOOLCHAIN=local go vet -tags scaffold ./...
GOTOOLCHAIN=local go fix -diff -tags scaffold ./...
GOTOOLCHAIN=local golangci-lint run ./...
scripts/check-architecture.sh
GOTOOLCHAIN=local go test -vet=off -tags scaffold -race -count=1 -timeout=5m -covermode=atomic -coverprofile=.cache/load-cancellation-repair-20261027/coverage.out ./...
scripts/check-coverage.sh .cache/load-cancellation-repair-20261027/coverage.out 72.5
GOTOOLCHAIN=local go test -vet=off -race -count=1 -timeout=2m scripts/load-api.go scripts/load-api_test.go
GOTOOLCHAIN=local go build -o .cache/load-cancellation-repair-20261027/load-api scripts/load-api.go
GOTOOLCHAIN=local golangci-lint run --enable-only=errcheck scripts/load-api.go scripts/load-api_test.go
```

本机 SIGTERM 验收使用标准库 loopback HTTP 夹具与已编译工具，窗口结束后在正文阻塞期间发送信号，断言真实退出码 1、stdout 可解析、termination_reason=canceled、取消计数和延迟桶正确；同时核对参数错误没有 JSON。不安装工具或依赖。

## 风险、兼容与回滚

旧 JSON 字段保留；新增字段以及取消不再计错误/延迟是有意的契约变化。严格 JSON 消费者需要接受新字段，依赖总桶数=completed 的调用方需要改用上述不变量。中止后的退出码从 0 改为 1。所有说明同步 README。

只改变工具和测试，不改变业务服务运行行为。不以合成时钟/loopback 验证冒充生产容量结论。失败留存真实日志；回滚以本次提交精确反向补丁恢复，不重置工作区、不改数据库、配置或依赖。

## 实际证据

- 当前 change apply ready，完整方案已在代码修改前保存并回读。
- 新回归先失败：`red-load.log` 确认取消仍计错且 CLI 返回 nil；`red-openapi.log` 确认路径 null/$ref 被接受。两组命令 exit 1，均非编译或环境错误。
- 修复后独立工具普通测试 exit 0（1.407s，`tool-tests.log`），bootstrap 整包 exit 0（5.953s，`bootstrap-tests.log`）。
- 本机编译工具 exit 0，原生 SIGTERM 验证 exit 0（`native-cli.log`、`native-cli-result.json`）：实际子进程退出 1，JSON 的 canceled_requests=1、transport_errors=0、status[200]=1、全部延迟桶为 0；参数错误子进程退出 1 且无 stdout JSON。
- 调度区间与用户 ADR 指纹均保持不变（`baseline-fingerprints.json`）。runtime 文件未修改、未新增对应回归。
- 上述普通回归完成后才执行以下全量检查；本轮没有排除旧 readiness 用例，没有更改 lint 配置。

| 本轮检查 | 结果 | 日志 |
|---|---|---|
| 产品 build | exit 0 | `build.log` |
| 全量 vet / modern | 均 exit 0；modern 空输出 | `vet.log`、`modern.log` |
| 全量 lint | exit 0，0 issues | `lint.log` |
| 架构检查 | exit 0 | `architecture.log` |
| 全量 race + coverage | exit 0，48 个测试包通过，0 DATA RACE | `race-coverage.log` |
| 覆盖率门禁 | 84.7% ≥ 72.5%，exit 0 | `coverage-check.log`、`coverage.out` |
| 独立工具 race | exit 0，4.838s | `tool-race.log` |
| 独立工具 build / errcheck | 均 exit 0，0 issues | `tool-build.log`、`tool-lint.log` |
| OpenSpec strict validate | exit 0，1 passed / 0 failed | `openspec-validate.log` |

最后回读确认：调度循环至 workers.Wait 的字节指纹未变，runtime 文件无差异；取消统计只改变请求结束后的分类，终止原因只在 worker 完成后读取。JSON 写入错误优先，状态码保留与取消/失败分类不互斥，旧字段保留。没有依赖、接口路由、数据库或权限变更；本机 HTTP/SIGTERM 证据不作为生产容量或外部环境验收结论。Git 结果由提交及 `.cache/load-cancellation-repair-20261027/delivery.json` 留证。

归档输出确认本 change 的任务已完成，向 capacity-baseline-tooling 合并 2 项新增与 2 项修改、0 项删除；归档位于 `openspec/changes/archive/2026-10-27-clarify-load-cancellation-contract`。归档后的对应 spec 严格验证通过，日志见 `openspec-archive.log`、`spec-validate.log`。OpenSpec 和错误日志沿用仓库忽略约定留在本机。
