# 指标、压测统计与 OpenAPI 契约修复方案

日期：2026-10-27（Asia/Taipei）
状态：四项修复及验收完成；Git 交付按本地 delivery.json 与远端引用留证。

## 来源、目标与边界

用户指出四项问题，经只读源码核对后授权：先落盘方案，再补齐回归测试、修复，验证通过后提交并 push GitHub，不再等待确认。本轮起点为 `a6d2b2bb8ba6311d74325b5d1c897505a8e1b912`，分支 develop，远端 gtkit。上一轮验证仅为历史证据，本轮重新验收。

目标为统一池等待指标流、保证 runtime 样本不匹配时安全跳过、避免正常发压结束污染错误/延迟统计、支持合法 OpenAPI 路径级字段。非目标：不改业务路由、探针、认证、存储、架构、依赖版本、生产配置或指标后端；不引入 restyx、Prometheus 依赖或新公开扩展接口。不触碰用户原有 ADR 修改，不拉镜像、不创建容器、不连接生产。

已确认版本：Go 1.27.3、OTel API/SDK v1.47.0、go-redis/v9 v9.23.0、Gin v1.12.0；本机 golangci-lint 2.15.0。沿用 go.mod/go.sum，不升级依赖。

## 当前事实与具体设计

### 1. 池等待指标

`RegisterDBPoolStats` 的 `pool.wait.duration` 带描述 `Cumulative connection pool wait time` 和单位 `s`；Redis 同名仪表只有单位。锁定 SDK 把描述纳入流身份，同一 meter 的默认 reader 会产生重复流；项目示例遥测默认关闭，不声称默认服务已经发生导出故障。

最小修复是在 Redis 注册处增加完全相同的描述，保持已有名称、类型、单位和所有标签。回归在同一真实 SDK ManualReader 上联合注册 MySQL/Redis，验证整个 collection 只有一条该名称的流，且同时含两类池的正确值、标签；分别验证两种注册顺序以及注销一方后另一方仍可采集。不建立无调用方的公共仪表抽象。

### 2. Runtime 样本

当前使用 `/gc/pauses:seconds`；本机标准库明确将它标为废弃，推荐等价的 `/sched/pauses/total/gc:seconds`。所有 Uint64、Float64、Float64Histogram 读取目前未检查 Kind；不支持的名称返回 KindBad，直接读取会 panic。

替换 GC 样本名称，输出指标名和累计分位语义不变。逐项确认 Kind 后才观察；内存 used 只有 total 与 released 都为 Uint64 才计算，错误样本不按零输出；GC 样本无效时仅跳过 GC 两个输出，其余合法项继续采集。增加私有 `registerRuntimeStats(readSamples)` 测试入口，公开方法仍委托标准库 Read，不改变公开签名。它只用于向真实采集回调注入缺失/错误类型样本；先引入等价委托并写红灯回归，再实现保护。测试覆盖七个样本的 KindBad 与错误类型、全部缺失、正常输出及推荐样本名称。

### 3. 压测工具

当前 runCtx 同时控制生成窗口与请求，窗口到期会取消在途请求并计入 TransportErrors 和延迟桶。改为生成窗口独立的派生 ctx；请求从调用方 ctx 派生各自 `cfg.timeout`，窗口结束后关闭 jobs、等待在途请求完成。正常结束的额外等待最多为单请求超时；调用方取消仍立即传播，不使用 WithoutCancel。HTTP Client 原有 Timeout 保留。

保留原字段及含义：offered、sent、completed、generator_dropped、transport_errors、status、elapsed_seconds、scheduled_latency。新增 `scheduler_dropped`（漏过的计划发送时刻及结束时补记的漏发）、`worker_dropped`（非阻塞派发时无空闲 worker）；总数始终满足 `offered = sent + generator_dropped`、`generator_dropped = scheduler_dropped + worker_dropped`、收尾后 `completed = sent`。Completed 仍包括所有已结束请求；单请求超时仍算真实传输错误。`elapsed_seconds` 包含有界收尾；保留计划发送时刻延迟、固定桶和有界响应体。

为确定性模拟生成器调度迟到，增加私有 `runLoadWithClock(..., now)`，生产入口仍使用 time.Now；测试注入的 now 在首轮取时前用 synctest 暂停生成器，随后返回正常时间。不增加 CLI 参数、不修改其他全局时钟。测试覆盖：到期前开始的慢请求成功排空、请求超时、父级取消、worker 上限、两种丢弃独立计数及总数守恒、延迟仍从计划时刻计算；补一条真实 loopback HTTP 的窗口结束后收尾验证。最终自审补充边界：生成器取时期间可能跨过结束时刻，派发前再次核对窗口/取消，超期只补记调度漏发、不启动请求；增加跨完整窗口的确定性迟到回归。

### 4. OpenAPI 契约测试

现有 Paths 的值类型把所有路径级键都解码为 operation struct。合法的 summary/description 字符串和 parameters/servers 数组会在过滤之前解码失败。

在测试文件中抽取私有路由提取函数，先保存路径项为 `map[string]json.RawMessage`，仅对 get/put/post/delete/options/head/patch/trace 解码操作及 x-auth-mode。忽略路径级元数据和扩展字段，保留 3.1.0 版本检查、local/external 过滤与生产 engine.Routes() 双向对照。非法 JSON、非法已识别 operation 仍返回错误。增加合法路径级字段、八种操作、认证模式及非法输入回归。不改 OpenAPI 文档或生产路由。

## 文件范围

| 文件 | 改动 |
|---|---|
| `internal/pkg/metrics/redis.go`、`redis_test.go` | 描述统一、联合流与独立注销回归 |
| `internal/pkg/metrics/runtime.go`、`runtime_test.go` | 推荐名称、Kind 保护、私有采样入口与异常输入回归 |
| `scripts/load-api.go`、`load-api_test.go` | 生成/请求生命周期分离、丢弃分类、确定性与真实 HTTP 回归 |
| `scripts/README.md` | duration/timeout、收尾、新增字段与错误解释 |
| `internal/bootstrap/openapi_contract_test.go` | 只解码 HTTP 操作、元数据与错误回归 |
| 本方案 | 逐阶段真实证据与兼容说明 |

OpenSpec 建立独立 `fix-metric-and-load-contracts`，不接手无关活跃 change。指标与负载工具行为的 spec delta 在验收后正常合并；OpenAPI 测试维护写入同一 change 的任务。`.harness/error-journal.md` 和 OpenSpec 延续本地忽略规则。

## 阶段、步骤与验收

| 阶段 | 前置条件 | 具体步骤 | 验收证据/交付物 | 停止条件 | 状态 |
|---|---|---|---|---|---|
| 0 方案与准备 | 当前授权、源码与错误记录已读 | 保存/回读方案；核对 ADR 指纹；补齐 OpenSpec artifacts | 本方案、apply ready | 事实或范围冲突则先核对 | 已完成 |
| 1 红灯回归 | 阶段 0 完成 | 增加必要私有等价委托；添加联合指标、异常样本、负载收尾/丢弃、路径级字段测试 | 分组红灯日志，明确每项失败原因 | 意外环境问题不冒充缺陷证据 | 已完成 |
| 2 实现与模块验收 | 回归已保存 | 按四项设计逐项修复，更新工具说明；每项跑对应整包/显式文件清单普通测试 | 指标包、bootstrap、工具测试日志及精确 diff | 同一问题连续自纠 3 次仍失败则报告，不扩展业务范围 | 已完成 |
| 3 全体验证 | 四项功能完成；用户已明确授权验证 | 产品 build、全量 vet/modern/lint/架构、全量 race+coverage；工具显式文件清单 race/build/errcheck；当前 OpenSpec strict | 全部真实退出码；覆盖率 ≥72.5%，modern 空输出，lint 0 issues | 失败先定位，不降低门禁、不跳过失败用例 | 已完成 |
| 4 交付 | 阶段 3 通过 | 回读修改与文档，归档当前 change；仅暂存范围内文件；commit、push gtkit/develop，核对远端哈希和 ADR | Git/归档输出，本地 delivery.json | 不强推、不覆盖用户改动 | 交付材料已备，结果以实际命令留证为准 |

命令和日志目录：`.cache/metrics-load-repair-20261027/`。代码测试使用 `-vet=off`，vet 在功能完成后单独跑；工具使用 //go:build ignore 的显式文件清单。

```sh
GOTOOLCHAIN=local go test -vet=off -count=1 -timeout=5m ./internal/pkg/metrics ./internal/bootstrap
GOTOOLCHAIN=local go test -vet=off -count=1 -timeout=2m scripts/load-api.go scripts/load-api_test.go
GOTOOLCHAIN=local go build ./...
GOTOOLCHAIN=local go vet -tags scaffold ./...
GOTOOLCHAIN=local go fix -diff -tags scaffold ./...
GOTOOLCHAIN=local golangci-lint run ./...
scripts/check-architecture.sh
GOTOOLCHAIN=local go test -vet=off -tags scaffold -race -count=1 -timeout=5m -covermode=atomic -coverprofile=.cache/metrics-load-repair-20261027/coverage.out ./...
scripts/check-coverage.sh .cache/metrics-load-repair-20261027/coverage.out 72.5
GOTOOLCHAIN=local go test -vet=off -race -count=1 -timeout=2m scripts/load-api.go scripts/load-api_test.go
GOTOOLCHAIN=local go build -o .cache/metrics-load-repair-20261027/load-api scripts/load-api.go
GOTOOLCHAIN=local golangci-lint run --enable-only=errcheck scripts/load-api.go scripts/load-api_test.go
```

## 兼容、风险与回滚

- 运行指标名称/单位/标签不变；修复后的池等待流合并，runtime 不可用数据缺测而非报零。
- 压测新增两个 JSON 字段，保留旧字段；已有只读旧字段的消费者兼容，严格拒绝未知字段的消费者需要同步。duration 表示发压窗口，进程总时长可增加到 duration+timeout（父级更短时服从父级）；文档明确这个行为变化。
- 合成时钟与替身只验证统计/取消模型；真实 loopback 验证标准 HTTP 传输和响应体收尾。测试不宣称真实生产吞吐或 Prometheus 后端验收通过。
- 不做数据库迁移。失败按本地错误记录留证；回滚用本次提交的精确反向补丁恢复上述文件，不 reset --hard、不改变 ADR、依赖或配置。

## 实际结果

- 阶段 0：OpenSpec apply ready；ADR SHA-256 `0c5ee44595ca4847380c73ea8bd70a716a98d2219175f7a87fb35e9033e0dbe1`，不修改或提交。
- 阶段 1：`red-metrics.log`、`red-load.log`、`red-openapi.log` 三组命令均 exit 1，实际复现重复流、废弃样本/类型 panic、到期截断/缺少丢弃分类、合法路径元数据解析失败。红灯来自已核实缺陷，不是编译或环境失败。
- 阶段 2：metrics 整包 0.892s、bootstrap 整包 5.931s，合并命令 exit 0（`modules.log`）；独立工具文件清单普通测试 1.355s，exit 0（`load-tests.log`）。包括真实 loopback 响应体跨窗口排空、两种池注册顺序及注销、所有错误 ValueKind、安全缺测与 OpenAPI 元数据/非法操作回归。
- 阶段 2 完成时尚未执行全体验收，下表记录随后完成的阶段 3 结果。Git 交付以实际命令和远端引用留证，不在文档内嵌自身提交哈希。
- 首轮 build/vet/modern/lint/架构、全量 race+coverage、工具 race/build 均已 exit 0；工具最终自审补充上述跨窗口迟到用例。该用例与修复仅修改 build-ignore 工具，库/服务源码及其全量验证输入保持不变；最终重跑工具整组验证后再定稿。
- 跨窗口迟到回归先 exit 1（`red-window-edge.log`）：1 秒窗口结束后恢复调度仍发送 1 个请求。修复后工具全套普通测试、race、build 与 errcheck 均重新 exit 0；迟到用例现在只报告 10 次调度漏发、发送 0 次请求。

本轮最终检查（日志均在 `.cache/metrics-load-repair-20261027/`）：

| 检查 | 实际结果 | 日志 |
|---|---|---|
| 产品 build | exit 0 | `build.log` |
| 全量 vet | exit 0 | `vet.log` |
| 全量 modern | exit 0，输出为空 | `modern.log` |
| 全量 lint | exit 0，0 issues | `lint.log` |
| 架构 | exit 0 | `architecture.log` |
| 全量 race + coverage | exit 0，48 个测试包通过，0 DATA RACE；未跳过旧 readiness 用例 | `race-coverage.log` |
| 总覆盖率 | 84.7% ≥ 72.5%，exit 0 | `coverage-check.log`、`coverage.out` |
| 最终工具普通/race | 均 exit 0，分别 1.446s / 2.798s | `tool-tests-final.log`、`tool-race-final.log` |
| 最终工具 build / errcheck | 均 exit 0，errcheck 0 issues | `tool-build-final.log`、`tool-lint-final.log` |

未新增 lint 排除规则；工具最终改动不在产品 `./...` 构建集合中，其实际源码由显式文件清单整组复核。已回读修改函数、两处调用方、配置来源、文档及兼容边界；没有改公开 API、依赖、DB 写入或业务权限。真实 loopback 覆盖标准传输/响应体收尾，SDK ManualReader 覆盖联合流；未声称 Prometheus/Collector 后端或生产容量实测通过。新 JSON 字段与 elapsed_seconds 的收尾语义已同步脚本说明。

OpenSpec 当前 change strict validate 为 1 passed / 0 failed。归档输出确认更新两个对应 spec（3 项新增、1 项修改、0 项删除），并已分别 strict validate 通过；归档位置为 `openspec/changes/archive/2026-10-27-fix-metric-and-load-contracts`。日志为 `openspec-validate.log`、`openspec-archive.log`、`spec-runtime.log`、`spec-tooling.log`。OpenSpec 与错误日志沿用仓库既有忽略规则，不混入代码提交。

提交范围为上列 8 个源码/测试/说明文件和本方案。最终 commit、push、远端哈希及 ADR 指纹核对保存在 `.cache/metrics-load-repair-20261027/delivery.json`。
