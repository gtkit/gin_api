# 日志与插件热路径性能优化方案及实施记录

> 历史记录：以下路径、命令和验收结果按当时版本保留，部分文件现已迁移或移除。当前基础组件采用显式 Open/Close 装配，见 [当前架构](architecture.md) 和 [简化方案](architecture-simplification-plan.md)。

日期：2026-10-21。授权来源：用户要求在 `pluggable` 分支接续已批准的性能优化，完成优化、验收与记录；允许在本轮范围内落盘完整方案后直接实施。立项授权不含 commit、push、tag、发布、其他分支或 worktree；2026-10-22 的提交授权与复核见第8节。

## 1. 立项时事实与目标（最终结果见第7节）

- 本轮实际核对：分支 `pluggable`，HEAD `4c8d7d7b582f847f579b86bef0896a1971ced29f`，开始时 tracked/untracked 工作区干净；忽略的旧工作文件不属于本轮，不覆盖它们。
- Go `go1.27.3 darwin/amd64`；go.mod `go 1.27.0`，logger/v2 `v2.3.0`、zap `v1.28.0`、Gin `v1.12.0`、GORM `v1.31.2`。不改依赖。普通 workspace 使用本地 logger/v2 和 mailx；另用 `GOWORK=off` 验证锁定依赖。
- 实读 历史路径「internal/plugin/builtin/logging/plugin.go」（已迁移或移除）：每次 Log 持 gate Mutex 选择 target、增加 writes WaitGroup；CallerSkip 非 3 时在锁内派生 logger；过滤调用 Zap()，实际 SDK 实现会 WithOptions 克隆。SDK WithCallerSkip/SetLevel/Sync 在本地和锁定源码均需继续核对；当前已核对两份 methods 的相关入口存在且 Zap 克隆一致。
- 实读门面、GORM、HTTP 中间件：门面只要求 Log；GORM 在 fc/callerSource/字段构建后才由后端过滤；HTTP 在脱敏、ClientIP、字段构建后过滤。Alert 经 HError，不能按普通 Error 过滤。
- configuration 使用 RWMutex 发布一次 cfg/err；MySQL/Redis/cache 的读锁还包住可替换 SDK getter/方法，与 Close 的并发契约尚未全部确认。
- 本轮基准、profile、正确性反证与最终门禁尚未执行。历史插件计划和归档保持原样。

目标：在兼容与正确性成立的前提下，以同条件 benchmark/benchstat 选择降低 ns/op、B/op、allocs/op 和热路径锁竞争的实现。过滤日志在同步、格式化及字段转换之前退出；调用方有可靠级别能力时，在昂贵计算之前退出。无需固定百分比门槛，以实测统计、复杂度与正确性共同判断。

非目标：不证明百万同时在途请求容量、不保证无 OOM；实例数、CPU/内存预算、请求时间、日志负载、生产 QPS/P99 尚未确认。不启动百万 goroutine，不新增限流/拒绝、采样、异步队列、日志默认级别、MQ、runtime plugin/so、JSON 插件化或版本迁移。不调整架构/目录、业务、配置、数据库结构、TTL/库号或依赖方向。

## 2. 范围、文件与兼容契约

| 文件 | 本轮允许变更 |
| --- | --- |
| internal/plugin/builtin/logging/plugin.go | 缓存 core 与有限 caller 变体；实测后决定原子 admission、固定字段缓冲池 |
| internal/pkg/log/logger.go | 保持 Logger.Log 必需接口；Install 探测可选 Enabled(Level, Stream)，新增查询并保留自定义实现兼容 |
| internal/platform/data/init.go | 在 fc、callerSource 与字段构建之前按实际日志分支查询级别 |
| internal/middleware/logger.go | c.Next 后按最终 status 查询级别，再做昂贵字段构建；body 捕获策略不改 |
| internal/plugin/builtin/config/plugin.go | 仅基准证明收益且一次发布 cfg/err 完整时，可采用 immutable atomic snapshot |
| 上述包现有/新增 *_test.go | 可复现 benchmark、行为与并发契约测试；不更改无关测试断言 |
| docs/logging-performance-plan.md、openspec 本轮独立 change/spec、.harness/error-journal.md | 方案、真实实施/验收/错误记录 |

MySQL/Redis/cache：本轮先完成契约检查；没有 getter/Close 并发依据时保留锁，并记录原因，不擅自改变可插拔实现的契约。

外部耦合清单（依据当前调用链，逐项保持）：

- 六类编译期插件及能力/owner 分离；注册顺序、失败回滚、幂等停止；Core rollback/stop 总预算各 30 秒，不改 Core/信号处理。
- 日志 Level/Stream/Field/LogEntry 字面量、格式参数、CallerSkip、Channel、Alert、主/访问流；request_id 上下文及 SQL trace_id；日志配置与路径/轮转、默认级别不变。
- 普通 facade CallerSkip=3 对应 SDK +4，GORM Trace=1 对应 +2，GORM emit=2 对应 +3，direct=0 对应 +1，access 恒 +2；其余 skip 按原规则临时派生，不建无限缓存。
- SQL ParamsFilter 只保留占位符，rows<0 不输出 rows，慢阈值 200ms，RecordNotFound 语义不变；HTTP 4xx Warn/5xx Error/其他 Info、query/body 脱敏和 4097 字节捕获边界不变。
- 默认 logger 的资源所有者和 previous 默认恢复；旧 release 不清除新 binding。日志同步无 context/错误返回，I/O 失败沿用 SDK stderr；不虚构硬 deadline。
- 没有新增 DB/Redis 连接、库号、键前缀、TTL、字符集或数据格式；本轮不修改这些耦合。

## 3. 立项时的候选与设计（最终取舍见6.8、7节）

| 候选 | 正确性与成本 | 取舍 |
| --- | --- | --- |
| A 保留 Mutex/WaitGroup，只缓存 core/caller、提前过滤 | 关闭协议不变，代码最少；启用并行日志仍争用 gate | 基准对照和可回退方案 |
| B 不可变日志 snapshot + 单 atomic.Uint64 admission | 关闭位与在途数同一 CAS，热路径无 gate Mutex；需反证 Stop 交错与 panic 释放 | 仅实测支持且契约通过时保留 |
| C 全插件直接换 atomic、无限 caller/channel 缓存、异步日志 | 可替换实现与关闭契约不足，内存/交付语义变化 | 不实施 |

候选B的原设计（最终未采用）：日志 snapshot 完整构建后发布，包含 root/forward/core。生命周期 Init/Stop 仍串行；snapshot 的关闭位与在途计数用同一 atomic 值：Log 先过滤，再 CAS 接纳；接纳后立即 defer 释放。Stop 原子封闭 admission、撤销自己的 facade binding，已接纳写入归零后关闭 drained channel，Stop 等待 channel 后依次 Sync access/main。无写入时 Stop 自行完成 channel；有写入时最后一个释放者完成。关闭与增加不能各用独立 bool/WaitGroup，不忙等、不为每条日志新建 goroutine。不删除对既有阻塞 SDK 的等待。

Enabled 是 advisory，动态读取 core 的级别能力而非缓存 bool，写入仍按最终选定的接纳协议复查（候选B为CAS，最终为gate+WaitGroup）；级别切换期间不声称跨调用的事务保证。自定义 Logger 无 Enabled 时返回可用，不替它定义过滤。Alert 绕过普通 Enabled。

字段先测：保留 zap.Any 的常见类型快路径。如字段 slice 成为可测分配，可用固定容量（最多 8 个 zap.Field）的 *buffer sync.Pool；无字段不取池，超过容量直接按原长度分配，Put 前 clear 全部引用，panic 路径也释放。SDK 仅同步消费字段的源码确认是前置条件。中立 Field 与接口不暴露 zap。

配置候选为一次发布 `{cfg, err}` 的 immutable snapshot；Value 的未初始化/失败语义和 Stop 后可读不变。MySQL/Redis/cache 若完整关闭契约不支持则不改。

## 4. 风险、失败处理、回滚与验收

- 风险：计数与关闭交错、panic 导致不归零、caller 栈帧偏移、动态级别失效、Alert 被抑制、池残留对象、替换 binding 误清除、SDK 版本差异。均由下列真实测试与源码回审验证；任一未解决则不保留对应优化。
- 测量风险：主机后台负载/电源状态不能固定，微基准不等于生产吞吐；记录已知环境与未知项。普通/锁定环境分别验证，不混用统计。race 数据不作性能结论。
- 失败时先记录真实输出与根因；同一问题连续 3 轮未解决停止相关实现并报告。外部权限、核心依赖或契约缺失不报告通过；不安装工具/拉镜像/建容器。
- 回滚：不涉及数据/配置迁移，可按本轮 diff 定向恢复生产实现至开始 HEAD，保留独立 benchmark/失败证据再复验；不使用 reset --hard，不覆盖用户新增修改。未获发布授权，不做部署。
- 本轮成功标准：存在相同测试源码/环境的 before/after（每组起始 count=10）及 benchstat 原件；保留的优化有统计收益或明确分配/锁收益且无未解释回归；过滤无格式化/转换、动态级别/Alert/caller/requestID/字段/流/脱敏、并发 Stop/重复 Stop/Init 失败/所有权反证通过；受影响 race、build、make check 与独立 OpenSpec strict 实际通过。容量与生产准入单列未验证，不能把局部成功写成百万在途 Ready。

## 5. 分阶段逐步实施计划

用户已授权以下全部阶段；顺序固定。阶段 1–5 均已完成；最终结果与限制见第7节。

| 阶段/步骤 | 前置条件 | 操作 | 检查/证据 | 停止条件 | 交付物/状态 |
| --- | --- | --- | --- | --- | --- |
| 1.1 事实核对 | 本轮授权 | 读全局/项目规则、错误记录、实际分支/源码/SDK/工具 | 命令、exit、已知与未知；保护既有文件 | 必需规则不可读、分支不符 | 本计划；完成 |
| 1.2 计划与 change | 1.1、完整方案已保存 | 回读计划；独立 optimize-logging-hot-path OpenSpec artifacts，读取实际 CLI instructions，apply-ready | status/instructions/strict；旧 active changes 不动 | artifact 不完整或范围冲突 | 本轮 change；完成 |
| 2.1 基准夹具 | 1 完成、计划回读 | 新增 serial/parallel、filtered/enabled、字段/access/GORM caller、HTTP/GORM 分支、配置 Value benchmark；不改生产实现 | 受影响现有测试；benchmark 源码哈希、Go/CPU/工具参数 | 基线失败先归因，未运行不勾选 | baseline 夹具与5包测试通过；完成 |
| 2.2 基线测量 | 2.1 固定源码 | 非 race，count=10，同一组构建参数；必要 CPU/alloc/mutex profile；复制开始源码至 /tmp 仅作隔离对照，不新建 git worktree | ns/op、B/op、allocs/op、profile 与 exit | profile/夹具未覆盖实际分支 | 520 个基准样本与 profile；完成 |
| 3.1 低风险日志优化 | 2 完成 | 缓存动态 core、有限 caller、可选 Enabled、GORM/HTTP 提前过滤；补契约 | 整包测试、过滤负例、动态级别、Alert、真实临时文件/HTTP | 语义或兼容不成立 | 候选A及4包race通过；完成 |
| 3.2 admission/字段/配置候选 | 3.1 与基线 | 依设计实现和测量，只有收益且关闭契约成立时保留；其余插件记录不改原因 | 并发/阻塞/panic/重复关闭/部分失败、新绑定；前后/分阶段 benchstat | 正确性失败或性能无依据 | 不保留atomic计数，采用细化A；完成 |
| 4.1 最终固定源码验收 | 3 完成、formatter 完成、源码 hash 冻结 | 双环境受影响完整 -race/shuffle；契约反证在 /tmp 副本；build 与 make check 串行、独立 coverage | 真实 exit、JSON pass/fail/skip、源码 hash 前后一致 | 未解决门禁、源文件变化 | 双环境accepted门禁/391测试/83.0%覆盖；完成 |
| 4.2 性能与后端验收 | 4.1、测量环境一致 | after count=10、benchstat；必要补样；功能验收与风险/容量说明 | 完整表、统计警告、真实 SDK/HTTP 证据；生产未验证单列 | 关键不变量未验证，不宣称准入 | 最终性能与功能矩阵已保存；完成 |
| 5.1 记录与归档 | 4 通过 | 更新本计划与 tasks，strict 后按 OpenSpec 自动流程归档本轮 change、确认输出并再验证；回读工作区范围 | diff、status、归档真实输出、无旧记录改写 | 未完成 tasks 不归档/报完成 | 归档、strict及文档回读完成 |

## 6. 实际实施与验收记录

以下按阶段保留真实执行记录；各“进行中”描述对应当时状态，最终状态以第5节和第7节为准。

本轮只读发现错误：读取未发现的 openspec/config.yaml、internal/plugin/plugin.go 失败；没有据此更改应用，后续使用 CLI instructions 和实际清单。记录于 ERR-20261021-037，需在收尾补全并核对。

### 6.1 准备与基准夹具（实际）

计划及全部 change context 已回读，CLI instructions apply 实际 state=ready；原 3 个 active changes 均与本轮无关未修改。SDK 差异已核实：workspace SetLevel(string) error，锁定 v2.3.0 SetLevel(string) 无返回值；两份 Zap/WithCallerSkip 均克隆视图并共享动态级别。测试将按两个真实签名适配，不升级 SDK。

Go1.27.3、lint2.14.0、benchstat x/perf v0.0.0-20260908200009-22c9c6c9d4da 已实际核对。gopls v0.23.0，精确 Log 声明的 references 成功，8 处引用；配合全仓 rg/源码覆盖接口及调用方，不声称覆盖仓库外或未构建平台。

本轮原件目录 `/tmp/gin-api-logperf-005obc7i`（规范路径 /private/tmp 同名目录），含 environment.json、baseline-sha.json、baseline-source、facade-references.log。基准使用真实 SDK JSON/caller，构造时捕获 /dev/null 输出，恢复 stdout 后才启动 worker；计量范围是转发/编码和设备写入，不是磁盘轮转吞吐。基准源固定后未改生产源码，受影响 5 包 race/json/shuffle 基线已启动，尚未取得完成状态。

### 6.2 修改前基线（进行中）

受影响 5 包修改前 `go test -race -json -shuffle=on -count=1 -timeout=5m` 实际 exit 0，JSON 统计 368 个测试/子用例 pass、5 包 pass、0 fail/skip。`baseline-tests.jsonl` 保留原始输出。随后启动普通 workspace 非 race benchmark：`-run ^$ -bench Benchmark(LogHotPath|GormLogHotPath|AccessLogHotPath|ConfigurationValue)$ -benchmem -count=10 -benchtime=150ms -cpu=1,4`，输出 before.txt；尚未取得完成状态。主机 Intel i7-9750H/12 逻辑 CPU/32GiB，默认 GC/无自定义 GOFLAGS；固定 -cpu 是基准对照，不是生产 GOMAXPROCS 配置。/dev/null 的 Sync 报 operation not supported 属夹具 sink 边界，发生在每次基准清理，完整保留，不作为日志文件故障或吞吐结论。

### 6.3 基线完成与实施决策（实际）

非 race baseline command 实际 exit 0，共 52 个序列/每序列 10 次/520 个样本；CPU/alloc profile command 也 exit 0。before.txt 保留原件；before.clean.txt 仅拼合被 SDK stderr Sync 警告分开的基准名称和数值，52 组均核对 10 次，数值未修改。统计中位数（-cpu=4）：过滤普通 272.1ns/144B/2alloc；过滤 GORM caller1 字段 306.1ns/552B/7alloc；启用6字段 4477.5ns/793B/5alloc；GORM caller1 4211ns/1209B/10alloc；并行6字段2284.5ns/793B/5alloc；配置 Value 串行14.125ns、并行49.16ns，均0分配；GORM过滤1956ns/944B/9alloc；HTTP过滤1783ns/688B/19alloc。背景负载影响仍在，不能按这些值推算服务容量。

据真实基线进入阶段3：先实施候选A（core/caller/Enabled/提前过滤）并保存该版本，再实施候选B及固定字段池、配置snapshot，后续同源基准对比决定保留。MySQL/Redis/cache 保留锁：可替换接口没有保证 getter、Middleware/Enabled 与 Close 同时执行安全，现有锁覆盖这些调用；去锁会扩大契约。没有 DB/Redis/缓存实现或目录改动。

### 6.4 候选 A（实际）

新增可选动态 Enabled、finite caller/core 缓存、GORM/HTTP 提前过滤，原 Mutex/WaitGroup 关闭协议保留用于对照。4包完整 race/shuffle 实际 exit0（candidate-a-tests.log），原新增反例现在通过，未删断言。候选A源码副本已保存 candidate-a-source，启动8组/10次/-cpu4 的候选对照测量，尚未完成。最终 caller/alert/原子关闭等扩展契约尚未验收。

### 6.5 候选 B 与关闭协议（进行中）

候选A8组10次对照测量实际 exit0（candidate-a.txt）。已实现单原子关闭位/在途计数、不可变caller/core视图，Init/Stop复合状态仍由生命周期锁保护；Stop封闭、撤销自己的binding、等待drained、再Sync。字段池固定8个zap.Field、超过8直接分配、全部引用清零；源SDK普通写入与HError字段格式化均同步消费，消息器保留字符串，未见字段切片异步保存。配置cfg/err一次发布snapshot，Value不持热锁；各依赖版本未变。

新增真实文件JSON/caller/requestID/SQL/流/脱敏、动态main/access、fatal阈值告警、panic释放、最后写入与关闭交错、旧owner不移除新binding、并发写/动态级别/Stop测试。5包完整race/json已启动，完成状态尚未取得。

### 6.6 契约复验（实际）

候选B首轮新caller夹具失败：GORM zap caller为data/init.go，source才是用户帧；保存候选A overlay实际重现相同输出（old-caller-contract.log，exit1），确认夹具假设错误。未改生产CallerSkip，测试改为分别精确验证source和既有adapter caller。候选B5包完整race/json/shuffle第二轮实际exit0，共391测试/子用例pass、5包pass、0fail/skip。15个本轮Go文件按实际formatter入口处理，changed=[]；定向lint进行中。反证、锁定依赖、全仓门禁及最终性能对照尚未验收。

### 6.7 首轮性能与补充对照（进行中）

完整after基准实际exit0，同样52组/每组10次；4份benchmark源码SHA与before完全一致。before-after.benchstat保留完整结果：过滤日志/SQL/HTTP、配置Value与多数并行输出显著改善，6字段分配793→264B、5→2alloc。首轮某些串行启用样本显著回退，field1有99%–163%异常；不能忽略这些结果。

为核实波动与原子方案，补三种相同基准的交替顺序对照：HEAD原实现、相同缓存/池但Mutex+WaitGroup接纳的隔离overlay、当前atomic实现。先编译三份test binary，在同一环境/-test.cpu=4/-test.benchtime=150ms下以轮转顺序测10轮，避免每版依次长时间采样的主机漂移；所有命令/退出码保存在controlled-commands.json。对照仍只代表4个逻辑执行线程的微基准，不是百万在途或服务压测。结果尚未取得，不据首轮数据宣称全路径CPU提速。

### 6.8 实测决策：不保留原子在途计数（实际）

三份binary编译与30次测量command均exit0；每版20组/10次/200样本。轮转对照 controlled-admission.benchstat 的20个时间序列均未显示atomic相对相同snapshot/字段池的Mutex+WaitGroup有显著改善（n=10，全部p>=0.05），变异较大。启用输出多数组相对HEAD也未显著改善，field1的首轮异常未在轮转对照重现，不能宣称全路径CPU提速或等价。稳定收益是过滤及分配下降；按原方案停止条件不硬上atomic关闭协议。

在已批准候选A范围内采用其细化版本：immutable视图用atomic.Pointer发布，closing原子标记仅供Enabled快速查询；真正的check-closing与WaitGroup.Add仍在同一个gate Mutex临界区，Stop在同一gate下封闭后才Wait，因此不存在仅靠独立bool+WaitGroup的Add/Wait竞态。派生caller、字段处理和I/O均移出gate；Init/Stop复合生命周期锁保持。保留固定字段池和配置snapshot的分配/读取收益。下一步先调整关闭契约反证并复验，再对最终版本补匹配性能证据与双环境门禁；阶段顺序与范围不变。

### 6.9 最终方案性能及反证（实际）

最终方案5包race/json/shuffle实际exit0，391个测试/子用例、5包pass、0fail/skip（final-candidate-tests.jsonl）。重新formatter并冻结436个Go文件；最终十轮HEAD/当前版本交替测量20个command均exit0，各20组×10次，4份benchmark源码不变，运行后436个Go SHA未变。final-performance.benchstat：过滤串行106.650→8.412ns（-92.11%）、并行144.100→2.145ns（-98.51%），均0B/0alloc；过滤字段串行295.500→8.530ns；GORM caller1串行4304→3937ns（-8.53%）、并行2279→1984ns（-12.95%）；caller2串行4618→4245ns（-8.07%）；requestID串行4955→4704ns（-5.06%）。启用的其他主要序列未见显著差异，不能写成等价或所有CPU均改善。最终对照无显著耗时回退。6字段793→264B、5→2alloc；GORM caller1 最终配对1210→272B、10→2alloc。详细置信区间/p值和所有零值geomean警告保留原件。

6个最终协议反证在隔离overlay执行，runner实际exit0且各go test实际exit1并命中指定测试与根因标记：去掉panic的Done、关闭接纳check、Stop等待、字段clear、Alert绕过、配置atomic发布均失败；后者实际DATA RACE。counterexample-results.json记录实际命令/exit/标记，生产源码未改。原子候选与中间版本的结果不当作最终源码证据。当前workspace make check正在执行；双环境最终门禁尚未完成。

### 6.10 workspace全仓门禁（实际）

workspace `make check COVERAGE_PROFILE=/tmp/gin-api-logperf-005obc7i/workspace.cover` 实际exit0，包含架构/能力清单/脚手架隔离/tidy/包级变量/文档引用/镜像摘要静态规则/安全判定/发布接线/身份/钩子/构建上下文、全仓lint、vet、modern、race/coverage；覆盖率83.0% >=72.5%。各原始输出保存在workspace-check.log；未修改门禁/豁免/覆盖率阈值。workspace与GOWORK=off的 `go build ./...` 均实际exit0。

锁定module元数据实际确认 logger/v2 v2.3.0、zap v1.28.0，原件locked-modules.json；source SHA仍436份不变。GOWORK=off完整make check与5包JSON/race已启动，尚未取得结果；普通SDK结果不代替锁定环境。

### 6.11 锁定SDK基准API兼容失败（实际）

GOWORK=off首轮5包JSON实际exit1：4包364测试pass，logging因benchmark夹具引用仅本地SDK存在的WithStacktraceLevel未编译；完整make check实际exit2，停止于同一typecheck。此时锁定功能测试与门禁不通过，不能沿用workspace结论。根因是本轮基准引入未核对的SDK选项（ERR-20261021-041）。实际锁定源码把stacktrace阈值固定为Error；基准仅Info/被过滤Debug，不涉及Error，因此去掉该非通用选项不改被测路径。仅改此测试夹具，不升级SDK或改生产配置。将刷新冻结清单、重采相同新基准源码的HEAD/最终对照，并对两环境最终源码重新跑门禁。先前样本/绿色workspace保留为当时证据，不冒充该修订后执行。

### 6.12 Stop反证夹具调度缺口（实际）

兼容基准新20个command实际exit0，每版20组/10次；锁定make check修订后实际exit0、82.9%覆盖。反证重复运行却发现去Wait变体20轮exit0（runner exit1），其余5项真实失败，故不能声称该等待反证稳定。原测试在观察closing后立即select，可能先于错误Stop返回而释放写入。仅修测试为synctest调度稳定屏障，保证错误Stop已返回、正确Stop仍在等写入后才断言；生产文件与4份基准SHA不变。对应ERR-20261021-042，旧反证原件保留，修正后重新冻结测试源与执行两环境门禁。

### 6.13 当前最终证据（进行中）

Stop修订仅改变plugin_test.go，436份Go SHA重新冻结；生产文件与4份基准源码SHA和兼容配对采样时一致，采样不执行该测试体。当前实现Stop定向race 10轮实际exit0，去Wait变体20/20命中stop passed live write、实际exit1；六项反证runner复验exit0且6/6变体exit1命中指定根因。

兼容基准最终采用compatible-performance.benchstat（原始compatible-{old,final}.txt、每版20组/10次；compatible-commands.json全部exit0）：过滤串行107.800→8.374ns、-92.23%，并行147.400→2.148ns、-98.54%；6字段串行4541→4220ns、-7.07%，并行2033→1892ns、-6.94%；GORM caller1串行4260→3863ns、-9.32%，并行2194→1982ns、-9.69%；GORM caller2串行4612→4158ns、-9.84%；access并行2089→1928ns、-7.71%。其他若干启用日志序列未见显著差异，均无显著回退；不能把未显著差异当作等价。6字段793→264B/5→2alloc保持。

对未再改动的适配器与配置源码，沿用相同来源的before-after原件：GORM适配器过滤1956→110.3ns、944→0B、9→0alloc；HTTP适配器过滤1783→281.2ns、688→208B、19→4alloc；配置Value串行14.125→2.486ns、并行49.160→0.5783ns，均0分配。前两项使用可控Enabled替身与真实适配器/Gin链，不能当作真实数据库/API吞吐；日志主基准使用真实SDK/JSON/caller和/dev/null。所有值为本机微基准中位数，后台负载/电源未固定。

最终生产函数及调用链已整段回读：门面Install/Log/Enabled、SDK适配器Init/Start/Log/Drain/Stop、配置Init/Value、GORM和GinLogger；原接口与实现类型、注册装配及自定义fallback沿实际源码/gopls/rg核对。workspace-accepted.cover方法明细：两处Enabled及配置全部方法100%，后端Log97.5%，门面Log71.4%，不声明所有分支已覆盖。

## 7. 最终验收与交付

变更类型：触及锁、atomic发布、同步排空和对象池的实现调整，按最深质量层级验证。范围限于日志门面/后端、GORM/HTTP日志准备、配置snapshot及对应测试。HEAD与分支不变，依赖、配置默认值、目录和业务接口未变。六类编译期插件保持，MySQL/Redis/cache读锁按完整可替换实现契约保留。原子在途计数候选未保留。

### 7.1 最终命令与结果

证据目录：`/tmp/gin-api-logperf-005obc7i`。accepted-commands.json记录完整参数、环境、exit和哈希；accepted-summary.json记录JSON统计。最终436份Go源码在所有验收命令前后完全一致。

| 环境 | build | make check | 受影响5包race/json/shuffle | 全仓覆盖 |
| --- | --- | --- | --- | --- |
| workspace（本地logger SDK） | exit0 | exit0 | exit0，391测试/子用例、5包pass，0fail/skip | 83.0% >=72.5% |
| GOWORK=off（logger/v2 v2.3.0、zap v1.28.0） | exit0 | exit0 | exit0，391测试/子用例、5包pass，0fail/skip | 83.0% >=72.5% |

两次make check均实际包含全仓lint/vet/modern/race/coverage及项目静态传感器，没有降低阈值或豁免。六项反证在最终夹具下复跑均真实exit1并命中指定根因，runner exit0；正确Stop定向10轮通过，去等待变体20/20被检出。变体仅在Go overlay，未修改生产代码。

性能采用相同bench源码、相同工具与-cpu=4，十轮交替顺序的compatible-*原件与compatible-performance.benchstat。Go测试夹具最后只改Stop调度屏障，生产与四份bench源码SHA未变。过滤普通串行107.800→8.374ns，路径内144→0B、2→0alloc；六字段启用日志793→264B、5→2alloc，串行约7.07%改善；GORM caller1约9.32%串行改善。若干其他启用序列无显著差异，不声明全面提速或等价。零值geomean无法计算的警告完整保留，不用总体geomean冒充API收益。

### 7.2 来源、调用链与不变量矩阵

| 来源/不变量 | 实现及反例/验收证据 | 结论/范围 |
| --- | --- | --- |
| 用户要求按实测降低CPU/分配，保持六类插件 | 本轮独立计划、匹配基准、细化A与B对照；架构传感器与Bootstrap/Core全仓测试 | 本轮通过；无架构/依赖迁移 |
| Logger只要求Log，自定义实现不被强制过滤 | Install一次探测可选LevelEnabler；TestLevelQueriesPreserveLegacyAndUnboundDiagnostics、TestOptionalLevelCapabilityPreservesAlertsAndDynamicChanges | 双环境通过 |
| 过滤不格式化/转换、SQL不调用fc | TestFilteringPrecedesValuesAndTracksDynamicStreams、TestGormDisabledBackendDoesNotMaterializeQuery；原实现负例实际失败 | 双环境通过；不改变GORM的SQL执行 |
| caller/requestID/字段/channel/main-access/告警一致 | TestCachedCallerFieldsRequestIDAndStreams、TestAlertSurvivesOrdinaryErrorFiltering；实际SDK临时文件JSON、动态阈值与告警发送 | 双环境通过；GORM caller仍为adapter，source仍为用户帧 |
| HTTP最终status分级、响应原样、敏感字段过滤与捕获上界 | TestAccessLogQueriesFinalStatusAndPreservesResponse及原有body/MaxBytes/脱敏/Writer能力测试 | 双环境通过；Gin/httptest功能证据，不是生产吞吐 |
| Stop封闭后不接纳，先join再Sync，panic/重复关闭释放 | deterministic Stop、TestPanicReleasesAdmittedWrite、TestStaleSnapshotCannotWriteAfterClose、并发写/Stop及六项负例 | 双环境race通过；SDK Sync仍合作式，不能硬中断 |
| 失败Init释放自己，旧owner不删除新binding | 原有部分失败/迟到取消；TestInstallConflictPreservesExistingOwner、TestClosingOwnerPreservesNewBinding | 双环境通过；不扩展为SDK多owner热替换保证 |
| 小字段池不保留引用、大字段不无限入池 | 固定8槽、clear引用，TestFieldBuffersReleaseAllReferences、实际9字段输出和去clear负例 | 双环境通过；不承诺全进程内存上界 |
| 配置仅完整发布一次，错误可区分，Stop后可读 | 原Once/校验契约，TestConfigurationValueDuringPublicationAndStop、去atomic实际DATA RACE | 双环境通过；调用者仍须遵守只读snapshot |
| 原信号/回滚/停止预算不变 | Core/Bootstrap/cmd生产文件未改，完整make check包含对应生命周期和信号测试 | 本轮回归通过；各30秒合作式预算未变 |
| 业务事务、幂等、权限、SQL计划/索引、Redis键/TTL | 本轮未改变这些路径、schema或规则 | 本轮不适用；未重新验收真实数据库/生产业务 |

调用链已回审：Bootstrap选定owner → registry Init/Start → facade Install → facade/GORM/GinLogger → default logger → SDK；Stop → gate封闭 → 释放自己的binding → WaitGroup join → access/main Sync。配置消费者仍通过ConfigurationProvider.Value读取同一结构，失败链与所有权不变。语义查询仅覆盖实际模块/构建上下文，rg补查注册与测试；未声称覆盖仓库外消费者、其他平台或动态外部调用。

### 7.3 结论、限制与回滚

本轮开发验证与功能验收通过；没有剩余的本轮代码阻断项。百万同时在途的生产容量及发布验收不准入：缺实例/CPU内存/请求时间/日志负载与真实压测数据，本轮没有执行该规模压测，不能给QPS/P99、无OOM或全服务Ready结论。磁盘轮转吞吐、真实MySQL/Redis业务和SDK错误stderr之外的I/O错误结果未重新测量。无生产操作、新容器、全局工具安装、commit/push/tag/发版。

需要回退时只恢复本轮五个生产文件的实现差异并重跑受影响契约；无数据或配置迁移，不用reset --hard，不覆盖后来用户改动。独立benchmark与实际失败记录保留供复核。

### 7.4 合规检查摘要

- [x] Go1.27现代写法：实际modern gate无输出
- [x] 分层/六类插件及错误链：保持架构，传感器与实际接口测试通过
- [x] 并发安全与资源：受影响race、确定性Stop/panic/所有权及负向证明通过
- [x] 超时控制：未改变合作式30秒预算，不声称SDK可硬中断
- [x] 敏感信息：请求字段脱敏/SQL参数占位/SDK字段过滤回归通过；未添加敏感配置
- [x] 测试与交叉验证：双环境build/make check、来源/代码/调用方/文档已核对
- [x] 如实记录：中间失败、未采纳原子候选、采样不确定性与生产未验证项分别保留

OpenSpec归档前strict实际exit0、valid=true、issues=[]；本节与任务清单已实际回读。最终源码/工作区范围核对通过。归档已完成，补齐Purpose后的spec strict及最终doc-refs均实际exit0。

### 7.5 OpenSpec与交付位置

归档命令实际exit0，输出明确确认新增logging-hot-path的3条requirement并成功归档为2026-10-21-optimize-logging-hot-path，没有Aborted。归档任务10/10勾选；新spec的自动Purpose占位已补成具体说明。归档前change strict与归档后spec strict均实际exit0、valid=true、issues=[]，Purpose补齐后的最终strict实际exit0、valid=true、issues=[]；最终doc-refs自检/实检exit0，32份文档通过。

本机原有.git/info/exclude忽略.harness/与openspec/：错误记录和OpenSpec源/归档已写入实际文件，但不显示在普通git status；未修改ignore或git index。可审查的Git工作区包含本轮15个Go文件及docs/logging-performance-plan.md；旧历史计划、无关active changes和生态SDK源码未修改。临时命令/测量原件在前述证据目录；没有提交/推送。

最终回读：6个本轮错误记录ERR-037至ERR-042均已按实际复验关闭，历史失败未删除。OpenSpec active list仍为原先3个无关change；本轮10/10任务归档完成。15个Go文件与本计划为Git可见交付，OpenSpec/error-journal为本机既有忽略规则下的本地交付。分支pluggable、HEAD仍为立项时提交，无其他授权外文件/分支/worktree/发布变更。

记录校对：doc-refs原始输出为32 docs，已将草稿中的33更正为32；检查通过结论未变。

补充引用校验：原始doc-refs仅枚举32份已跟踪文档；为覆盖本轮尚未跟踪的计划，使用原校验器的隔离副本仅追加本计划到输入集合，实际exit0、33 docs通过（final-doc-refs-with-plan.log）。项目校验器、Git index和ignore均未修改。


## 8. 提交前完整复核（2026-10-22）

授权来源：用户要求再次检查本轮完整 diff，确认并发安全、内存安全和兼容性后提交到 `pluggable`，明确不要 push。本次没有修改 Go 源码；436份 Go 文件与第7节最终验收的 SHA 完全一致。审查范围为5个生产文件、10个测试/基准文件和本计划，未扩大到其他优化或发布。

| 步骤 | 检查与证据 | 当前状态/停止条件 |
| --- | --- | --- |
| 8.1 范围与源码复核 | 实读规则、原计划、错误记录、全部 diff/新增文件、完整函数与直接调用链；git diff --check | 完成；分支或源码变化则停止提交 |
| 8.2 双环境提交门禁 | Go1.27.3 darwin/amd64；workspace和GOWORK=off分别执行go build ./...、make check及5包race/json/shuffle | 完成，六条命令exit0；失败则停止提交 |
| 8.3 契约反证复跑 | /tmp隔离overlay破坏panic释放、关闭检查、排空等待、字段清理、告警绕过与配置原子发布 | 完成，6/6变体exit1且命中根因、runner exit0；源码未变 |
| 8.4 暂存与提交准入 | 核对暂存清单、完整diff、文档引用与源码哈希，确认本地提交授权 | 完成；本轮16文件，diff检查exit0，33份文档引用检查exit0；不push |

输入域与调用方回审（静态回审不冒充新增运行用例）：

| 函数/边界 | 回审结论与对应证据 |
| --- | --- |
| facade Install、Enabled、Log | nil/typed-nil沿原Install检查；无可选能力、自定义实现、未绑定诊断、动态级别及Alert绕过均有测试；必需Logger接口不变 |
| logging Init、initialize、Start | 未初始化、停止后、nil配置、部分构造失败、安装冲突、迟到取消与重复Init/Stop沿实际测试核对；lifecycle保护所有者字段，snapshot完整构建后原子发布 |
| logSnapshot.enabled、Plugin.Enabled、zapLevel | 主/访问流各自动态core；未知Level仍按Info、未知Stream仍按主流；closing查询不访问已关闭writer；两份SDK的channel与root共享同一AtomicLevel |
| logging Log | CallerSkip 0–3有限缓存，其余正/负/极值沿原1+skip派生表达式，无新增索引越界；字段0/1/8以内池化，超过8独立分配，空键跳过；nil context沿旧RequestID提取语义；check-closing和writes.Add在同一gate内 |
| releaseFields、closeOwned | defer覆盖格式化/Context.Value异常；固定8槽全部clear后Put；先封闭接纳、精确释放自己的binding，再Wait和Sync；旧release不能清除新绑定 |
| config Init、Value | loader错误、nil/无效配置、取消、并发Init/Value及Stop后读取语义保留；cfg/err同一snapshot发布；调用者仍须按原契约只读配置 |
| GORM Trace、emit | 错误/慢查询/普通查询按实际分支提前查询；被过滤时不调用fc，不构造source/字段；RecordNotFound、rows<0、占位SQL、trace_id及LogMode不变 |
| GinLogger | 查询在c.Next之后按最终status分级；过滤不改变响应和body捕获；既有4097字节边界、敏感字段脱敏及Writer能力由整包回归覆盖 |

调用方：本次gopls v0.23.0对门面Enabled/Log、后端Log、配置Value的4次精确声明引用查询均exit0；配合rg及源码，核对门面helper、GORM、GinLogger、Bootstrap运行/迁移装配、logging/mysql/redis/cache/httpmiddleware配置消费与错误分支，均无需签名适配。能力与所有权分离不变。覆盖范围为当前模块darwin/amd64的实际构建上下文，未声称覆盖仓库外消费者或其他平台。

本次原件目录：`/var/folders/_w/xwv4k3t12vn9054592gsk5200000gn/T/gin-api-logperf-review-20261022-rzkmh7my`。accepted-commands.json记录完整命令、环境、退出码、Go源码哈希检查；references.json和references-*.log记录语义查询。双环境各391测试/子用例、5包pass、0fail/skip；完整make check均83.0% >=72.5%，包括lint/vet/modern、全仓race/覆盖和架构等传感器。

审查未发现新的本轮代码阻断项。已有性能样本对应的生产和基准源码未变化，本次未重跑性能采样。配置/依赖/目录/业务接口及SDK仓库没有改动。百万在途、全进程内存上限、生产QPS/P99、磁盘轮转吞吐和真实数据库/Redis业务仍未验证，不作生产准入结论。只读检索的缺失路径已如实记录并关闭ERR-20261022-043，未据其改代码或修改本机hook。

本次六项反证已完成：runner exit0，6/6变体均exit1且出现指定测试失败与根因标记（counterexample-results.json），去Wait变体20轮；这是故意破坏实现后的预期失败，未写回生产，记录于已关闭的ERR-20261022-044。review-summary.json汇总双环境测试数量、六项反证与436份源码SHA不变。未发现需要修改Go代码的新问题；本轮代码符合本地提交条件，生产容量限制保持上述结论。

提交前冻结记录：暂存清单精确包含本轮16文件；无其他暂存或工作区变更。暂存diff检查exit0，纳入本计划后的原始make check-doc-refs实际exit0（33 docs、2项原有豁免）。本节随性能优化一并提交；本地提交对象及提交后状态以Git记录和最终交付结果为证据。
