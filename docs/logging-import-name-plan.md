# logging 包导入名修正

> 历史记录：以下路径、命令和验收结果按当时版本保留，部分文件现已迁移或移除。当前基础组件采用显式 Open/Close 装配，见 [当前架构](architecture.md) 和 [简化方案](architecture-simplification-plan.md)。

来源：用户指出 `plugin.go` 的 `sdk` 别名语义不明，并明确要求立即改正。目标是沿用第三方包原名 `logger`；项目日志门面仍为 `applog`。

当前事实：`go list -f '{{.Name}}' github.com/gtkit/logger/v2` 实际输出 `logger`。本项目 Go 1.27.0、logger/v2 v2.3.0 不变；workspace 仍可能使用本机替换，需与 GOWORK=off 分别检查。

范围仅为 历史路径「internal/plugin/builtin/logging/」（已迁移或移除） 中五个文件：`plugin.go`、`options.go`、`plugin_test.go`、`hotpath_contract_test.go`、`benchmark_test.go`。删除 `sdk` 导入别名，将对应 `sdk.` 引用改为 `logger.`；hotpath 测试中同名局部变量分别改为 `instance`、`gormLogger`，避免遮蔽导入。保留函数名、实际类型、依赖、构造和关闭顺序、配置、日志及通知行为，不改目录或其他包，不进入通知阶段 2，不启动 OpenSpec。

| 步骤 | 前置条件 | 具体动作与验收证据 | 停止条件 | 状态 |
|---|---|---|---|---|
| N1 | 用户已授权，全局规则和错误记录已回读 | 保存上述文件独立副本，按本清单替换，gofmt 后逐份对照；包内无 sdk 导入或 sdk. 引用 | 名称冲突需改其他接口/包，或无法区分既有修改 | 已完成，见差异及范围记录 |
| N2 | N1 完成 | workspace 和 GOWORK=off 分别对 logging 包运行 build、vet、lint、现有 race 测试；记录真实命令、exit、fail/skip；不新增镜像实现的测试 | 本任务失败无法修正，或阶段外错误阻断 | 已完成，双环境均通过 |
| N3 | 检查结果可复核 | 回读差异和本文，更新结果；命名纠正和上轮未落实修改记录到错误日志，仅关闭本任务条目 | 未执行项被当作通过 | 已完成，ERR-20261022-069 已关闭 |

风险为导入被局部变量遮蔽或漏改限定名，由局部变量核对及编译检查验证。失败保留原日志，不顺带修复其他任务。回滚仅按独立副本撤回本次名称变化，不覆盖原有差异；不执行 reset/clean、提交或发布。没有接口、数据、存储、依赖或配置变化。

执行记录（2026-10-22）：先保存并回读本文和错误记录，再修改五份 Go 文件并 gofmt。独立副本逐份对照确认仅含导入、限定名和上述局部变量改名；450 份基线文件中仅指定五份 Go 文件改变，无新增 Go 文件，依赖文件和既有通知/反射计划未变。

本机 `/usr/local/bin/go` 为 go1.27.3 darwin/amd64；检查实际执行时间为 06:19:39–06:19:52 UTC，均设 `GOTOOLCHAIN=local`。下表每条命令分别在 `GOWORK=/Users/xiaozhaofu/go/src/Project/gin_api/go.work` 和 `GOWORK=off` 下执行，工作目录为项目根目录，包参数均为 `./internal/plugin/builtin/logging`。

| 实际命令（末尾追加上述包参数） | workspace | GOWORK=off |
|---|---|---|
| `/usr/local/bin/go build` | exit0 | exit0 |
| `/usr/local/bin/go vet` | exit0 | exit0 |
| `/usr/local/bin/go test -json -race -count=1 -timeout=5m` | exit0；1 包、27 测试/子测试通过，0 fail/skip | exit0；1 包、27 测试/子测试通过，0 fail/skip |
| `/usr/local/bin/go fix -diff` | exit0；无输出 | exit0；无输出 |
| `/Users/xiaozhaofu/go/bin/golangci-lint run --allow-serial-runners --timeout=5m` | exit0；0 issues | exit0；0 issues |

lint 保留排除规则未匹配的警告，没有修改其配置。原始命令、环境、起止时间、退出码及日志保存在 `/var/folders/_w/xwv4k3t12vn9054592gsk5200000gn/T/gin-api-logger-name-i7e39kei/` 的各检查 `.json` / `.log`；独立副本、`rename-only.diff` 和 `scope-report.json` 用于复核本次范围。

验收：原包名 `logger` 已用于五份源码/测试，`applog` 保留，局部遮蔽已处理。静态差异确认接口、分层、错误处理、并发、超时和资源生命周期逻辑未变；未引入敏感信息，现有测试完成双环境验证。未执行全仓门禁、基准性能测量或真实通知投递，不据此声称这些检查已通过；通知阶段 2、OpenSpec、提交和发布均未执行。
