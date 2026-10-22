# 非必要反射清理方案与实施记录

> 历史记录：以下路径、命令和验收结果按当时版本保留，部分文件现已迁移或移除。当前基础组件采用显式 Open/Close 装配，见 [当前架构](architecture.md) 和 [简化方案](architecture-simplification-plan.md)。

日期：2026-10-22（Asia/Taipei）。来源：用户指出 `internal/pkg/log/logger.go:109–115`，要求整个项目能不用反射的地方坚决不用。本次独立处理该要求，不进入通知通道方案阶段 2。

当前状态：R1–R3 已完成，命令、证据及剩余反射边界见本文执行记录。

## 目标、事实与边界

- 优先明确类型、普通比较和标准库泛型集合操作；消除可直接替代的显式反射，不把反射转藏到新 helper 或第三方包。
- 保留现有接口、typed-nil 拒绝、插件替换和资源清理契约，不改目录、依赖方向、配置、存储、日志输出格式、通知接线或供应商版本。
- 当前项目声明 Go 1.27.0；go-llm-provider/v2 v2.15.0、logger/v2 v2.3.0、GORM v1.31.2 均维持锁定。workspace 与 GOWORK=off 分开验证。
- 已读取全局改动规则、错误记录及本次相关源码、指南、go-review/code-simplifier/use-modern-go/go-testing/go-enterprise-quality。错误记录中其他任务的开放项不由本任务处置。
- 全项目 Go/模板直接搜索发现 7 处 DeepEqual，操作数均为 []string 且期望序列非空；另有 AI 类型名检查、GORM 固定命名空间查询，以及下表需保留的结构/动态能力检查。检索不等于审查了所有依赖内部实现。

## 改动清单与等价性依据

| 文件 | 具体改动 | 兼容与验证依据 |
|---|---|---|
| 历史路径「internal/core/plugin_registry_test.go」（已迁移或移除） | 3 处 DeepEqual 改为 slices.Equal，删除 reflect import | 均为非空期望的字符串事件序列；保留长度、顺序、内容要求；不更改并发夹具或生命周期实现 |
| `internal/bootstrap/workers_rollback_test.go` | 1 处字符串序列比较改 slices.Equal | 保留逆序和不触碰旧回调的断言，不更改回滚实现 |
| `internal/pkg/news/news_test.go` | 1 处图片通知目标序列比较改 slices.Equal | 保留非空目标、顺序和消息内容要求，不改发送行为 |
| `internal/router/middleware_order_test.go` | 1 处比较改 slices.Equal | 保留中间件精确顺序；用于取函数身份的反射不改 |
| 历史路径「internal/plugin/builtin/httpmiddleware/plugin_test.go」（已迁移或移除） | 1 处比较改 slices.Equal | 保留四种组合的精确中间件顺序与配置冻结校验 |
| `internal/runtime/module/ai/module_test.go` | 删除 reflect.TypeOf/typeName，改为对构造后 Chat 发出的请求检查路径与认证头 | v2.15.0 的两种实现均为未导出类型，不能直接类型断言；用注入 fake RoundTripper 截获请求，无外网、无真实 API Key。校验 DeepSeek 的 OpenAI 路径与 Anthropic 原生路径，不以类型名代替协议行为 |
| `internal/platform/data/logcaller.go` | GORM 命名空间固定为 `gorm.io/`；移除对应 TypeFor 和无用 import；项目 adapterPackage 直接初始化 | 当前导入路径为 gorm.io/gorm，旧表达式取首个斜线前缀的结果逐字相同；项目包路径仍从类型派生，保持 gonew、trimpath、replace 兼容，不硬编码项目 module 路径 |
| `internal/pkg/log/logger.go` | 仅解释 Install 中反射用于 typed nil 且不在写日志路径 | 保留 ErrInvalidLogger、任意 Logger 实现、绑定/释放、原子状态和原有判空结构；既有 TestLoggerBindingOwnershipAndNil 回归验证 |

`slices.Equal` 最低 Go 1.21，本项目版本满足。它将 nil 和空切片视为相等；上述 7 个断言的期望均非空，因此不改变这些断言的判定，不推广为全仓盲替换。

## 保留项与理由

| 位置 | 保留原因 |
|---|---|
| logger.Install、core.isNilPlugin、bootstrap.nilCapability、MySQL/Redis/cache 工厂边界 | 对任意接口实现拒绝 typed nil；`value == nil` 不等价。保留具体实现未知时的最小动态检查，不改成 unsafe 或限定某几个指针类型 |
| data.adapterPackage 的 TypeFor | 获得本项目真实包路径，避免固定母版 import 路径破坏派生项目；仅包初始化执行 |
| router/httpmiddleware 测试的 ValueOf(handler).Pointer | 当前测试要锁定闭包身份及顺序，函数除与 nil 比较外不能直接比较；不把同一反射藏进 Gin 包装调用 |
| config/example_coverage_test.go 的结构与 tag 遍历 | 自动发现新增配置字段与样例遗漏；静态字段清单会弱化现有门禁。本次不引入代码生成或 AST 扫描体系 |

## 分步实施与验收计划

| 步骤 | 前置条件 | 动作与检查方法 | 交付/状态 | 停止条件 |
|---|---|---|---|---|
| R1 | 当前用户指令与规则已回读 | 保存工作树差异/目标文件副本；双环境运行相关包现有 race 基线，保留命令、exit、fail/skip | 已完成；双环境各 8 包、499 测试/子用例通过，fail/skip=0 | 基线失败属于阶段外且阻断本次比较；无法区分既有改动 |
| R2 | 本方案保存并回读，R1 有真实证据 | 按 8 份 Go 文件清单精确修改；逐段回读与快照比较；不改错误、锁、注册或发送实现 | 已完成；去掉 7 处 DeepEqual、1 处 TypeOf 和 1 处固定前缀 TypeFor | 需要改业务行为、接口、依赖或目录；必须弱化 typed-nil/配置覆盖门禁 |
| R3 | R2 完成 | 双环境相关完整 race；全仓 build/vet/lint（lint 串行）；相关 modern 与架构检查；核对剩余反射用途、GORM 前缀与 AI 请求断言 | 已完成；双环境各 8 包、501 测试/子用例通过，所有本轮检查 exit 0 | 本次引入失败或现有阶段外门禁阻断；同一问题三轮无进展 |

相关测试包固定为：`./internal/core ./internal/bootstrap ./internal/pkg/log ./internal/pkg/news ./internal/router ./internal/plugin/builtin/httpmiddleware ./internal/runtime/module/ai ./internal/platform/data`；命令为 `go test -json -race -count=1 -timeout=5m` 后接上述包，两个环境分别执行。测试阶段如新增 AI 两个子用例，数量增加须记录原因，不伪称前后完全相同。

本次为行为保持的初始化表达式与测试调整，没有产品行为、接口或数据结构变更，不创建/续跑 OpenSpec，不修改通知方案或其任务状态。未新增/升级依赖，不运行漏洞升级、容器、数据库操作或真实通知/模型请求。

## 风险、失败处理与回滚

- AI 请求测试必须真的经过 fake transport，先检查收到请求，再断言 POST、路径与认证头；返回受控 400，要求 Chat 返回错误，避免本地输入错误让测试假通过。只证明本地协议装配，不证明供应商端到端兼容。
- 不把初始化反射减少写为吞吐/延迟收益；本次无性能实测，不给性能数字。
- 原有工作树差异先保存。回滚仅按独立副本撤销本轮行，不执行 reset/clean、不还原其他文件、不修改别的任务记录。未演练回滚就不写已回滚。
- 失败保留原日志与真实 exit；本任务问题在授权内处理，其他任务问题只报告，不顺带修复或降低门禁。

## 实际执行记录

R1 已完成。证据目录：`/var/folders/_w/xwv4k3t12vn9054592gsk5200000gn/T/gin-api-reflection-uhwdus2d`；已保存 445 个 Go 文件及 6 个相关元数据文件的 SHA-256、8 份目标文件副本、工作树状态和目标原有差异。修改前复核无同期漂移。

环境为 go1.27.3 darwin/amd64，GOFLAGS 为空。2026-10-22 13:52:26 +0800 开始上述相关包 race 基线，workspace 于 13:53:31 完成、GOWORK=off 于 13:53:25 完成，均真实 exit 0，各 8 包、499 测试/子用例通过，失败与 skip 均为 0。完整参数、环境、UTC 开始/结束和计数保存在 `baseline-workspace.json`、`baseline-locked.json`，原始日志为同名 `.log`。这些是本轮新基线，未复用通知阶段的结果；建立此基线时尚未执行 R2/R3。

### R2 实际差异与保留边界

只修改清单中的 8 份 Go 文件，实际相对本轮快照的差异保存在 `reflection-only.diff`。7 处字符串序列断言已改为 slices.Equal；AI 原有顶层测试名保留，增加 deepseek/anthropic 两个子用例，经实际 Chat 调用截获请求，断言 POST、路径及认证头，并验证受控 400 返回错误；删除 typeName 和该文件 reflect import。GORM 固定前缀改为相同值的常量，项目 adapterPackage 仍动态派生。logger.Install 只增加用途注释，没有删除或改写判空分支。

重新检索后，项目中没有显式 `reflect.DeepEqual` 或 `reflect.TypeOf` 调用。反射 import 从 14 份 Go 文件减为 10 份：6 处接口/工厂 typed-nil 边界，1 处项目包路径派生，2 处中间件函数身份测试，1 处配置结构覆盖测试。逐项理由见“保留项与理由”；没有将反射转藏到 fmt 类型名格式化、通用 helper、第三方工具或 unsafe。

451 项快照范围内，除本任务 8 份 Go 文件和本文外无变化；go.mod/go.sum/go.work、通知方案及其 OpenSpec tasks 与基线逐字一致。修改后 SHA-256 保存在 `post-edit-sha256.json`，检查结束再次确认未变化。未新增 Go 文件，没有改变插件校验/装配、通知阶段状态或其他任务的错误记录。

### R3 实际命令与结果

所有命令在项目根目录执行，设 `GOTOOLCHAIN=local`；workspace 的 GOWORK 为 `/Users/xiaozhaofu/go/src/Project/gin_api/go.work`，locked 为 `off`。以下命令分别在两环境执行；完整展开参数、开始/结束 UTC、耗时及真实退出码保存在证据目录的同名结果 JSON，原始输出为同名 `.log`。

```text
/usr/local/bin/go build ./...
/usr/local/bin/go vet ./...
/Users/xiaozhaofu/go/bin/golangci-lint run --allow-serial-runners --timeout=5m ./...
/usr/local/bin/go test -json -race -count=1 -timeout=5m ./internal/core ./internal/bootstrap ./internal/pkg/log ./internal/pkg/news ./internal/router ./internal/plugin/builtin/httpmiddleware ./internal/runtime/module/ai ./internal/platform/data
/usr/local/bin/go fix -diff ./internal/core ./internal/bootstrap ./internal/pkg/log ./internal/pkg/news ./internal/router ./internal/plugin/builtin/httpmiddleware ./internal/runtime/module/ai ./internal/platform/data
bash scripts/check-architecture.sh
```

lint 两环境顺序执行；已核对工具 help，`--allow-serial-runners` 在共享锁上排队，不允许并行绕过锁，也没有修改 linter 配置。

| 检查 / label | workspace（2026-10-22 +0800） | locked（同日 +0800） |
|---|---|---|
| 全仓 build / after-build-* | 13:55:05–13:55:43，exit 0 | 13:55:05–13:55:43，exit 0 |
| 全仓 vet / after-vet-* | 13:55:44–13:55:52，exit 0 | 13:55:43–13:55:52，exit 0 |
| 全仓 lint / after-lint-* | 13:55:05–13:56:01，exit 0、0 issues | 13:56:01–13:56:26，exit 0、0 issues |
| 相关 race / after-race-* | 13:55:52–13:57:00，exit 0；8 包、501 测试/子用例通过，fail=0、skip=0 | 13:55:52–13:57:08，exit 0；相同计数 |
| 相关 modern / after-modern-* | 13:57:00–13:57:05，exit 0、diff 0 字节 | 13:57:08–13:57:09，exit 0、diff 0 字节 |
| 架构 / after-architecture-* | 13:57:05–13:57:08，exit 0、architecture check OK | 13:57:09–13:57:12，exit 0、相同输出 |

`verification-summary.json` 比较实际通过的测试名集合：两环境均保留全部原有 499 项，仅新增 `TestNewProviderPicksTheNativePathForNonOpenAIVendors/deepseek` 与 `/anthropic` 两项，故回归为 501 项。没有删除、跳过或用新计数替代未执行项。本轮检查均首次 exit 0，无需追加新的失败记录；其他任务的开放记录不由本结果关闭。

### 逐项验收与合规摘要

| 项目 | 实际结论 |
|---|---|
| R1–R3 | 已完成；计划先保存/回读，基线后修改，修改后双环境检查和差异回审 |
| Go 现代写法 | slices.Equal（Go 1.21 起）满足本项目 Go 1.27；相关 go fix -diff 无输出 |
| 分层/接口/依赖 | 不改目录、接口、配置或依赖；两个环境全仓 build/vet/lint 通过，架构通过 |
| 错误处理 | typed-nil 拒绝、绑定冲突和原错误分支保持；fake transport 显式处理请求 Body.Close 的错误 |
| 并发安全 | 不改生产同步机制；原有相关 race 通过，新 fake transport 无后台任务 |
| 超时与资源 | 不增加生产外部调用/资源；测试客户端预算 1 秒，完全使用进程内 fake transport，不出网 |
| 敏感信息 | 只使用 `.invalid` 目标与 test-key；未读取真实凭据、无真实发送 |
| 覆盖与交叉核对 | 8 包相关用例全量基线/回归；原有测试集合完整保留；整段重读改动函数，核对现有调用链与保留项 |
| 结果边界 | 未跑完整 make check、覆盖率、integration/scaffold 标签或真实供应商；未测性能，不给吞吐/延迟或生产准入结论 |
| 回滚/交付 | 只保留本期快照和撤销方法，未实际回滚；未提交、发布，通知阶段 2 未启动 |

当前完成的是非必要显式反射清理。保留的反射都有具体边界用途；后续新增代码继续优先明确类型、类型断言、普通比较及泛型集合操作，不以方便为由引入通用反射路径。
