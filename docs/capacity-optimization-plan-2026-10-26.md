# 容量保护、可观测性与出站韧性优化方案
日期：2026-10-26（Asia/Taipei）
版本：修订 5（修正 Redis 连接状态和准入路由标签）
状态：全部实施阶段完成；Git 交付收尾中。存量 readiness 用例及 3 项存量 lint 问题单独保留，不宣称全量门禁全绿。
OpenSpec：已归档至 openspec/changes/archive/2026-10-26-harden-capacity-and-outbound-resilience；当前 change 及归档后六个对应 spec 的 strict validate 全部通过。

## 1. 需求与授权来源
- 用户先提出五项核心建议，随后要求“把优化方案落地到当前项目”。
- 用户已明确选择：先落地五项核心优化；新增能力只补契约与使用说明，不编造订单、租户等业务。
- 用户补充：未来可能使用 https://github.com/gtkit/restyx。当前不迁移 HTTP 客户端、不引入依赖；重试执行逻辑与具体客户端分离，保留现有 httpc 调用兼容性。
- 修订 2 已纳入用户提出的五项实施前阻塞修正，以及阶段拆分、准入位置、指标类型、负载统计和接口范围要求。
- 用户随后要求修订六处：自定义指标命名、等待配置 null 语义、最大重试次数与兼容影响、累计 GC 分位用途、阶段 4 依赖和分阶段验证命令，修订 3 已落实到主文。
- 修订 4 固定各新增指标的标签键与值域，补齐阶段 2 的 bootstrap 装配回归，并逐文件标注阶段。
- 修订 5 根据本轮两项意见，将 Redis 连接数改为规范指标的 used/idle 状态，并移除业务准入指标不可达的 unmatched 取值。当前授权仍仅为主方案文档修订，不执行代码或检查。
- 用户最新明确指令“按照方案执行所有”，授权本版全部实施阶段；受控检查仍在全部功能完成后单独取得授权。前述“仅文档”表述属于历次修订的历史边界。
- 用户追加授权：全部阶段完成后提交并 push 到 GitHub；提交仅包含本次改动，保留用户原有 ADR 修改。Git 操作在阶段 5 完成后执行。
- 用户最新指令明确“不要等我任何确认……给你所有授权”，本次不再等待追加确认；功能完成后直接执行本次必要的受控验证，然后提交并推送。仍不改无关业务、用户改动或生产环境。
- 本文件的新参数、接口和文件在对应阶段取得实际证据前均为待实施设计，不作为已实现事实。

## 2. 目标与非目标
目标：
1. 修复可选 Producer 的重复 Shutdown 等待语义。
2. 业务请求准入有界，密码运算等待人数与等待时间有界，认证正文按真实 DTO 收紧。
3. 补齐 HTTP/密码运算、Go runtime、DB 等待时间与 Redis 连接池指标；提供可重复、本机默认、有界的 API 压测入口。
4. 明确通用 Cache[T] 的条目数、字节责任与对象只读所有权；补部署内存预算说明。
5. 可选出站重试增加抖动、参数边界和 Retry-After 传递路径，兼容现有调用；补六类新增能力的契约说明。

非目标：
- 不新增业务模块、业务数据表、迁移、Outbox/Inbox 实现、租户系统、可靠审计实现或认证缓存。
- 不移动目录、不调整分层、不升级 Go/module/dependency，不修改现有用户 ADR。
- 不启用 taskqueue/upload/outbound/auditlog，不发送真实通知，不接触生产，不拉镜像、不建容器。
- 不把合成负载结果当成实际业务容量；不擅自运行 lint/vet/modern/全量 race/coverage。

## 3. 已核对的事实
- go.mod 和本机均为 Go 1.27.3。依赖保留 asynqx v1.6.0、httpc v1.6.1、go-redis/v9 v9.23.0、OTel v1.47.0、x/sync v0.24.0、Gin v1.12.0。
- Producer.Shutdown 外层 sync.Once 捕获局部错误；锁定的 asynqx 已用 CAS 启动一次关闭，并允许每次调用继续等待 closeDone。
- 密码运算使用 semaphore.Weighted.Acquire(ctx,1)，执行并发有界，等待人数无独立上限。
- /api/v1 已有 30s 协作式预算，认证子路由有 10s 预算；Timeout 同步执行 c.Next，不强停忽略 ctx 的业务。
- 访问日志为旁路正文捕获，不主动预读，保留现有 4097 字节捕获上限。
- 四个认证路由使用 JSONBody(0)，全局未配置时请求体上限为 10 MiB。
- HTTP/worker 指标已存在；队列已有每次执行结果和耗时。DBPoolStats 缺少 WaitDuration。
- Redis v9.23.0 的 PoolStats 中 WaitCount、Timeouts 等为 uint32，WaitDurationNs 为 int64；独立 pipeline 池统计在 PipelineStats，主池统计不包含它。
- 锁定 OTel API 没有异步直方图。semconv v1.43.0 的 go.memory.gc.pause.duration 和 db.client.connection.wait_time 为同步 Float64Histogram；不能把累计分布或累计等待值直接写入这两个指标。
- semconv v1.43.0 已有 go.goroutine.count、go.memory.used、http.server.active_requests、db.client.connection.pending_requests 与 db.client.connection.timeouts；具体 instrument 类型以已安装源码为准。
- DB 最大连接数缺省为 25，业务在途数不等于数据库连接数。
- scripts/README.md 和现有独立 Go 工具采用 //go:build ignore 与显式文件清单测试，不把这些工具加入普通 ./... 包集合。
- Cache[T] 按 maxItems 淘汰，返回 T 不深复制；当前未找到产品业务对该通用缓存的直接引用，不能据此声称泄漏。
- RetryPolicy 最多三次重试，零等待有明确测试，当前未防负时长、倍增溢出；Attempt 只返回状态码与错误。
- 可选模块默认未接线；现有活跃 OpenSpec 变更不覆盖本次完整目标，建立独立 change，不接手旧变更。
- git 初始已有 docs/adr/0001-module-boundaries-over-ddd-layering.md 修改，必须原样保留。
- 根目录 task_plan.md/findings.md/progress.md 属于旧订单/长连接任务，已忽略且未跟踪。本次不改它们，在 .cache/capacity-optimization-20261026 保留独立工作记录。
- 生产容量、内存配额、真实流量比例和 SLO 未提供，均为未确认。

## 4. 已批准的具体设计
本章保留编码前决策；实际实施状态与证据以第 6、8、13 节为准。
### 4.1 关闭语义
保留 nil 防护，删除外层 closeOnce 及无用 sync import，直接委托 inner.Shutdown(ctx)。
通过可控的在途投递验证首次取消、二次仍等待、底层完成、并发关闭以及共享 Redis 不被生产者关闭。

### 4.2 准入与认证（阶段 2 连同观测一起交付）
- application.max_inflight_requests：进程业务请求上限；0/省略均采用 128，正数显式覆盖，负数启动失败。本方案不提供关闭准入的配置语义；如未来需要旁路，必须另行明确授权。128 是待校准的初值，不是实测容量或内存安全保证。
- 在 /api/v1 组按 Admission → Timeout → 后续业务链的顺序注册非阻塞准入，拒绝时不创建本组的超时上下文；全局日志、指标与错误处理仍保留。满载直接返回既有 503 错误信封，不建立业务等待队列。
- /health 与 /ready 在该组之外。以后 /api/v1 下若增加 SSE、流式或长轮询，连接会长期占用名额，必须先确认独立分组或独立限额；本次不预造长连接路由。
- 名额在 c.Next 实际退出时释放，包括 panic 展开；ctx 到期但业务未返回时仍占用名额。
- 准入 503 本次不写固定 Retry-After：服务无法由一个静态值知道何时恢复容量。文档要求网关不得对过载 503 无条件重试；仅明确幂等且有次数、抖动和总预算的调用可由策略决定重试。
- auth.bcrypt_max_waiters：省略或 null 时取 EffectiveBcryptConcurrency()，即最多额外等待一批与执行并发同量级的请求；显式 0 才表示满载快速拒绝；正数允许对应数量的等待者；负数非法。配置用 *int 保留省略/null 与显式 0 的区别，解码验收分别覆盖省略、null、0、正整数和负数，不能只根据 Go 字段类型假定语义正确。
- config/env.yml.example 和本地 config/env/env.yml 的默认示例均明确写 bcrypt_max_waiters: null，并在紧邻注释中说明“省略/null=等待人数取执行并发数，0=禁止排队，正整数=指定等待人数”。不得照抄相邻 bcrypt_max_concurrent: 0；已有 log.sql_slow_threshold_ms 的省略/null/0 说明作为写法参照。此处为待实施要求，本轮不改实际配置。
- auth.bcrypt_queue_timeout：允许等待时才生效；0/省略回落 1s，负数非法，实际等待不超过调用方剩余预算。等待上限不改变正在运行的 bcrypt 生命周期。
- 密码队列饱和或独立排队超时返回 application 领域忙错误并映射 503；调用方取消优先保留取消语义，不能伪装成凭据错误。
- 认证四个 JSON 路由使用 8 KiB 上限，覆盖现有 DTO 最大字段、JSON 转义和合理空白；不修改字段校验、令牌和密码规则。
- HTTP 在途、准入拒绝、密码执行/排队数、排队耗时和拒绝原因的仪表与行为在阶段 2 同批实现并验证，不能等到阶段 3 才补可见性。标签按 4.3.1 的逐项契约固定；在途/排队数量不带 outcome，只有已完成的等待或拒绝决策记录结果。
- application.max_inflight_requests 必须经 bootstrap.buildEngine 传入 router.Options，再到实际业务组准入。阶段 2 同步修改 internal/bootstrap/modules.go 与现有 assembly_contract_test.go，验证非默认配置值确实控制实际注册路由；阶段 2 回归必须包含 ./internal/bootstrap。
- 新配置同步强类型定义、验证、config/env.yml.example、现有 config/env/env.yml 的对应非敏感字段及说明。后者已被 gitignore 忽略，禁止强制提交或回显其他秘密值。
- 校准依据为稳定窗口下 L ≈ λ × W（在途数、吞吐每秒、平均端到端秒数），不能用 P99 替代这里的平均 W，也不能仅由平均值推断尾延迟。DB 并发可粗估为 λ × 每请求 DB 次数 × 单次占用连接秒数；并行调用、事务、突发和其他使用者需单独计入。将估算与池上限 25、DB 等待、503、P95/P99、CPU/RSS 实测对照，不把 HTTP 上限机械设成 DB 池大小。

### 4.3 可观测性和基线（拆为阶段 3A、3B）
#### 4.3.1 指标类型和命名
- 阶段 3A 只补 runtime/DB/Redis 指标和注销生命周期，以阶段 2 完成准入、密码指标交付为前置；当前各项均待实施。
- 复用已锁定的 semconv v1.43.0 名称与 instrument 类型，不引入新依赖。现有 db.client.connections.usage、db.client.connections.max、db.client.connections.wait 及其标签保持不变，避免破坏面板。
- 累计量不借用逐次直方图的 semconv 名字。新增自定义指标沿用 ratelimit.decisions、auth.events、worker.executions 的组件命名风格，不嵌入 gin_api 或派生项目 module 名，也不新占用 db.、go. 规范指标族。
- 仪表来源由 meter scope 表达，服务身份由 service.name 等资源属性表达，均不靠新增项目名前缀拼进指标名。已有 semconv 名称及存量自定义指标保持不变。
- 新增指标按下表固定名称、类型、采集语义和完整标签集合；“无”表示不附加测量点标签，不影响既有 service.name 等资源属性。不得在实现时自行添加 scope、outcome 或任意业务标签。
- HTTP method 沿现有白名单归一为 GET/HEAD/POST/PUT/DELETE/CONNECT/OPTIONS/TRACE/PATCH/QUERY，其他值统一为 _OTHER。url.scheme 仅为 http/https，按到达本服务的真实请求传输确定（TLS 存在为 https，否则 http），不直接相信转发头。
- HTTP 在途指标挂在现有全局 Metrics 中间件，覆盖与现有请求量相同的请求边界；进入时捕获 method/scheme，退出及 panic 收尾用同一属性集合减一。它不带 outcome、状态码或路由属性；业务准入占用仍由业务组闸门负责，不能把全局活跃数直接当成业务槽位数。
- 池名规则固定：当前唯一 MySQL 池为 mysql/main；Redis 为 redis/<db>/main 或 redis/<db>/pipeline，<db> 是实际配置且已初始化的逻辑库号的十进制文本。同一进程内唯一，重连不改名，不把客户端指针或 generation 写进名称。
- 池标签值域固定：pool.type=mysql/redis；pool.role=main/pipeline（MySQL 仅 main）；redis.db 是配置中实际初始化的非负整数库号，仅 Redis 样本携带，MySQL 样本省略。不得使用请求参数、地址、账号或密码生成池标签。

| 统计量 | 拟用名称与类型 | 语义 | 标签键和取值 |
|---|---|---|---|
| HTTP 正在处理数 | http.server.active_requests，Int64UpDownCounter | 阶段 2；同一请求进入/退出对称增减 | http.request.method=上述白名单或 _OTHER；url.scheme=http/https；无 outcome |
| 业务准入拒绝次数 | admission.rejections，Int64Counter | 阶段 2；已确定饱和拒绝后计数 | http.request.method、url.scheme 同上；http.route=已注册的路由模板；reason=saturated |
| 密码正在执行数 | auth.password.active，Int64UpDownCounter | 阶段 2；实际持有运算名额期间 | 无 |
| 密码等待人数 | auth.password.queued，Int64UpDownCounter | 阶段 2；实际持有等待名额期间 | 无 |
| 密码排队耗时 | auth.password.queue.duration，Float64Histogram，单位 s | 阶段 2；实际进入排队后离队时记录 | outcome=acquired/timeout/canceled；未进入排队的请求不记录此直方图 |
| 密码容量拒绝次数 | auth.password.rejections，Int64Counter | 阶段 2；容量拒绝与调用方取消分开 | reason=queue_full/queue_timeout；取消不计入拒绝 |
| goroutine 数 | go.goroutine.count，Int64ObservableUpDownCounter | runtime 当前快照 | 无 |
| Go 管理的总内存 | go.memory.used，Int64ObservableUpDownCounter，单位 By | runtime 管理的内存总量，不是 RSS；本期不拆 go.memory.type | 无 |
| 堆对象内存 | runtime.heap.used，Int64ObservableGauge，单位 By | runtime/metrics 中堆对象当前字节数 | 无 |
| 累计分配字节 | go.memory.allocated，Int64ObservableCounter，单位 By | runtime/metrics 的累计堆分配字节 | 无 |
| GC CPU 时间 | go.cpu.time，Float64ObservableCounter，单位 s | 仅 GC CPU 累计时间，不填充其他 CPU 状态 | go.cpu.state=gc |
| Redis 连接数 | db.client.connection.count，Int64ObservableUpDownCounter | 同一份 PoolStats 中 idle=int64(IdleConns)，used=max(int64(TotalConns)-int64(IdleConns),0)；不发布重叠的 total 状态 | db.client.connection.pool.name=redis/<db>/<role>；pool.type=redis；redis.db=<实际库号>；pool.role=main/pipeline；db.client.connection.state=used/idle |
| Redis 等待连接数 | db.client.connection.pending_requests，Int64ObservableUpDownCounter | 当前 PendingRequests，不用累计 WaitCount 代替 | db.client.connection.pool.name=redis/<db>/<role>；pool.type=redis；redis.db=<实际库号>；pool.role=main/pipeline |
| Redis 池超时次数 | db.client.connection.timeouts，Int64ObservableCounter | 分代/回绕处理后的单调累计量 | db.client.connection.pool.name=redis/<db>/<role>；pool.type=redis；redis.db=<实际库号>；pool.role=main/pipeline |
| Redis 等待总次数 | pool.wait.count，Int64ObservableCounter | 分代/回绕处理后的 WaitCount | db.client.connection.pool.name=redis/<db>/<role>；pool.type=redis；redis.db=<实际库号>；pool.role=main/pipeline |
| DB/Redis 累计等待秒数 | pool.wait.duration，Float64ObservableCounter，单位 s | 自定义累计量，不得使用逐次直方图名 db.client.connection.wait_time | pool.type=mysql/redis；db.client.connection.pool.name=上述固定池名；pool.role=main/pipeline（mysql 仅 main）；Redis 额外带 redis.db=<实际库号>，MySQL 省略 |
| GC 暂停次数 | runtime.gc.pause.count，Int64ObservableCounter | runtime 累计直方图的次数和 | 无 |
| GC 暂停分位上界 | runtime.gc.pause.quantile，Float64ObservableGauge，单位 s | 进程累计分布的桶上界估计，仅长期参考 | quantile 为字符串 0.50/0.95/0.99 |

- 验收对名称、instrument 类型、标签键全集、取值及禁止属性逐项断言：HTTP 进入/退出属性一致且不带 outcome；异步池指标也必须携带 db.client.connection.pool.name；MySQL/Redis 等待量不合并到同一属性集合；pipeline/main 不互相覆盖。现有 MySQL db.client.connections.* 的标签与名字不追加或改写，其中 db.connection.state=used/idle 保持不变。
- Redis 连接数只发布互不重叠的 used/idle 两类，禁止同时把 total 作为第三类状态。计算时先把两个 uint32 字段分别转为 int64，再相减和钳零，避免无符号下溢。字段不是原子联合快照：正常一致快照下 used+idle=TotalConns；短暂出现 IdleConns>TotalConns 时 used=0，不承诺该瞬间相加仍等于 TotalConns，也不据此判定资源泄漏。验收覆盖正常值、零值、IdleConns>TotalConns 和 main/pipeline 分别计算。
- admission.rejections 只由匹配到 /api/v1 已注册路由的组中间件记录，http.route 只取该路由模板。NoRoute/NoMethod 不经过该组准入，不设 unmatched 分支；阶段 2 的生产装配回归验证 404/405 不增加准入拒绝计数。这个限制仅针对业务准入指标，既有全局请求指标对 unmatched 的归类保持不变。

- GC 暂停明确选择“累计次数 counter ＋ 分位上界 gauge”。runtime/metrics 的累计直方图没有精确总时长，不能将桶边界乘次数伪装成累计秒数，不导出伪造的 pause-duration counter。
- 分位 gauge 按累计次数选取命中桶的上界；无样本或命中 +Inf 桶时不发布该分位值。它不是最近采集窗口分位，也不是请求延迟。禁止创建异步直方图或把整份历史在每次回调重新 Record。
- 该累计分位仅用于长期暂停分布和基线参考，不用于近期暂停回归告警；运行时间越长，近期变化越容易被历史样本稀释。本期不在回调中实现差分窗口，避免将 reader 数量、额外采集和并发回调隐含为固定时间窗口。使用说明与指标描述必须同时标明这一用途限制。
- 分配量、GC CPU 等累计量及其他 runtime 内存快照同样按真实类型映射；采集只在 OTel 回调发生，回调不访问 DB/Redis 网络，不在请求热路径调用 GC 或阻塞 I/O。
- 回调沿用 providerCleanup 的注销顺序；关闭导出维持 no-op，不增加独立采集 goroutine 或重复 Prometheus 栈。
- 已有队列执行结果和耗时继续复用；积压、最老任务年龄及独立重试统计只写入启用队列时的采集契约，本次不启动可选后台采集器。

#### 4.3.2 Redis 分代、回绕和 pipeline 池
- bootstrap 采样时按配置的有限 DB 集合捕获 Resources.Rdb(db) 返回的实际 *redis.Client，再从同一个捕获对象读取 PoolStats。该指针只在内部识别客户端，不成为标签或日志字段；nil 客户端不发布池样本。
- 每个 DB 跟踪当前客户端身份。实例变化时递增内部 generation；不能用每次重新分配的 *PoolStats 快照地址判断新一代，也不能仅凭“计数下降”判断重建。
- 同代 WaitCount/Timeouts 等 uint32 增量计算为 uint64(uint32(current - previous))，利用模 2^32 算术正确处理一次回绕；再加到 exporter 自有单调总量。首次采样把当前值计入，新代保留旧累计总量并加入新实例当前值，不跨代相减。
- 以上回绕还原要求两次样本间每项增量小于 2^32；一次间隔发生多次整周回绕无法从两个快照还原。替换前未采集到的末尾增量也不可恢复，说明中必须保留这两个观测边界。
- WaitDurationNs 为 int64，按同代非负差值和跨代当前值累加，再转换为秒；若同代出现不应发生的下降，将该次计数标为重置/异常并重新建立基线，不推导负等待时长。
- 采集独立 PipelineStats，并用固定 pool.role=main|pipeline 区分。PipelineStats 为 nil 表示共享主池，此时只发布 main，不补一份重复的 pipeline 计数。两类池分别持有基线与累计量。
- 采样状态仅保存每个配置 DB 的当前代与两种池角色；换代替换旧引用，不能按历史客户端无限增加 map 或标签。
- 验收覆盖 uint32 最大值附近回绕、换代时新值更大/更小、main 与 pipeline 分别回绕、nil pipeline、重置和并发采集/注销。

#### 4.3.3 独立 Go 压测工具
- 文件定为 scripts/load-api.go 和 scripts/load-api_test.go，两者均使用 //go:build ignore；沿用 scripts/README.md 的文件清单运行/测试方式。它们不进入 go build/test ./...、默认覆盖率或架构传感器。
- 以起始时刻和序号按 t(n)=t0+n/rate 计算计划发送时刻；有限 worker、零无界等待队列、有限执行时长，目标默认只允许本机。调度滞后时按原计划补记未能发出的数量为 generator-dropped，不把漏掉的 tick 当作未提供过的负载。
- 完成请求延迟从计划发送时刻到响应结束计算，包含发送前调度等待；不从“拿到 worker 后”重新计时。另行统计实际请求耗时可用于诊断，但不能替换主延迟统计。
- 延迟只保存固定桶与计数，不保存随请求数增长的样本数组。分位输出为桶上界估计；超过最大有限桶时明确报告越界，不伪造有限 P99。
- 输出 offered/sent/completed/generator-dropped、HTTP 状态分布、传输错误、完成请求的计划时刻延迟 P95/P99。未发请求没有完成延迟，丢弃率必须与分位同时呈现，不能只挑完成样本宣称服务容量。
- 接受显式场景文件；场景数、响应读取、统计标签及并发有界。不得输出 token、请求正文、完整敏感 URL；默认禁止重定向绕过本机目标限制。
- 非本机目标必须显式许可，示例不自动执行远程或生产压测。CPU/RSS/GC 来自独立服务观测，不能把发生器资源当作服务资源。
- 阶段 3B 独立验收计划时刻、调度滞后、固定桶、慢下游、客户端取消、到期退出和目标限制；本机可控服务结果只作为工具功能证据。真实认证/DB/Redis 容量仍需环境、数据和 SLO。
- 工具实现参数在编码前固定：`-target` 与 `-scenario` 必填；场景文件为 JSON 请求数组（method、path、weight、headers、body），总文件最多 1 MiB、最多 64 个场景、单请求正文最多 64 KiB、最多 32 个请求头。默认 rate=10/s、concurrency=4、duration=10s、timeout=5s；分别限制为 rate≤100000/s、concurrency≤4096、duration≤24h、timeout≤1min，均须为正。响应最多读取 1 MiB，超出计为传输失败。分位固定桶上界为 1/2/5/10/20/50/100/200/500ms、1/2/5/10/30/60/120s，越界明确标注；禁止重定向、代理和目标 URL 内嵌凭据。localhost 固定拨号到 127.0.0.1，避免依赖 DNS 将本机限制绕开。以上是工具的设计边界，不是服务容量结论。

### 4.4 内存与对象所有权
只在当前 Cache[T] GoDoc 和说明中固定返回对象的只读共享约定、调用方修改须复制、maxItems 非字节预算、单对象大小/总预算属于使用方职责。
不增加无实际调用方的通用深复制、反射测量或成本计算框架。
部署说明给出 GOMEMLIMIT 预算方法、Go 外内存余量、GC 压力与 RSS 对照；不猜测本机/生产内存配额，不自动设置进程全局内存限制。

### 4.5 出站重试与 restyx 替换边界
- 保留现有 Attempt、Do、DoIdempotent 公共签名及零等待语义；重试引擎、尝试结果和元信息保持包内私有，不增加无人调用的公开入口。
- 现有 propagationTransport 已包住真实出站请求。拟在每次 DoIdempotent 尝试的私有 context 状态中收集最终响应的 Retry-After，再交给私有引擎；每次新建状态以免复用上一轮响应头。该状态与调用隔离，原始响应、body、连接池与 trace 传播不变。Do 仍只执行一次。
- Attempts 明确表示首次请求之后的最大重试次数，允许 1..3；实际重试次数为 0..Attempts，总尝试次数最多 1+Attempts。成功、不可重试响应、调用方取消、deadline 或等待预算均可使其提前结束，不承诺执行满配置次数。
- 验证负时长、单项上界关系和倍增溢出；指数退避采用饱和计算与全抖动，0 等待仍为 0。选择“最大次数＋等待预算允许提前结束”的契约，不因最坏退避序列总和超过 30s 而拒绝本身满足单项边界的配置；因此随机等待长度可能影响实际可执行的重试次数，说明与测试均覆盖预算提前结束。
- 拟定私有绝对等待上限 30s，同时作为一次重试循环的累计等待预算；它是本方案明确选择的执行边界，不是下游实测恢复时间。正数 WaitTime/MaxWaitTime 超过 30s 在执行前拒绝；MaxWaitTime=0 表示采用此有界策略，不能解释为无限等待。
- 支持 Retry-After 的 delta-seconds 与 HTTP-date；等待取服务端要求与本地抖动等待的较大值。非法语法回落本地策略，合法但过大/数值超出可表示范围的值按不可接受等待处理，不能误当非法头而提前重试。
- 若下一次等待超过 30s、超过本轮剩余等待预算，或不能在调用方剩余 deadline 内完成，放弃下一次尝试，返回最后状态和原始失败；不截短服务端要求、不伪造尚未发生的 context 错误。这样无 deadline 的 ctx 也不会因 Retry-After: 86400 睡一天。
- 真正在等待过程中发生调用方取消时，沿用现有规则聚合最后真实失败与 ctx 错误。熔断、状态码优先判定和明确幂等限制保持不变。
- 未来接入 restyx 时再评估是否开放复用入口；当前只保证私有引擎不依赖具体客户端。届时须确定唯一重试所有者，并重新核对超时、响应上限、连接池、追踪和 logger，不能仅改 import 宣称兼容。

### 4.6 新增能力只交付契约
在现有 docs、模块 README 和 API 文档中说明：OpenAPI/认证与错误示例；写入幂等键及结果重放；Outbox/Inbox 同事务与去重；多副本调度所有权和窗口幂等；资源归属与可选租户隔离；可靠审计与尽力而为审计的区别。
添加覆盖真实现有路由的静态 OpenAPI 文档，不编造业务 API、表、字段和容量数据。
在 internal/bootstrap/openapi_contract_test.go 使用与产品相同的 buildEngine 注册路径，对照 engine.Routes() 的方法与完整路径和 OpenAPI 的操作集合，双向发现缺失、多余和错误方法；规范化 Gin 路径参数与 OpenAPI 参数写法。分别验证本地/外部认证模式的条件路由，不能用手写第二份路由清单代替产品装配。该测试仅列入待实施验收，本轮不创建或执行。

### 4.7 主要取舍
| 议题 | 本版选择 | 未选择的方案及原因 |
|---|---|---|
| 独立工具 | scripts/ 下 build-ignore 文件清单运行 | 普通包会改变 ./...、覆盖率及传感器范围 |
| GC 暂停 | 累计次数＋仅作长期参考的累计分位上界 gauge | 回调直方图不存在；从桶估出总秒数会冒充精确量；本期不增加依赖采集触发方式的差分窗口 |
| 密码等待缺省 | 一批等待者＋1s 预算，显式 0 可禁排队 | 缺省零等待会让小核数部署的轻微突发直接失败 |
| 准入关闭 | 本次不提供关闭选项，0 使用默认值 | 不让 0 同时表达省略与禁用两种含义 |
| Retry-After | 私有 30s 绝对/累计等待边界；Attempts 为最多次数，超限提前结束 | 截短后重试违反服务端等待；只依赖 ctx 无法约束无 deadline 调用；不以最坏等待总和强制保证试满次数 |
| 未来客户端替换 | 私有引擎接现有调用与 transport 元信息 | 预先导出新入口没有当前调用方，扩大兼容负担 |

## 5. 文件范围与阶段归属
下表是获准变更清单；“新增”均为待创建文件，现有文件沿用原位置。多阶段文件分别记录该阶段的 diff 和源码版本，不因同一路径重复出现而跳过后续回归。

| 文件 | 阶段 | 拟变更 |
|---|---|---|
| docs/capacity-optimization-plan-2026-10-26.md | 0；各阶段状态记录 | 本方案、文件边界与真实证据 |
| internal/optional/taskqueue/producer.go | 1 | 重复 Shutdown 委托 |
| internal/optional/taskqueue/producer_shutdown_test.go | 1 | 新增关闭语义回归 |
| internal/runtime/module/auth/adapters.go | 2 | 密码执行/等待容量及指标 |
| internal/runtime/module/auth/adapters_test.go | 2 | 容量、等待、取消和指标回归 |
| internal/runtime/module/auth/module.go | 2 | 配置和 Recorder 注入 |
| internal/module/auth/application/service.go | 2 | 领域忙错误及传播 |
| internal/module/auth/transport/http/router.go | 2 | 8 KiB 正文上限 |
| internal/module/auth/transport/http/handler.go | 2 | 容量拒绝映射 503 |
| internal/module/auth/transport/http/errmap_test.go | 2 | 错误信封及 503 |
| internal/module/auth/transport/http/body_limit_test.go | 2 | 新增正文边界回归 |
| internal/platform/config/types.go | 2 | 应用在途字段 |
| internal/platform/config/auth.go | 2 | 等待参数及省略/null/0 语义 |
| internal/platform/config/load.go | 2 | 沿用已有存在性字段校验，拒绝等待人数的非整数输入 |
| internal/platform/config/auth_test.go | 2 | 参数和默认值回归 |
| internal/platform/config/validate.go | 2 | 新参数校验 |
| internal/platform/config/capacity_test.go | 2 | 新增容量配置解码与校验 |
| internal/router/init.go | 2 | Options 与业务组准入顺序 |
| internal/router/admission_test.go | 2 | 新增准入和探针隔离回归 |
| internal/bootstrap/modules.go | 2 | Config → router.Options → 业务准入接线 |
| internal/bootstrap/assembly_contract_test.go | 2 | 现有真实装配测试补非默认上限的实际效果 |
| internal/bootstrap/providers.go | 3A | runtime/池采集与注销注册 |
| internal/bootstrap/app_test.go | 3A | 同步实际 provider 清理项的生命周期断言 |
| internal/bootstrap/openapi_contract_test.go | 5 | 新增真实路由与 OpenAPI 双向对照 |
| internal/middleware/metrics.go | 2 | 全局活跃请求对称计数与固定属性 |
| internal/middleware/admission.go | 2 | 新增业务准入及拒绝指标 |
| internal/middleware/admission_test.go | 2 | 新增容量边界与属性回归 |
| internal/pkg/metrics/metrics.go | 2、3A | 阶段 2 准入/密码仪表；3A DB 等待总量 |
| internal/pkg/metrics/metrics_test.go | 2、3A | 对应各阶段的仪表、属性及累计值回归 |
| internal/pkg/metrics/runtime.go | 3A | 新增 runtime 指标和固定属性 |
| internal/pkg/metrics/runtime_test.go | 3A | 新增 runtime 类型/属性/分位回归 |
| internal/pkg/metrics/redis.go | 3A | 新增分代、回绕和双池采集 |
| internal/pkg/metrics/redis_test.go | 3A | 新增池属性、双池、回绕和注销回归 |
| internal/optional/outbound/retrying.go | 4 | 旧入口适配及参数边界 |
| internal/optional/outbound/retrying_test.go | 4 | 既有重试契约回归 |
| internal/optional/outbound/downstreams.go | 4 | 当前入口接私有引擎 |
| internal/optional/outbound/downstreams_test.go | 4 | 当前出站入口功能回归 |
| internal/optional/outbound/propagation.go | 4 | 私有响应元信息传递 |
| internal/optional/outbound/propagation_test.go | 4 | 上下文隔离与 trace/头透传回归 |
| internal/optional/outbound/retry_engine.go | 4 | 新增私有重试引擎 |
| internal/optional/outbound/retry_engine_test.go | 4 | 新增抖动、预算和 Retry-After 回归 |
| internal/pkg/cache/cache.go | 5 | 仅 GoDoc，固定共享只读和字节责任 |
| scripts/load-api.go | 3B | 新增 build-ignore 独立工具 |
| scripts/load-api_test.go | 3B | 新增文件清单功能测试 |
| config/env.yml.example | 2 | 新参数及 null 默认示例 |
| config/env/env.yml | 2 | 仅对应非敏感字段；保持忽略、不强制提交 |
| config/README.md | 2 | 参数含义与准入配置说明 |
| scripts/README.md | 3B | 独立工具运行/测试命令 |
| pprof/README.md | 3B | 负载、采样和基线记录入口 |
| internal/optional/outbound/README.md | 4 | 重试边界与未来 restyx 适配说明 |
| internal/optional/README.md | 5 | 可选能力契约入口 |
| docs/capacity-and-memory.md | 2、3A、3B、5 | 分别写容量参数、指标标签、基线工具、内存/缓存责任 |
| docs/optional-capability-contracts.md | 5 | 新增六类可选能力契约说明 |
| docs/openapi.json | 5 | 新增真实 API 契约 |

后续获准实施时，当前 OpenSpec change 的 proposal.md、design.md、tasks.md 在阶段 0 同步本版，tasks 状态随实际阶段更新；以下已有 spec delta 按所属阶段同步，不接手其他 change：
- request-capacity-control/spec.md：阶段 2。
- runtime-capacity-metrics/spec.md：阶段 2 的准入指标边界、阶段 3A 的运行时/池指标。
- capacity-baseline-tooling/spec.md：阶段 3B、5。
- task-queue-preset/spec.md：阶段 1。
- http-request-contract/spec.md：阶段 2。
- outbound-http-resilience/spec.md：阶段 4。

上述路径均相对于 openspec/changes/harden-capacity-and-outbound-resilience/specs/；proposal/design/tasks 位于该 change 根目录。.harness/error-journal.md 仅在获准代码任务中按各阶段的实际事件记录。本轮不修改 OpenSpec、错误日志、配置或代码。

保留现有用户 ADR 修改及根目录历史工作记录。若实现需要清单外文件，先更新本文件并说明原因；涉及未授权业务或架构变化时停止等待授权。

当前 change 完成后按项目 OpenSpec 流程归档：仅合并六个对应能力 spec（request-capacity-control、runtime-capacity-metrics、capacity-baseline-tooling、task-queue-preset、http-request-contract、outbound-http-resilience）并保留该 change 的归档产物；不归档或修改其他活跃 change。归档目录以 CLI 实际输出为准。

## 6. 阶段和验收
所有实施阶段共同前置：用户批准本版（已满足）、当前 change 按本版同步后 apply-ready、回读本文件/最新指令/相关错误记录，具体文件已读取。
保留原阶段 1、2、4、5 的编号与目的；阶段 3 拆为 3A/3B，准入相关指标前移到阶段 2。
阶段依赖不是一条直线：阶段 2 → 3A → 3B 构成容量与基线分支；阶段 4 在共同前置和阶段 1 满足后即可实施，不依赖阶段 2、3A 或 3B。阶段 5 汇总两个分支的全部结果。该依赖调整不意味着自动启动并行代理或扩大已授权阶段。

| 阶段 | 前置与具体步骤 | 检查及可复核验收证据 | 停止条件 | 交付物/状态 |
|---|---|---|---|---|
| 0 | 回读修订 5 和规则；同步 OpenSpec；核对本轮执行环境 | 主方案及 artifacts 回读、applyRequires | artifacts 尚不一致或执行前置未满足 | 已完成；apply instructions=ready；会话门禁已获准本次跳过 |
| 1 | 共同前置满足；1.1 复现重复 Shutdown；1.2 去除外层 Once；1.3 核对 nil/取消/重复/共享资源 | 原始失败、修复后队列整包普通测试与源码 diff | 原因不明或触及真实 Redis | 关闭修复与用例；已完成 |
| 2 | 阶段 1 已验收；2.1 准入/密码配置及 503；2.2 同批仪表；2.3 8 KiB 正文与说明；2.4 buildEngine 真实配置接线 | 并发与 panic 名额释放、Admission 在 Timeout 前、满载探针可用；404/405 不经过组准入且不增加拒绝计数；省略/null 默认一批等待、显式 0 禁排队及取消；method/scheme 必填且在途无 outcome；bootstrap 非默认配置实际生效；正文边界及合法 DTO | 超限进入业务、接线遗漏、提前释放、配置存在性混淆或拒绝未记录 | 带指标的容量保护及装配回归；已完成，存量测试例外见第 13 节 |
| 3A | 阶段 2 已验收；3A.1 runtime/DB/Redis 指标；3A.2 分代、回绕、pipeline；3A.3 注销和文档 | ManualReader 名称/类型/标签键全集/值域断言；池名必填及 MySQL/Redis、main/pipeline 隔离；Redis used/idle 不重叠、先转有符号数再相减且负差钳零；旧指标不变；回绕/换代；GC 长期参考语义；注销后不采集 | 必填属性缺失、属性集合混淆、连接状态重复计数、累计值写入逐次直方图、跨代相减、双池重复、负增量或错误告警用途 | 容量指标及独立回滚范围；已完成 |
| 3B | 阶段 3A 已验收；3B.1 build-ignore 工具；3B.2 计划时刻与固定桶；3B.3 本机功能演练 | 文件清单测试/编译；工具不成为普通包；调度延迟计入分位、错过负载计入 dropped；慢下游、取消、时限退出；原始 JSON | 远程未经允许、无界样本/排队、漏计计划负载或泄密 | 独立工具和说明；已完成 |
| 4 | 共同前置及阶段 1 已满足；不依赖 2/3A/3B；4.1 私有引擎接现有入口；4.2 transport 元信息；4.3 30s 边界/抖动/Retry-After | 旧 API 与零等待；负值/溢出及超过 30s 参数拒绝；真实头两种格式；无 deadline 的 86400 秒头立即放弃；预算提前结束且实际次数不超过 Attempts；取消、状态码与聚合错误 | 新增无人使用公开入口、非幂等重发、超限仍等待、提前重试或承诺执行满次数 | 私有出站韧性实现；已完成 |
| 5 | 前述阶段已验收；5.1 缓存/内存/六类能力契约；5.2 OpenAPI 与生产路由双向对照；5.3 交叉核对与结果 | 不改业务/表/依赖；local/external 注册路由契约测试；配置说明一致；普通测试、产品与独立工具各自编译、当前 change strict validate | 未授权检查不执行；缺真实环境不标生产通过 | 已完成；验收与门禁例外见第 13 节 |

分阶段普通验证入口（全部待实施，不隐含 vet）：
| 阶段 | 命令 | 结果覆盖范围 |
|---|---|---|
| 1 | `GOTOOLCHAIN=local go test -vet=off -count=1 -timeout=120s ./internal/optional/taskqueue` | 当前队列关闭修复 |
| 2 | `GOTOOLCHAIN=local go test -vet=off -count=1 -timeout=120s ./internal/middleware ./internal/router ./internal/runtime/module/auth ./internal/module/auth/... ./internal/platform/config ./internal/pkg/metrics ./internal/bootstrap` | 准入、认证、当阶段新增的 metrics 及 Config → buildEngine → Router 的真实装配 |
| 3A | `GOTOOLCHAIN=local go test -vet=off -count=1 -timeout=120s ./internal/pkg/metrics ./internal/platform/telemetry ./internal/bootstrap` | 阶段 3A 再次修改后的 runtime/池 metrics 和生命周期 |
| 3B | `GOTOOLCHAIN=local go test -vet=off -count=1 -timeout=120s scripts/load-api.go scripts/load-api_test.go` | build-ignore 工具的当前源码 |
| 3B | `GOTOOLCHAIN=local go build -o .cache/capacity-optimization-20261026/load-api scripts/load-api.go` | 独立工具编译，不纳入产品 ./... |
| 4 | `GOTOOLCHAIN=local go test -vet=off -count=1 -timeout=120s ./internal/optional/outbound/...` | 当前私有重试实现与原出站契约 |
| 5 | `GOTOOLCHAIN=local go test -vet=off -count=1 -timeout=120s ./internal/bootstrap` | 阶段 5 新增 OpenAPI 对照测试后的 bootstrap 版本 |
| 5 | `GOTOOLCHAIN=local go build ./...` | 最终产品包编译 |
| 5 | `python3 .openspec-auto/tools/run_openspec.py validate harden-capacity-and-outbound-resilience --type change --strict --json --no-interactive` | 实施后同步的本 change 文档 |

metrics 在阶段 2 和 3A 各自发生修改；bootstrap 在阶段 2 修改配置接线及装配测试，在 3A 修改采集注册，在 5 新增 OpenAPI 对照测试，因此这三次包级回归分别对应不同变更，不因包名重复就删除。记录每次对应的源码版本/指纹；阶段 5 可复用同一最终版本下仍有效的分阶段结果，只补跑新增或再次受影响的范围。同一源码版本、同一验收批次的相同检查只执行一次；后续变更使证据失效时应重跑。
独立工具测试与产品构建是两类证据，不能用产品 ./... 通过替代 build-ignore 工具验证。实际运行记录命令、环境、退出码和原始输出；同一问题最多三轮，不扩大修改范围。

阶段 3A 与 3B 分别记录结果和文件 diff：3A 回滚指标与 bootstrap 注册；3B 回滚独立工具及其说明，不连带移除阶段 2 已验收的拒绝/排队指标。阶段 5 的 OpenAPI 测试只能核对路由集合，不能证明全部业务语义或生产可用性。

## 7. 兼容、风险、失败与回滚
- 预期行为变化：业务同时处理超限返回 503；密码满载默认最多再等待一批请求且最长 1s；队满或排队超时返回 503，显式配置 0 等待者才快速拒绝；认证正文超过 8 KiB 返回 413；可重试调用的退避随机化且受 30s 累计等待预算约束。
- 重试兼容性变化：现有 WaitTime 或 MaxWaitTime 超过 30s 的策略在实施后会校验失败，即使过去能够构造或运行。Attempts 是最大重试次数，成功、分类、取消和等待预算可使其提前结束；全抖动下不保证实际重试次数固定。outbound 默认未接线只限制默认服务的影响面，不免除启用方的兼容核对。
- 令牌协议、数据库 schema、缓存值、任务 payload、幂等规则和错误信封结构保持不变。新增忙错误仅补 503 映射。
- 最大风险是容量初值导致误拒绝；所有初值标为未校准，按 L≈λ×W、DB 等待、503 和 CPU/RSS/尾延迟实测调整。禁止降低密码计算成本来提高吞吐。网关不得对过载 503 无条件重试；本次不以固定 Retry-After 暗示恢复时间。
- 采集不能在请求热路径执行 GC/阻塞 I/O；回调生命周期必须早于依赖释放结束。
- 失败先记录 .harness/error-journal.md；区分产品失败、用例夹具和本机条件。只修本次引入问题。
- 回滚按本次精确 diff 撤销（不使用 reset --hard、不覆盖用户 ADR）；恢复对应配置项和 README。无数据库迁移、无业务数据回滚、无依赖回滚。
- 默认不提交、不推送、不发布、不归档不满足证据的 change。功能完成后单独询问受控检查授权。

## 8. 当前证据与未验证
### 2026-10-26 全阶段实施轮次
- 用户已明确授权全部阶段。已回读修订 5、全局及项目规则、相关错误记录，实际 Go 依赖以 go.mod 为准。
- 当前真实 session 为环境 CODEX_THREAD_ID 返回的 01a17773-8495-73b0-a33e-c79554cbf151。workflow.py status 返回 no request state；用户随后明确允许本次跳过并继续。未手工构造授权状态，不修改 hook、权限或其他会话状态；使用普通命令保留真实结果，仍遵守 OpenSpec 和受控检查边界。
- 以下为历史记录，不作为本轮测试结果；本轮各阶段的真实命令、退出码、证据及未验证项在此追加。
- 阶段 0：Go 1.27.3 darwin/amd64；本 change 的 apply instructions 返回 ready，所需 artifacts 已齐；已将决策、specs 和阶段任务同步为修订 5。
- 阶段 1：原实现回归失败（第二次 Shutdown 为 nil）；修复后 `GOTOOLCHAIN=local go test -vet=off -count=1 -timeout=120s ./internal/optional/taskqueue` exit 0。首次整包因测试夹具跨 synctest 边界超时，已记录 ERR-20261026-198 并修正；通过版本把 Producer 创建在同一 bubble，未改变被测语义。
- 阶段 2 文件补充：load.go 已有整数存在性字段校验；新等待人数同样不能被弱类型解码截断浮点数或转成布尔值。追加此精确文件，不改加载顺序、配置来源或架构。
- 阶段 2 发现存量 readiness 测试假定立即恢复，而现有探针缓存 1s；在 .cache/capacity-optimization-20261026/baseline 以 HEAD 快照隔离复核，不修改存量探针或其测试。新 bootstrap 夹具缺应用名，将补齐夹具后重跑。
- 阶段 2：`stage2.log` 中 middleware、runtime/module/auth、auth application/transport、config、metrics、bootstrap 均 exit 0。router 只有 TestReadinessKeepsPrivateCauseInRateLimitedLogs 失败（缓存未过期时仍为 503）；在 HEAD 快照用 GOWORK=off 单独执行该用例，同样失败（baseline-readiness-isolated.log）。router 排除这一已证实存量用例后的整包 exit 0（stage2-router.log）。未修改存量测试或探针，不能声称原始阶段命令全部通过；容量验收本身通过，继续其余已授权阶段。
- 阶段 3A 补充 app_test.go：新增 runtime/Redis 回调后，旧清理列表精确断言需要列入这两项；仅同步预期清理资源，不放宽清理顺序或生命周期校验。
- 阶段 3A：metrics 和 telemetry 整包普通测试通过，补齐清理列表后的 bootstrap 整包也通过（stage3a-metrics.log、stage3a.log、stage3a-bootstrap.log）。首次 bootstrap 失败仅为旧清理列表尚未纳入两项新回调，已同步精确预期。
- 阶段 3B：独立文件清单测试 exit 0（stage3b.log），独立 build exit 0；真实 loopback HTTP 演练输出保存在 load-fixture.json，只证明工具功能。固定桶、计划到达计数、超时取消、目标限制和禁止重定向均有测试，尚无真实业务容量结果。
- 阶段 4：`GOTOOLCHAIN=local go test -vet=off -count=1 -timeout=120s ./internal/optional/outbound/...` exit 0（stage4.log）。覆盖实际 HTTP Retry-After 两种格式、超大/溢出头、累计预算、抖动、零等待、取消及跨请求元信息隔离；未迁移 restyx 或启用可选出站模块。
- 阶段 5 功能完成：OpenAPI 覆盖 6 条实际路径，真实 local/external 服务装配路由对照随 bootstrap 整包通过（stage5-bootstrap.log），产品 `GOTOOLCHAIN=local go build ./...` exit 0。缓存/内存、六类可选能力和出站兼容边界说明已写入；开始执行用户最新完整授权覆盖的质量检查，再提交推送。

### 历史准备与修订记录
- 历史修订 1 已执行：源码/配置结构/依赖实现只读核对、本机 Go/Node 版本、OpenSpec list/创建/status/strict validate。其校验只对应当时 artifacts，不代表修订 5 已通过。
- 本轮仅做必要的文件只读核对与主方案文字修订；未运行测试、构建、lint、vet、modern、覆盖率、OpenSpec CLI 或其他检查。
- 修订 1 准备期已追加 ERR-20261026-196 记录路径/通配查询失败；本轮未改错误日志，没有把后续命令的成功退出码算作整组通过。
- 尚未执行：功能代码修改、测试、构建、压测、受控检查、真实依赖/生产容量验收。
- 已有根工作记录属于历史任务，历史测试结果均不作为本次证据。

## 9. 修订 2 审阅处置（均为方案修订，尚未实施）
| 用户意见 | 本版处置 |
|---|---|
| 独立 Go 工具位置/命令 | 改到 scripts/，双文件 build-ignore，显式文件清单测试和编译 |
| GC 暂停与等待指标类型 | 选累计暂停次数＋累计分位上界 gauge；累计等待使用自定义 counter 名，不借逐次直方图名 |
| 密码默认零排队过激 | 缺省等待人数=执行并发；显式 0 禁等待；1s 与父 ctx 双重约束 |
| Redis uint32 回绕及 pipeline | 实例分代＋同代取模增量，主池与独立 pipeline 分开，记录观测不可恢复边界 |
| Retry-After 无绝对等待边界 | 私有 30s 单次/累计预算，超限返回最后结果；即使无 deadline 也不睡一天 |
| 拒绝和排队指标晚一阶段 | 与阶段 2 行为同批交付；原阶段 3 拆 3A/3B |
| 准入位置、长连接、0/关闭、DB 关系 | Admission 在本组 Timeout 前；长连接另行分组；0 默认且不提供关闭；补容量公式和 DB 25 对照 |
| 503 响应头及网关重试 | 不写固定 Retry-After；注明幂等、抖动、重试预算和避免无条件 503 重试 |
| coordinated omission 与样本内存 | 从计划发送时刻计时、补记漏发负载、固定桶和显式丢弃率 |
| OpenAPI 漂移 | 阶段 5 新增生产 buildEngine 的 engine.Routes() 双向集合测试 |
| semconv 与旧面板兼容 | 已存在规范名称按真实类型使用，旧 db.client.connections.* 名称保留 |
| 无调用方的公开重试入口 | 私有引擎与 transport 元信息接现有入口；restyx 真正接入时再评估公开复用 |

## 10. 修订 3 审阅处置（仅方案修订，均待实施）
| 已确认意见 | 落盘决定 |
|---|---|
| 自定义指标前缀 | 使用 pool.wait.duration、runtime.gc.pause.count、runtime.gc.pause.quantile；不嵌入项目名，不新增占用 db./go. 规范指标族 |
| 等待人数默认配置 | 示例与本地配置明确使用 bcrypt_max_waiters: null；省略/null 为默认，0 禁排队，逐项验证解码存在性 |
| 重试最大次数和兼容影响 | Attempts 为最多次数；预算可提前结束，不要求最坏等待序列一定装入预算；第 7 节列明超过 30s 参数的新拒绝行为 |
| GC 累计分位用途 | 仅长期分布/基线参考，不用于近期暂停回归告警；本期不增加回调差分窗口 |
| 阶段 4 依赖 | 只要求共同前置与阶段 1；不再依赖阶段 2、3A、3B，阶段 5 汇总两个分支 |
| 验证入口重复 | 按阶段标注源码版本；保留修改后的必要回归，同一版本同一批次去重 |

## 11. 修订 4 审阅处置（仅方案修订，均待实施）
| 最新意见 | 落盘决定 |
|---|---|
| 指标属性不明确 | 4.3.1 增加“标签键和取值”列，固定 HTTP method/scheme、池名/类型/库号/角色和其他仪表属性；在途/排队数量不带 outcome；仅等待完成或拒绝决策记录结果 |
| semconv 必填属性 | HTTP 活跃请求进入/退出复用相同 method/scheme；池等待及超时均带 db.client.connection.pool.name；读取本地锁定版本源码核对，不新增依赖 |
| 阶段 2 漏 bootstrap 回归 | 阶段 2 命令加入 ./internal/bootstrap，现有 assembly_contract_test.go 归属阶段 2，验证非默认配置经 buildEngine 实际生效 |
| 文件与阶段对应不清 | 第 5 节逐文件列明阶段与变更目的；多阶段文件各自记录 diff 和版本，不删除必要回归 |

## 12. 修订 5 审阅处置（仅方案修订，均待实施）
| 最新意见 | 判断与落盘决定 |
|---|---|
| Redis total/idle 状态重叠 | 需要修正。使用 db.client.connection.count 和 Int64ObservableUpDownCounter，状态键为 db.client.connection.state=used/idle；先转 int64 再计算 used=max(TotalConns-IdleConns,0)，分池验证。MySQL 既有名称及 db.connection.state 保持不变 |
| 准入指标 unmatched 不可达 | 需要修正。http.route 只允许已注册路由模板；404/405 不进入业务组准入、不产生该拒绝计数。保留既有全局请求指标的 unmatched 处理 |

## 13. 实施验收记录

环境：Go 1.27.3 darwin/amd64；golangci-lint 2.15.0（与 security/toolchain.mk 一致）。本地测试使用内存替身、miniredis 和 loopback HTTP；没有连接生产、创建容器或升级依赖。来源为本版已批准设计与用户后续完整授权。全部产品改动遵循原分层；本地配置只同步三项容量字段，仍保持忽略。用户原有 ADR 改动不纳入提交。

| 范围 | 实际证据与结果 |
|---|---|
| Producer | 先复现原始第二次 Shutdown 提前返回 nil；修复后整包通过，覆盖取消、继续等待、并发关闭与共享客户端 |
| 准入与认证 | 阶段 2 普通回归和后续受影响 race 通过；Config→buildEngine 的非默认上限实际生效，探针及 404/405 隔离；哈希执行/等待有界，503 错误映射、真实排队指标、8 KiB 声明/流式/有效请求边界通过 |
| Runtime/DB/Redis | 整包普通及 race 通过；类型/属性/单位、used/idle、分代、回绕、独立 pipeline、同代异常回退和注销通过；大累计量不转成负数；旧 MySQL 名称/标签保留 |
| 压测工具 | build-ignore 文件清单普通/race 测试与 errcheck 通过，独立编译通过；本机演练计划 40、发送/完成 39、发生器丢弃 1、传输错误 0；该结果不是业务容量 |
| 出站重试 | 整包普通/race 通过；公开 API 保留，0 等待、次数/时长校验、抖动、累计预算、Retry-After 两种格式/溢出、取消及元信息隔离通过；未引入 restyx |
| 契约 | 新增 OpenAPI 6 条路径，真实 local/external 装配双向对照通过；只补六类能力契约，未编造业务实现 |
| 构建/vet/modern/架构 | 产品 go build、带 scaffold 的 go vet、go fix -diff（空输出）以及 scripts/check-architecture.sh 通过；最终修改过的 metrics 又单独复核 |
| 全量 race | `go test -vet=off -tags scaffold -race -count=1 -timeout=5m ./...` 已执行，没有 DATA RACE；唯一失败为下述已证实存量 readiness 用例，不能报告全量命令通过 |
| 覆盖率 | `go test -vet=off -tags scaffold -covermode=atomic -coverprofile=.cache/capacity-optimization-20261026/coverage-current.out -count=1 -timeout=5m -skip '^TestReadinessKeepsPrivateCauseInRateLimitedLogs$' ./...` exit 0；随后 check-coverage.sh 得到 84.7% ≥ 72.5%。明确排除存量用例，不能冒充未排除的全量测试 |
| Lint | 完整 golangci-lint 只剩下述 3 项存量问题；HEAD 快照复现相同结果；本次最后修改的 metrics 和独立工具 errcheck 分别通过 |
| OpenSpec | 当前 change strict validate：1 passed / 0 failed；不对无关 change 宣称通过 |

归档输出确认新增 13 项 requirements、更新六个对应能力，未触及其他 change；六个 spec 分别严格验证通过。OpenSpec 与错误日志延续仓库既有忽略约定保留在本机；产品契约文档与本方案纳入本次提交。

存量问题（不在本次优化修改范围）：
- `internal/router/panic_observability_test.go:156`：在 1s readiness 缓存未到期时要求立即从 503 恢复 200；未改动 HEAD 快照同样失败。
- `internal/bootstrap/http_error_log_test.go:26`：既有 output.Close 未检查错误。
- `internal/middleware/routetimeout_test.go:187`：既有类型断言未检查。
- `internal/bootstrap/validation_test.go:16`：既有 newProviders 未使用。

原始日志和源码 SHA-256 指纹保存在 `.cache/capacity-optimization-20261026/`；失败、夹具修正及过早读取未完成覆盖率文件的无效结果已如实记入本地错误日志，不覆盖为成功。

后端验收：运行行为与失败路径已按 backend-acceptance 对照；数据库 schema、令牌协议、缓存值和任务 payload 未改。生产流量比例、机器配额、真实 DB/Redis 容量、滚动发布与真实业务 SLO 未提供，本次不作生产准入或单机容量结论。Git 提交仅用于交付实现，不表示生产验收通过。

合规核对：已按项目 Go 版本使用标准能力；分层、错误传播、取消、资源所有权、容量边界和文档交叉核对均有对应源码或功能证据。未运行或未提供的真实环境证据保持未验证，不与构建/单测混为一谈。
