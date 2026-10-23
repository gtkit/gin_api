# 第 3、4、6、7 项方案与实施计划

## 需求、授权与基线

需求来源：用户要求先为第 3（出站关联）、4（Redis 降级说明）、6（任务执行指标）、7（数据库 context 约束）项保存完整方案和实施计划，再按计划实施，同时修正数据库文档残留的 AI 示例。第 1（全局请求超时）、2（熔断扩展）、5（OpenAPI）项暂不实施，其他问题只汇报。

基线 commit：`4e93cfab8765b6106db48b0cef3d7504983c1bbf`。本轮开始时跟踪文件无修改；已有未跟踪 REVIEW.md、REVIEW-my.md 和两份旧计划，均保留。运行时为本机 Go 1.27.3 darwin/amd64。已读取全局规则、项目 AGENTS、错误记录及本次相关源码/guide。

本次实现授权不包含 lint、脚本 lint、vet、modern、全量 race 和覆盖率门禁；全部功能完成后另行询问。普通构建与本次测试可执行，`go test` 显式 `-vet=off`。不恢复上一轮已中断的检查，不连接数据库或启动/创建容器，不提交/push。

## 现状与外部耦合

| 项目 | 已核实事实 | 保持的契约 |
|---|---|---|
| HTTP | Factory 用 gtkit/httpc v1.6.1 创建具名客户端，已有超时、连接数、响应体上限和 CloseIdleConnections | 不更改任何预算、重试、熔断、连接池参数或响应体处理 |
| Request ID | 入站生成新的内部 ID，存于标准 context；不信任来路 header | 出站从当前 context 取 ID；不修改入站信任策略或把请求 ID 放入共享默认 header |
| Trace | 已锁定 OTel API/SDK v1.47.0，入站 tracing 与 propagator 已存在；出站未注入 | 只传播有效 W3C trace context，不新建 exporter、不改变采样，不增加全局请求超时 |
| Redis | Required 由 local auth、fail_closed Redis 限流、tasks 等能力推导，不是手工总开关 | 不更改 fail-open/closed、缓存、撤销或恢复行为；只补事实说明 |
| 限流 | failure_mode 必填；有 fail_open、fail_closed、local_fallback；启动可降级与运行恢复路径不同 | 表明启动无客户端时的恢复限制，不承诺自动重连 |
| 缓存 | Redis 启动不可达时本进程禁用缓存；正常客户端的缓存读写有 1s/2s 超时；读失败回源 | 不新增回源保护或新的配置项 |
| 任务 | WorkerManager 区分 OneShot/LongRunning；singleton 有 Observer；asynqx v1.6.0 支持 WithMiddleware | 不改变任务执行次数、租约、重试、ack、panic 传播、启动/关停顺序 |
| 现有指标 | worker.runs 是旧计数，singleton 的租约失败也会计入；RegisterWorkerHealth 是可用 API | 保留旧指标口径，新执行指标精确计一次实际运行；不把租约获取/释放计入执行耗时 |
| 任务中间件 | asynqx 的中间件类型来自已有间接依赖 hibiken/asynq v0.26.0 | 仅将该已锁定版本提升为直接依赖，不升级/引入新版本 |
| 仓储 | 查询走 mdbCtx → dbtx.DB → WithContext；脚手架生成同样入口 | 不修改查询/事务实现；加强开发期检查 |
| 现有传感器 | check-architecture.sh 只做文件文本匹配，mdbCtx 出现时检查固定字符串 | 用类型与 AST 识别上下文来源，沿用 scripts 目录和架构检查入口 |
| 文档残留 | docs/db-patterns.md 的示例注释仍指向已删除的 AI 仓储 | 替换成现存 useraccount 示例，不还原 AI 代码 |

未确认事实：外部部署的采集后端、业务 SLO、生产 Redis/DB 状态及下游对自定义 header 的处理，均不作为已验证结论。

## 实施方案

### 第 3 项：出站 trace 与 Request ID

在 `internal/pkg/httpclient` 添加薄 RoundTripper，由 Factory 在所有原连接池选项设置完成后包装原 transport。每次请求复制 request/header，再从请求 context 注入 `propagation.TraceContext`（traceparent/tracestate）和非空的内部 `X-Request-ID`。

- 保留原请求对象与 header map；并发请求不得串 ID。
- 无有效 trace/span 时不生成伪造 trace；无内部 ID 时不自动生成 ID。保留调用方显式 header，context 中有有效对应值时覆盖对应 header。
- 只注入 W3C TraceContext，不自动转发全部入站 header、Authorization、Cookie 或任意 baggage。
- 原 transport 的 RoundTrip、CloseIdleConnections 继续执行；请求取消、deadline、请求体所有权不改变。
- 对重定向沿用原 http.Client 规则，不新增跨域凭据转发规则。内部 ID 不用作认证依据。
- 不添加客户端 span、不增加依赖；现有 exporter/采样/入站 middleware 不动。

测试：内存 RoundTripper 校验 header/context、无 context、并发隔离、原对象不变、错误透传与关闭转发；通过实际 Factory 客户端和 httptest 内存服务验证有效 trace 与 ID 到达下游。

### 第 4 项：Redis 故障与恢复说明

在 SCAFFOLD.md 增加能力矩阵并由 README 链接，逐项写清：未配置、启动不可达、运行中故障、恢复条件、HTTP 结果。覆盖限流三种 failure_mode、local auth 撤销、Redis 响应缓存、任务队列的 Required 判定。指明 local_fallback 的本地桶状态与跨实例额度不连续；撤销故障是依赖不可用而非无效凭据；不新增撤销 fail-open 开关。

### 第 6 项：单次任务执行指标

新增 `worker.executions` 计数和 `worker.execution.duration` 秒直方图。标签仅为启动期确定的 `worker.name`、有限枚举 `worker.kind`（one_shot/singleton/queue）、`worker.outcome`（success/failure/canceled/timeout/panic）。队列按 consumer 名聚合，禁止用任意任务类型、task ID、用户 ID、payload 作标签。

- Recorder 提供单次执行记录与包装函数，记录一次计数及一次耗时。nil/no-op Recorder 仍执行原任务。
- 成功、业务失败、纯取消、纯 deadline、panic 分开；混合错误中的真实失败不被取消掩盖。panic 记录后继续原有传播/恢复流程；`panic` 统计未正常返回的执行，也包含 `runtime.Goexit`，文档和测试明确这个边界。
- WorkerManager 仅包装 required/optional OneShot 的 Run；LongRunning 的完整 Run 生命周期不计为一次任务执行。由 buildWorkers 注入既有 Recorder，旧构造签名不变。
- 队列通过 asynqx.WithMiddleware 包装每次处理尝试，保持重试/解码/错误返回；增加保留原 NewConsumer 的 observed 构造入口用于 bootstrap 注入 Recorder。
- singleton 在实际 task 函数边界增加可选 ExecutionObserver 回调（结束时给 context、名称、耗时、错误和 panic 标志）；旧 Observer 接口和既有日志/租约事件不变。bootstrap observer 把该事件写入新指标。
- 旧 worker.runs 保留原口径，文档明确它与实际 executions 的区别。没有真实任务时不创建虚假的执行样本。

测试：ManualReader 检查 counter/histogram 数量、标签和值；成功/失败/取消/deadline/混合错误/panic；并行请求隔离；OneShot 与 LongRunning 区分；singleton 未获租约不计执行、任务 panic 仍传播；队列中间件的真实调用边界与错误保留。不依赖生产 Redis。

### 第 7 项：仓储 context 检查与文档

新增 `scripts/check-repository-context.go` 与同目录显式文件测试，使用已有 golang.org/x/tools/go/packages v0.52.0 的类型信息及 AST。默认检查 `./internal/repository/...` 的生产文件；`internal/repository/dbtx` 是唯一底层实现豁免。接入现有 check-architecture.sh，替换原 mdbCtx 字符串检查，不移动目录或改变层级。

- 核对 mdbCtx 返回统一 dbtx.DB，且传入自身 context 参数与底层 DB。
- 检查 GORM 调用接收者来源：允许已核验的 mdbCtx、dbtx.DB、从其派生的查询链/局部变量以及受管 Transaction 回调句柄；拒绝裸字段、裸 DB 参数及其别名直接发起查询。
- 不把注释/字符串/同名非 GORM 类型当成调用；类型加载失败作为失败报告，不能当空结果通过。
- 检查只证明加载到的 Go 生产代码与可识别局部来源，不宣称覆盖反射、unsafe、跨进程或第三方内部行为；不能证明来源时报告，不静默放行。
- 文档说明统一入口兼顾取消与事务，不要求在每条查询重复 WithContext；修正 docs/db-patterns.md 的 AI 示例路径。

测试：受控 AST/type fixtures 包括标准入口、局部别名、事务回调、不同导入别名、裸 Find/Save/Transaction、错误 helper、context.Background/TODO、注释与同名伪类型、未知来源。只运行这些功能用例；项目级 lint/架构门禁待功能完成后授权。

## 文件范围

- 出站：`internal/pkg/httpclient/factory.go`，新增 propagation.go / propagation_test.go，必要时补 factory_test.go。
- 任务：`internal/pkg/metrics/metrics.go` 及测试；`internal/pkg/tasks/tasks.go`，新增 execution_metrics.go / execution_metrics_test.go；`internal/pkg/singleton/runner.go` 及执行观察测试；`internal/bootstrap/providers.go`、workermanager.go、singleton_worker.go 及相应测试。
- 检查：`scripts/check-repository-context.go`、`scripts/check-repository-context_test.go`、`scripts/check-architecture.sh`。
- 依赖：go.mod/go.sum 仅按已有 asynq 版本的直接导入更新依赖声明；其他版本保持。
- 文档：README.md、SCAFFOLD.md、docs/outbound.md、docs/observability.md、docs/db-patterns.md、docs/workers-and-scheduling.md、scripts/README.md；本计划及本次 OpenSpec change。
- 错误记录：按规则记录本轮真实失败；不修改旧错误或顺手修复无关实现。

## 兼容性、风险与回滚

没有新增配置项、数据库结构、Redis key/TTL、对外 HTTP API 或目录分层变更。已有 constructor、Observer 和 worker.runs 口径保持。出站增加关联 header 是有意行为变更；它们不参与权限决策。

主要风险：transport 包装漏转发关闭；共享 header 被污染；指标重复记录/高基数；panic 或取消语义改变；传感器误报/漏报。以专门反例用例、类型识别和范围限定验证。禁止借测试问题修改第 1/2/5 项或其他业务逻辑。

发生失败先读实际输出，记录原因，只修本次引入的问题。同一问题 3 次修正仍失败停止并汇报。外部依赖或环境缺失时列未验证，不安装新工具或容器。

回滚：按本次补丁逆向恢复相关文件；删除仅本次新增文件；恢复原依赖声明及文档。保留用户既有文件，不 git reset/clean，不动数据库。实施前保存本次文件快照至临时目录并记录真实路径。

## 分阶段实施清单

| 阶段/步骤 | 前置条件 | 操作 | 验收方法/证据 | 停止条件 | 交付物 | 当前状态 |
|---|---|---|---|---|---|---|
| 1.1 方案 | 已读规则、源码、用户范围 | 保存/回读本计划；准备本次 OpenSpec 至 apply-ready；备份文件 | 回读文本、CLI ready、备份路径 | 边界与用户不符 | 本计划、change | 已完成 |
| 2.1 出站实现 | 1.1 完成 | 包装原 transport，注入 trace/内部 ID，转发关闭 | 相关普通测试、真实内存 HTTP 请求 | 需要改变池/超时/熔断 | 出站实现与测试 | 已完成 |
| 3.1 Redis 文档 | 已核对当前分支 | 添加故障/恢复矩阵与链接 | 文档逐项对照源码回读，不启动 Redis | 文档需假定生产状态 | 文档 | 已完成 |
| 4.1 指标基础 | 1.1 完成 | 新 counter/histogram、有限标签与包装 | ManualReader 反例测试 | 新指标破坏旧指标/任务结果 | Recorder 与测试 | 已完成 |
| 4.2 执行接线 | 4.1 完成 | OneShot、singleton、queue 边界接线，保留旧接口 | 任务数量、错误/panic/取消及无租约测试 | 需要改调度/重试/退出语义 | 运行接线与测试 | 已完成 |
| 5.1 仓储检查 | 1.1 完成 | AST/type 检查、接入脚本、修正数据库示例 | 显式测试文件的正反例，不执行全项目门禁 | 来源无法分析却被放行 | 检查器、测试、文档 | 已完成 |
| 6.1 普通验收 | 2–5 完成 | 构建、相关包及脚本功能测试、回读 diff | 实际命令、exit code、失败/未验证列表 | 发现无关问题不修 | 验证记录 | 已完成 |
| 6.2 收尾 | 6.1 完成 | 更新计划；校验/归档本次 OpenSpec；询问受控门禁 | 任务清单、CLI 结果与最终差异 | 未解决本次缺陷 | 交付说明 | 已完成本轮收尾；受控门禁待用户单独授权 |

## 实际执行记录

本计划已保存并完整回读；实施前快照位于 `/var/folders/_w/xwv4k3t12vn9054592gsk5200000gn/T/gin-api-observability-context-6m291v1q`，包含 17 个现有目标文件。

本次 OpenSpec change 为 `complete-observability-context-guards`，在应用代码修改前已达到 apply-ready。第 3/4/6/7 项已实现，相关文档已回读；没有修改第 1/2/5 项、Redis 策略、数据库查询或现有目录层级。

普通验证均使用本机 Go 1.27.3，环境前缀为 `GOWORK=off GOTOOLCHAIN=local GOPROXY=off GOFLAGS=`：

- `go test -vet=off -count=1 -timeout=3m ./internal/pkg/httpclient`：exit 0，0.877s。包含通过实际 Factory 客户端发送到 httptest 内存服务的请求；不代表生产下游已联调。
- 最终增加不同大小写 header 直接写入的反例后，同一 httpclient 命令重跑：exit 0，0.861s；原请求不变且没有重复关联字段。
- `go test -vet=off -count=1 -timeout=3m ./internal/pkg/metrics ./internal/pkg/tasks ./internal/pkg/singleton ./internal/bootstrap`：exit 0，各包 1.494s、2.516s、3.126s、6.398s。
- 新增真实 WorkerManager.Start/StopAll 双 OneShot 路径及 Goexit 反例后，重跑 `./internal/pkg/metrics ./internal/bootstrap`：exit 0，各包 1.714s、5.922s；任务次数与 Cleanup 次数保持。
- `go test -vet=off -count=1 -timeout=3m scripts/check-repository-context.go scripts/check-repository-context_test.go`：exit 0，1.803s；覆盖类型夹具的标准入口、事务、别名、重赋值、裸调用/方法值/方法表达式、错误 helper、取消丢失、加载失败与非 GORM 同名类型。
- `go mod tidy`：exit 0，离线使用已有缓存；只把已锁定的 asynq v0.26.0 从 indirect 提升到 direct，go.sum 无差异。
- `go test -vet=off -count=1 -timeout=3m -tags=scaffold ./internal/scaffold -run '^TestProvidersMarkersPresentInRealProvidersFile$'`：exit 0，0.783s；真实 providers 的扩展标记保持可识别。
- `go build ./...`：exit 0；最终 header 修正后再次执行，exit 0。
- `git diff --check`：exit 0。
- `openspec validate complete-observability-context-guards --type change --strict --json --no-interactive`：exit 0，valid=true，issues 为空。
- `openspec archive complete-observability-context-guards -y`：exit 0，CLI 确认新增 3 份 spec、6 条 requirement；已归档至 `openspec/changes/archive/2026-10-23-complete-observability-context-guards/`，回读任务清单全部完成。
- 对 `outbound-context-propagation`、`repository-context-guard`、`worker-execution-observability` 分别执行 `openspec validate <名称> --type spec --strict --json --no-interactive`：均 exit 0，valid=true，issues 为空。

完整架构入口、lint、脚本 lint、vet、modern、全量 race 与覆盖率均未运行，等待功能完成后的单独授权。

本轮读取路径失误、内存 HTTP 测试启动假设、补丁定位失败和 header 大小写缺陷已如实记录于 ERR-20261023-093～099，并在纠正/验证后关闭；仅修复本轮问题。

范围外发现：`docs/workers-and-scheduling.md` 的周期任务示例仍用 `cleanup:ai-conversations`。用户本轮只要求修正数据库文档残留，故保留并汇报。

## 本轮验收边界

按 backend-acceptance 对照需求与不变量：出站关联字段不改权限、不改原 request；指标不改变执行次数、错误或 panic 路径；Redis 仅说明现有行为；仓储检查不改 SQL、事务和存储。上述本地路径已有对应测试或源码/文档回读证据。

没有新增配置、迁移、权限、业务状态或数据库操作。本轮未启动服务或容器、未连接生产，未验证真实 Redis 队列投递、数据库取消、外部采集后端和生产容量。仓储检查器目前验证了类型夹具，实际项目扫描与完整架构门禁仍待授权。结论为实现与本地定向验证完成，不作生产准入声明。

## 追加阶段 7：已授权的完整检查

用户后续明确授权继续完整架构检查、lint、脚本 lint、vet、modern、全量 race 和覆盖率门禁；只修本次改动引入的问题，其他问题先汇报。此前“未授权/未运行”是上一阶段的历史状态，不代表本阶段授权。

基线仍为 `4e93cfab8765b6106db48b0cef3d7504983c1bbf`，本阶段开始 HEAD 未变、工作区为上一阶段交付内容。使用本机 Go 1.27.3、`GOWORK=off GOTOOLCHAIN=local GOPROXY=off`，不升级依赖或工具、不创建容器、不提交。新增检查产物仅为日志和 coverage profile。

已核实的前置风险：本机 golangci-lint 为 2.14.0，而 `security/toolchain.mk` 要求 2.15.0；不绕过版本门禁，不安装替代工具。仓库的脚本 lint 是 `scripts/*.go` 的 errcheck；ShellCheck 当前未发现，不以 shell 语法检查冒充 ShellCheck。旧 ERR-20261023-091 记录 cmd 默认配置测试五分钟超时，若重现只汇报；当前开发配置的 DB/Redis 为本机地址，不扩大为生产操作。

| 步骤 | 前置条件 | 操作与检查方式 | 验收证据 | 停止条件 | 状态 |
|---|---|---|---|---|---|
| 7.1 | 已回读规则、原计划和错误记录 | 记录授权、工具、命令及日志位置 | 本追加计划及真实工具版本 | 工具缺失不擅自安装 | 已完成 |
| 7.2 | 7.1 完成 | 分别运行 `make check-architecture`、`make lint`、`make vet`、`make check-modern`；检查脚本 errcheck 和 shell 语法 | 各自完整日志及退出码 | 版本不符则保留阻断；发现无关问题不改 | 架构/vet/modern/语法通过；lint 与脚本 lint 版本阻断 |
| 7.3 | 7.1 完成 | `make check-coverage`，使用独立 profile；其中 `go test -tags scaffold -race -count=1 -timeout=5m -covermode=atomic ./...` 同时覆盖全量 race，阈值保持 72.5% | 全部测试结果、profile 完整性、覆盖率比较 | 测试失败不把部分 profile 当全量通过 | 已执行：cmd 旧测试超时，覆盖率门禁被阻断 |
| 7.4 | 已有真实失败 | 对照基线和当前 diff 归因；仅本次引入的问题先补具体方案/文件清单再修复、重跑相关完整检查 | 失败原文、根因、修复差异和复验 | 来源不明或需越界则只报告 | 已核对：仅版本环境阻断与旧测试失败，未改源码 |
| 7.5 | 各检查已出结果 | 更新本计划、区分通过/失败/工具阻断/未验证，回读范围 | 完整汇报、未修存量问题 | 不追加无关清理 | 已完成记录与回读 |

若需修复，本阶段仍沿用原兼容约束及逆向补丁回滚方法；禁止降低门禁、跳过失败测试或修改无关实现来取得通过结果。

本阶段日志目录：`.cache/observability-context-checks-20261023-PYlrJR/`。各命令保持上述离线环境前缀，未改任何工具版本。

- `make check-architecture`：exit 0；输出 `repository context check OK`、`architecture check OK`。
- `make vet`：exit 0，包含 `scaffold` 构建标签。
- `make check-modern`：exit 0，输出 `OK go fix -diff`。
- `make lint`：exit 2，版本检查报告要求 2.15.0 / 实际 2.14.0；业务 lint 和 Go 脚本 errcheck 均未执行，记录 ERR-20261023-100。不使用旧版本绕过，不修改版本声明。
- `bash -n` 检查全部 `scripts/*.sh`：exit 0；只证明 shell 语法，ShellCheck 未在 PATH 中找到，未执行。
- 新增仓储检查器及其测试显式文件组的 `go vet`、`go fix -diff`：均 exit 0、无输出；`go test -race -count=1 -timeout=3m`：exit 0，1.748s。
- `make check-coverage COVERAGE_PROFILE=.cache/observability-context-checks-20261023-PYlrJR/coverage.out`：exit 2。`cmd/TestServerUsesDefaultConfigurationLoader` 五分钟超时，cmd 包 303.917s；其余有测试包均输出 ok，日志未出现 `WARNING: DATA RACE`。本次涉及的 bootstrap/httpclient/metrics/singleton/tasks 均通过 race + coverage 测试。
- 覆盖率阈值比较未执行：测试子命令失败后 make 停止，不能用产生的部分 profile 宣称全量覆盖率通过；阈值仍为 72.5%。
- 超时日志显示该旧测试启动了本机 `127.0.0.1:8009` HTTP 与 `127.0.0.1:6060` pprof，然后等待关停。这是阶段 7 的真实测试副作用，与上一阶段“未启动服务”的历史记录区分；未创建容器、未连接生产。
- 失败与旧 ERR-20261023-091 相同；对照基线确认 cmd/runtime_assembly_test.go、config/load.go 与 runtime/resource 无本次修改，记录 ERR-20261023-101。按用户范围保留该旧问题，不跳过测试、不修改配置回落/服务关停、不降低覆盖率门禁。

阶段 7 结论：架构、vet、modern、Shell 语法及新增检查器 race 通过；Go lint/脚本 errcheck 因版本漂移未执行；全量 race 因已知旧测试超时失败，覆盖率门禁未完成。未发现需要修复的本次新增失败，本阶段没有追加源码改动。
