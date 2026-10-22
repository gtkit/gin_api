# gin_api 验收问题整改方案与分阶段实施计划

日期：2026-10-22（Asia/Taipei）。

**当前状态：2026-10-22 用户已指示“DB 和 Redis 暂不考虑集群，所有 error 不忽略，基于已更新 goerr 优化错误处理，其他按方案实施”。核心阶段1–6及X1按该指令执行；集群明确排除。阶段1–5核心源码和阶段6本地复测/报告已完成；X1源码与解析验证完成，容器运行未验证，归档保留检查点；实际证据见第12节及结果报告。** 上一轮仅文档的历史边界保留为历史记录；本轮授权、错误处理补充和进入条件以第11节为准，设计章节保留实施前候选措辞，执行状态以本轮记录及结果报告为准。后续命令在取得真实执行证据前均为待执行。

## 1. 需求来源、事实与目标

### 1.1 来源与基线

- 本轮指令：基于验收报告，先对与代码功能有关的问题制定完整整改方案和分阶段实施计划，保存到项目文档目录并回读，暂不修改代码。
- [完整验收报告](/var/folders/_w/xwv4k3t12vn9054592gsk5200000gn/T/gin_api_acceptance_20261022_uebeqqry/gin_api_acceptance_report.md)：15 个维度、70 项检查；F01–F18 的原编号保持不变。下表中的历史等级沿用报告，不以重新分类掩盖问题。
- 验收基线提交：`3bd0e5903a11717fa9464fa4f5d31d57446ad0ff`。本轮规划开始时仍只有已有 `go.mod`、`go.sum` 未提交改动；不是干净 HEAD 验收。
- 已有改动必须保留：`go.mod` SHA256 为 `b5eac2f63027132e8105b4899c0f2a8893dbbb3ba1357da8d02224477cc790ba`；`go.sum` 为 `dbe4162466a8b31479e392a781481f72c6ff14c7edc4d966c1f632d9f430a178`。不能用 checkout/reset 回退它们。
- 项目现有规则、相关 Guide、错误记录和既有计划已读取；既有插件/日志整改计划保持原状，不重新开启其已完成阶段。历史通过记录不能当作本次整改复测。

当前锁定版本：Go 声明 1.27.0，验收执行器为 1.27.3；Gin 1.12.0、GORM 1.31.2、goerr 1.5.0、logger/v2 2.3.0、go-redis/v9 9.23.0、redisx 1.4.0、x/sync 0.24.0、Cobra 1.10.2。本方案不升级这些依赖，不修改 module 路径或发布主版本。

### 1.2 已有证据及边界

| 事实 | 已有验收证据 | 能证明什么 / 不能证明什么 |
|---|---|---|
| 缓存 loader panic | [反证日志](/var/folders/_w/xwv4k3t12vn9054592gsk5200000gn/T/gin_api_acceptance_20261022_uebeqqry/fault-probes.log)：独立子进程 exit 2 | 可复用缓存能力存在进程退出风险；当前内置路由无此调用点，不能宣称公网已可触发 |
| 嵌套事务独立提交 | 同一日志：外层 rollback 后仍有 1 行 | `Manager.Do` 不支持嵌套原子性；源码已说明限制，当前没有嵌套业务调用点 |
| Redis 前缀未消费 | 配置声明存在；缓存、认证及限流装配读取 `App.Name` | 配置存在不代表生效；命名空间切换涉及刷新/撤销状态，不能当作纯字符串替换 |
| 完整门禁失败 | [make check 日志](/var/folders/_w/xwv4k3t12vn9054592gsk5200000gn/T/gin_api_acceptance_20261022_uebeqqry/make-check.log)：旧 goerr 1.4.0 的两行 sum，exit 2 | 依赖整洁门禁失败；不是已锁定 goerr 1.5.0 无法编译 |
| 普通回归基线 | 两种 workspace 口径的全仓 race 通过，83.0% 覆盖率；真实依赖、HTTP、故障恢复和迁移有记录 | 仅是整改前历史基线；不能抵消上面两项反证失败，也不是容量准入 |
| JWT 诊断冲突 | 生成私钥 0600、公钥 0644；doctor 对所有钥统一按私钥检查 | 可复现的生成→诊断契约不一致；不能把公钥可读等同私钥泄漏 |

### 1.3 可验证目标

1. loader panic 成为可识别、可观测的错误，子进程不退出，等待者及在途名额正常收尾。
2. 同一事务管理器的嵌套调用不独立提交；外层失败不留下内层写入，失败回滚错误不被吞掉。
3. Redis 前缀来源唯一且真实消费；默认行为兼容旧 key，显式切换不能使撤销记录失效或打开刷新重放窗口。
4. 诊断、常用 Make 入口与实际运行一致；必需集成套件确实进入门禁，不能以 skip 或占位成功替代执行。
5. 按附件补齐响应关联、生产日志保护及可配置慢 SQL；版本信息作为低风险功能增强，单独验收兼容性。
6. 每阶段保留命令、退出码、测试事件、配置口径和失败路径证据；未验证项保持未验证。

### 1.4 非目标与边界

不调整目录层级、模块边界、依赖方向或插件架构；不删改业务模块、迁移历史或用户数据；不替换 JWT 算法、令牌格式、密码算法及错误码体系；不升级依赖、安装全局工具、提交、推送、打 tag、发布 v2 或部署生产。没有业务 QPS、数据规模和 SLO，不承诺容量数字。

F09/F10 属于额外能力建设，单列受控方案，不能混入故障补丁。F12/F17 属于规范冲突，先记录裁决条件，不能通过擅改中间件顺序或关闭服务器错误日志来“修复”。文档/风格/忽略规则的独立优化不在代码功能整改范围；与具体功能变更直接相关的示例和说明必须同步。

## 2. 问题分类与处置追踪

“实际缺陷”表示当前实现或工具链内部契约不一致；“能力缺口”表示附件要求超出现有明确承诺；“规范冲突”不能直接推导运行错误。以下分类是整改范围判断，不修改原报告结论。

| 编号 / 原等级 | 性质与准确边界 | 本方案处置 | 阶段 |
|---|---|---|---|
| F01 / 严重 | 条件触发的实际稳定性缺陷；未接入内置路由 | 必修：异步边界恢复、错误语义、资源收尾及子进程反证 | 1 |
| F02 / 一般 | 已声明不支持的事务能力缺口；不是现有嵌套业务回归 | 补同管理器 savepoint 语义；跨管理器/未知事务拒绝，禁止默默另开事务 | 1 |
| F03 / 一般 | 实际配置消费缺陷，具有状态兼容风险 | 统一来源；旧实例保留原 namespace，切换作为独立状态操作 | 2 |
| F04 / 一般 | 附件要求的协议增强；现有 Guide 只承诺 code/msg/data | 提议新增 request_id；不能称现有三字段响应本身不可用；批准契约后实施 | 3 |
| F05 / 一般 | 实际依赖门禁失败，叠加用户已有依赖改动 | 只整理旧 sum；保留用户选择的 goerr 1.5.0 | 5 |
| F06 / 一般 | 实际生成器/运行时/诊断契约不一致 | 区分私钥保密与公钥完整性，不把公钥读取限制套成私钥规则 | 4 |
| F07 / 一般 | 实际命令误导与入口缺口；test 是迁移环境占位 | 补入口且保留位置参数兼容，不直接覆盖占位语义 | 4 |
| F08 / 一般 | 实际门禁覆盖遗漏；cmd 另有 opt-in | 修枚举和强制模式；先隔离测试配置，防止新接线读取项目真实配置 | 5 |
| F09 / 一般 | Compose 明确只编排依赖，和附件三服务要求不一致 | 能力扩展 X1；需单独批准范围，不改冻结的生产部署流水线 | X1 |
| F10 / 一般 | 当前是单机多逻辑 DB，附件要求集群切换 | 能力扩展 X2；涉及客户端接口、多 DB 与 Lua 约束，不作为小修 | X2 |
| F11 / 一般 | 附件生产保护缺口；当前显式 debug 配置会被接受 | 增加 prod/debug 拒绝规则；现有合法非 debug 配置保持 | 3 |
| F12 / 一般（冲突） | 附件顺序与现有双 Recovery 设计/Guide 不一致 | 默认保留实现；单列规范裁决，不能按字面顺序直接重排 | C1 |
| F13 / 低风险 | CLI 版本与构建追溯增强，无已证实业务故障 | 保留版本单一来源，补可查询版本/commit/构建时间 | 4 |
| F14 / 低风险 | 文档语义过期及交付说明不足 | 只同步本次功能变更涉及部分；全面文档治理另行授权 | 配套 |
| F15 / 低风险 | 忽略规则不足，未发现对应秘密已提交 | 本期只列风险，不顺带修改 .gitignore 或扫描全历史 | 范围外 |
| F16 / 低风险 | SQL 观测配置增强；现有慢日志和脱敏已工作 | 补一个兼容旧默认的阈值/关闭配置，不改普通 SQL 开关语义 | 3 |
| F17 / 一般（冲突） | net/http ErrorLog 的标准 log 适配违反绝对禁用文字；输出仍到 gtkit | 需维护者裁决窄例外/替代约束；不删除 ErrorLog，不绕用 slog | C2 |
| F18 / 低风险 | 文件头、tag 形式差异；已有手写 Validate | 不批量改头注释，不为 tag 形式重写配置系统；新字段按现有规范校验 | 范围外 / 配套 |

## 3. 总体方案与候选比较

| 候选 | 正确性、改动面与成本 | 兼容与回滚 | 判断 |
|---|---|---|---|
| A：按现有包职责分阶段修复，功能增强单独锁定契约，扩展与规范冲突设置条件 | 修改集中在缓存、事务、配置、装配、响应、CLI 与门禁；不引入新基础设施；每阶段有反证 | 无数据库 schema 变更；Redis namespace 作为唯一特殊状态边界处理；可逐阶段撤回 | 推荐，待审阅 |
| B：按附件字面一次替换中间件、客户端及配置体系，并同时补齐集群/应用编排 | 把稳定性修复与接口扩围混在一起，扩大跨包及部署面；成本/容量尚无数据 | 混部、令牌状态、Lua 同槽和多 DB 回退无法由一次构建证明 | 不采用 |

后续核心执行顺序固定为阶段 1→2→3→4→5→6；阶段 0 是本轮文档交付。用户若只授权其中一阶段，完成并验收后停在该阶段；若明确授权全部核心阶段，按此顺序推进，不重复索要同一授权。X1/X2 及规范裁决不因批准核心阶段自动获得实施授权。

F05 安排在阶段 5，故阶段 1–4 的局部通过不代表整仓门禁已绿；当时若运行完整检查，仍须如实保留依赖整洁阻断。不能为消除阶段外红灯提前改用户依赖，也不能据局部结果宣称母版已准入。

## 4. 详细设计与兼容契约

### 4.1 F01：异步 loader panic

位置：[cache.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/pkg/cache/cache.go) 的 `GetOrLoad` / `singleflight.DoChan` 回调。

- 保持 `New`、`GetOrLoad` 的签名、泛型结果、TTL、LRU、singleflight、失效屏障、取消及超时契约。不能为恢复 panic 增加无界 goroutine，也不能把共享加载绑定到首个等待者的取消。
- 在实际执行 loader 的异步回调内恢复 panic；调用方或 Gin 上的 recover 不能跨 goroutine 生效。恢复路径必须正常返回错误，使 singleflight 广播结果；不能 recover 后继续走成功缓存回填。
- 拟新增可识别的缓存 loader panic 错误（这是待新增契约，不是已有 API）。错误对外文案固定，不回显 panic 值、缓存 key 或请求秘密；受控日志保留堆栈及现有请求关联。普通 loader error 原样保留可判断语义；不新增自动重试。
- panic、error、超时、等待者取消均须准确释放自己拥有的在途登记；仍在运行的 loader 不能因等待者离开而被误计为已释放。沿用现有 `finishLoad` / `Forget` 的同步设计，避免清理旧登记时误删同 key 新一轮加载。
- 没有响应体时不要为了“友好错误”伪造 HTTP 响应；只有实际 HTTP 调用方才映射既有内部错误。

验收不变量：子进程 exit 0；共享同 key 的等待者均得到受控错误或各自取消；不缓存错误值；名额可再次使用；不同 key 的后续加载可用；失效屏障及上限 64 的默认语义不退化。无视 context 的 loader 仍不可被 Go 强杀，不宣称本修复解决其执行终止问题。

### 4.2 F02：嵌套事务

位置：[dbtx.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/repository/dbtx/dbtx.go)。

候选：直接拒绝嵌套可快速阻止独立提交，但仍不满足附件的嵌套能力；无条件复用 ctx 事务可能接入其他管理器的数据库；采用同管理器 savepoint 可以限定语义。**推荐同管理器 savepoint，其他情况显式拒绝**。

拟定契约：

1. 外层 `Do` 拥有唯一物理事务；同一个根 DB 所属的管理器在 ctx 中记录事务归属及事务状态。保留 `With`、`From`、`DB` 的现有仓储取用行为，不让 application 接触 GORM。
2. 同归属嵌套调用使用外层连接上的 savepoint，不调用底座 DB 的独立 Begin。内层成功不提交物理事务；外层 error/panic/取消必须撤销全部写入。
3. 内层 error 回滚到自己的 savepoint并返回原错误；外层显式处理该错误后可以继续并提交其他写入。跨管理器、只有外部 `With` 且无可确认归属、或禁用了所需嵌套能力时，进入用户回调前返回明确错误，不猜测跨库原子性。
4. SavePoint 建立失败不执行回调；RollbackTo 失败必须保留原错误及回滚错误，并将外层置为不可提交。不得在不确定回滚成功时允许外层“成功提交”。内层 panic 经回滚后继续向现有外层恢复边界传播，不吞掉编程故障。
5. 已读取的 GORM 1.31.2 `Transaction` 嵌套分支使用 savepoint，但其 defer 不返回 `RollbackTo` 的错误。因此不能仅替换成 `DB(ctx, m.db).Transaction(...)` 就宣称完成全部失败契约；实现需要显式保留这类错误及不可提交状态。
6. savepoint 标识只能来自管理器内部，不能拼入用户数据；同一事务中的嵌套序列须避免冲突。并行复用同一事务 ctx 不承诺独立并行 savepoint 调度；检测到无法保证的用法即失败，不扩展成新的并发事务框架。

无 schema 迁移、不重写现有业务事务、不新增自动死锁重试。复测覆盖外层失败零残留、内层局部回滚、两层成功、panic、取消、跨管理器拒绝、savepoint/rollback 失败和 max_open=1 的连接等待。

### 4.3 F03：Redis 命名空间

现有外部耦合点必须逐项保留：

| 链路 | 当前事实 | 拟变更 / 兼容要求 |
|---|---|---|
| 来源 | `RedisConfig.Prefix` 仅声明；消费者取 `App.Name` | 在现有 config 包增加单一生效取值：显式非空 Prefix 优先，否则原样回落 App.Name；不得擅自 trim、改编码或增加分隔符而改变旧 App.Name 字节 |
| 认证 | `providers.go` 注入 KeyPrefix；客户端使用逻辑 DB 0 | 只换来源，不改 DB、哈希算法、令牌格式、刷新 TTL 或 clock skew |
| 撤销 key | `<prefix>:revoked:<hash>` | key 后缀及 exp+skew 保留；禁止丢失原撤销状态 |
| 刷新 key | `<prefix>:refresh:{family}:<hash>` 及同 family 的 revoked key | 保留 family hash tag、原子 Lua、轮换/重放语义与 TTL；不能直接新建一个空刷新库后宣布迁移成功 |
| 限流 | `ratelimit.go` 的 KeyBuilder 和 RedisBackend 都取 App.Name | 两个入口同步；保留 `:rl:`、策略/方法/路由/scope/HMAC、DB 及失败模式，不能只改其中之一 |
| 响应缓存 | cache 插件传 App.Name，httpcache 追加 `:httpcache:` | 改为同一来源；保留所选 `cache.redis_db`、TTL、身份绕过及缓存响应准入 |
| 测试 | 之前配置唯一 Prefix 但 App.Name 仍为 app_client | 新测试同时核对生效值和实际 key；每次记录确切归属，不靠配置文本推断隔离成功 |

配置契约：Prefix 缺省或空串时保留当前 namespace；显式仅空白的值拒绝，避免“看着填了”却无可审计含义；非空值作为原始字节前缀，不暗改末尾冒号。示例保留空串并写清回落到 application.name，环境覆盖仍按现有 APP_ / 双下划线规则。任何新增限制必须和现有合法值兼容，不顺便改 application.name 的校验规则。

**部署与状态迁移选择：本期不引入双读双写、不扫描搬运旧 token key，也不自动改生产 namespace。**

- 旧实例升级前核对“旧实际 App.Name / 原配置 Prefix / 拟生效值”。如果原 Prefix 非空且不同于 App.Name，停止该实例升级，先由维护者确认保留原 namespace 的配置。不能把以前被忽略的值悄悄激活。
- 推荐兼容发布时把 Prefix 留空，或显式等于旧实际 namespace。这样新旧二进制、令牌、撤销记录和限流桶可共存；版本回滚不依赖数据搬迁。
- 只有新项目/全新隔离实例可以直接选择新 Prefix。存量实例若确需改名，另行批准停流、令牌失效/重新认证与限流窗口重置方案。等待旧 key TTL 耗尽并不自动解决所有状态：fence 等永久状态、队列或其他消费者必须分别盘点。不能凭样例 `refresh_ttl=720h` 假定所有实例都在 30 天内可安全切换。
- 原验收可能残留在 app_client 的 TTL 测试键，归属及数量仍未确认。本轮不删除该共享前缀；仅清理第12节明确归属的新独占namespace，不能声称历史共享键已清干净。

### 4.4 F04：request_id 协议增强

候选一是继续只承诺响应头并修订验收契约；候选二是为统一 JSON 信封增加字段。推荐按附件选择二，但须把它作为对外契约增量审阅。

- 在现有 `ResResult` 增加 `request_id`，保留 `code/msg/data` 名称、类型、空数据策略、错误码、HTTP 状态及既有 helper 的调用签名。纯构造 helper 没有请求上下文时不自造请求标识。
- 在响应写出时从现有服务端关联上下文取得当前 ID；保持 `X-Request-ID`、日志和 JSON 一致，禁止信任任意客户端同名头。无已分配 ID 的极早失败路径使用空字符串并明确记录边界，不临时启动第二套 ID 生成器。
- 健康探针、流式/SSE、非 JSON 响应保持既有独立协议；如果附件需要这些也套信封，必须另行批准，不能破坏探针与流式客户端。
- **缓存命中必须专项处理**：缓存存的是序列化响应，不能把首次请求的 request_id 带给后续请求。保留通用缓存原样返回其他数据的能力，不对任意 JSON payload 做同名字段替换。
- 已只读核对锁定的 gincache 1.3.1：`WithOnHitCache` 和 `WithOnShareSingleFlight` 的回调仅接收 gin.Context，不提供可修改的缓存响应参数；回放由库内 `writeResponse` 完成。因此不能假设有“替换响应体”现成选项。采用下述现有 httpcache 包内的局部适配，不升级依赖、不修改库缓存对象或关闭全部响应缓存。
- 增量 JSON 字段可能被严格 schema、签名或快照客户端拒绝；这些调用方目前未确认。切换前补兼容样例和受影响调用方清单，不能把“增加字段”写成无条件无破坏。

统一信封缓存的拟定适配规则：

1. 在现有 httpcache 包拟新增显式的统一 JSON 缓存包装 helper（候选名 `UnifiedJSONCache`，并非已有 API），包住现有缓存 middleware。仅将明确使用 `ResResult` 的普通 JSON 只读路由迁入此模式；原通用缓存接口及非信封数据原样保留。使用内部请求上下文标记声明模式，不对外增加实现标记头。
2. 此模式的派生缓存 key 增加固定 `:envelope:` 域，与旧通用响应缓存分开，避免读取升级前无 request_id 的旧信封。只影响可丢弃的响应缓存，不涉及 F03 的认证/撤销/限流状态 key；代价是这类路由一次冷缓存，须记录。
3. 在缓存 middleware 外层安装请求独有的 Gin writer 适配，首发、命中及 singleflight 等待者都经过它。只处理一次完整的普通 JSON 信封写出；以 RawMessage 等保留 data/数值语义，只更新顶层 request_id，不修改共享 ResponseCache、嵌套同名数据或其他请求的缓冲。
4. 该模式是“每请求信封、可共享data”，不承诺整包字节恒定。移除固定 Content-Length 和基于整包的 ETag/Last-Modified/摘要头；用请求头副本屏蔽 If-None-Match/If-Modified-Since 在该模式下的缓存条件复用，使普通 GET 返回含当前ID的200。原通用缓存的304/validator规则保持。这个变化必须写进协议兼容说明；要求保留整包签名/条件复用的路由不迁入此模式。
5. HEAD、204/304等无正文响应不补JSON、不制造body，仅保留本次X-Request-ID；探针和SSE仍不进入此模式。压缩/已签名/任意分块输出的路由不适用此包装，登记为继续使用现有协议的例外，不能解压重签或静默改原始字节。
6. 适配额外内存受现有 max_body_bytes 上限约束，不能为了重写ID收集无界body。超过上限的首发响应保留response helper已生成的当前ID并走既有不缓存路径；本模式只有合格且有界的信封可写入/回放。格式不符或违反普通非流式声明时，在提交前返回受控错误并拒绝入缓存，不原样回放旧ID。Flush/Hijack不能成为绕过该检查的路径。
7. 回滚时恢复该路由原通用缓存入口/原派生key，不清空共享Redis；由于旧客户端可能已经依赖新增字段，协议层回滚仍遵守第9节先处理调用方依赖的要求。
8. 以上是待实施契约，不是已证明的实现。先做局部writer/缓存反证，覆盖miss、hit、singleflight多等待者、当前ID、data中同名字段、大整数、HEAD、条件头、尺寸边界、错误与panic后的writer恢复；任何一条无法在当前库下满足则停止3.1并修订方案，不由实施者临时降低约束。

### 4.5 F11/F16：生产日志和 SQL 观测

F11 推荐在现有 `validateLogConfig` 中拒绝 prod+debug，错误标明字段及环境；dev/test 仍允许 debug。沿用当前大小写、首尾空白和其他合法级别语义，不静默把 debug 改成 info，不新增绕过开关。生产临时调试需求若存在，需要维护者另行裁决。回滚二进制仍使用已改为 info 的配置，避免恢复被拒绝的不安全组合。

F16 推荐只新增一个配置：**拟新增 `log.sql_slow_threshold_ms`，类型为可区分“未配置”和 0 的可选整数**。这不是当前已有字段。

| 输入 | 拟定语义 |
|---|---|
| 未配置/null | 维持当前 200ms 阈值，旧配置与手工零值构造均兼容 |
| 0 | 关闭慢 SQL 专属 Warn 分支；不关闭 SQL 错误日志 |
| 正整数 | 毫秒阈值；仍使用当前“耗时严格大于阈值”判定 |
| 负数或 duration 转换溢出 | 配置校验返回 error，启动前失败 |

保留 `log.sql` 的原语义：1 为逐条 Info，0 不逐条输出；慢日志关闭时若逐条 Info 已打开，慢语句仍可能作为普通查询记录。SQL 绑定值继续过滤，错误分类、`LogMode` 副本隔离、懒求值 `fc` 与请求关联均保留。新增可选字段的配置快照复制要独立，不能引入指针别名导致运行时快照可变。类型、校验、默认方法、实际 logger、示例与日志说明同阶段更新。

### 4.6 F06：JWT 生成、加载与 doctor

- 保留生成器私钥 0600、公钥 0644 及 kid 不可覆盖、原子创建/同步目录等语义；不通过把所有公钥 chmod 0600 掩盖诊断不一致。
- Unix 诊断区分活跃私钥、活跃公钥与 previous 公钥：私钥继续拒绝 group/other 权限；公钥允许合法只读共享，拒绝 group/other 可写及不符合既有文件类型规则的路径。诊断按角色明确错误，不把 public readable 报成 private leakage。
- 运行时与 doctor 统一公钥完整性检查和非普通文件约束；如因此拒绝原先可写公钥，作为安全校验收紧记录，先修文件权限再升级。不得放宽生产私钥校验。
- Windows 不以 Unix mode 位假称 ACL 安全；保留平台分流和原存在性检查，ACL 未验保持未验。诊断只读取元数据，生成→doctor→签发/验签闭环使用临时测试钥，不读取或输出用户真实密钥。

### 4.7 F07/F13：命令与构建

F07：`run` 委托现有 `dev`，`lint` 委托现有 `tool`；普通 `make test` 执行现有带 scaffold tag 的 race 目标。当 MAKECMDGOALS 含 `migrate-*` 时，保留 `test` 作为环境位置参数占位，不启动整仓测试；同时在说明中推荐明确的 `ENV=test` 形式。对含糊组合给出清晰用法，不能猜测环境。

`clean` 只删除该项目明确管理的构建产物和默认覆盖率产物，不执行 `go clean -modcache`、Docker 清理、删日志、删数据库或递归删除任意 BIN_DIR。实现前列出可删文件；外部路径、符号链接、根目录及非受管文件均拒绝。复测在临时目录放相邻哨兵文件，证明其未受影响。

F13：保留 `version.go` 中 `const Version` 作为现有 release 脚本的版本来源，不改解析格式或切主版本。建议由 main 将该值传给拟新增的 `cmd.ExecuteWithBuildInfo`，保留 `cmd.Execute()`、`NewRootCmd(...)` 现有入口兼容。新增入口只承载明确的版本/commit/构建时间值；版本查询不读取运行配置或连接 DB，非版本命令保留现有唯一信号监督者语义。根命令支持 `--version`，输出固定字段。

commit/构建时间的拟新增字符串变量留在现有 main 包，通过 Make/Docker 的 link flags 注入；普通 `go build` 缺少注入时显示 unknown，版本仍取现有常量，不伪造当前时间。不得新增第二份手工版本常量，也不手改 appmeta 生成文件。构建时间须支持固定输入，以便相同输入可复现；新增包级变量需同步现有认领登记，不能关掉传感器。镜像构建参数、使用说明及部署二进制路径保持一致。

### 4.8 F05/F08：依赖与门禁

F05 只移除已由 tidy -diff 证明多余的 goerr 1.4.0 两行；保留 go.mod 中 1.5.0 和有效 sum。实施时如果实际 tidy 差异超过该范围，先读差异并停下核对，不能用一次全量 tidy 改掉用户其他选择。

F08 在现有集成包发现范围加入 cmd，并增加可反驳的枚举/强制模式检查。发布路径设置 `REQUIRE_DB_INTEGRATION=1` 及插件显式 opt-in；在强制模式下插件套件不能因 opt-in 缺失而静默 skip。

接线前先调整 `localPluginConfiguration`：其当前强制 `APP_ROOT=projectRoot`。后续应尊重明确的外部测试 APP_ROOT，只读独立测试配置和临时钥，缺失则在发起连接前失败；仍限定本地测试连接，禁止被环境覆盖到外部/生产。源码构建使用的 projectRoot 与运行配置根要分开，不能删除 child build 的源码定位。原型验收通过临时副本隔离，不能直接把那种“必须复制整个项目”的做法当成日常门禁最终设计。

日常 `make check` / GitHub `check-ci` 继续允许无数据库；真实依赖由已有 test-integration/release-check 承担。不能为了补 F08 把生产数据库或容器隐式拉入普通提交检查。SCRIPT FLUSH 继续需要专用实例授权；强制集成不是授权影响共享 Redis。

## 5. 文件范围与依赖

下列为后续预计修改范围，均未修改。以本轮已读取的文件为依据；标为“拟新增”的路径不是现有实现。步骤开始前需重新核对文件及调用方，如果事实变化使范围扩大，先更新本计划。

| 阶段 | 预计代码/测试/配套文件 |
|---|---|
| 1 缓存 | `internal/pkg/cache/cache.go`、`cache_test.go`；按需在同目录拟新增 `panic_test.go`；`docs/cache.md` |
| 1 事务 | `internal/repository/dbtx/dbtx.go`、`dbtx_test.go`、`dbtx_ctx_test.go`、`integration_test.go`；`docs/testing.md` 的事务约定 |
| 2 前缀 | `internal/platform/config/types.go`、`defaults.go`、`validate.go`、`redis_test.go`、`effective_test.go`；`internal/bootstrap/providers.go`、`ratelimit.go` 及其现有测试；`internal/plugin/builtin/cache/plugin.go`、`contract_test.go`；`config/env.yml.example`；认证/限流/缓存现有测试 |
| 3 协议 | `internal/pkg/response/response.go`、`response_test.go` 及相关响应测试；`internal/pkg/httpcache/cache.go`、`cache_test.go`，拟新增同目录 `envelope.go`、`envelope_test.go`；已确认使用统一信封与缓存的路由调用方；必要的现有 middleware/router 协议测试；已确认存在的 `docs/api-conventions.md` |
| 3 日志配置 | `internal/platform/config/types.go`、`defaults.go`、`validate.go` 及现有日志/配置测试；`internal/platform/data/init.go`、`init_test.go`、`gorm_logger_contract_test.go`；`config/env.yml.example`、`docs/observability.md`；既有日志整改计划与历史证据保持 |
| 4 诊断 | `internal/diagnostics/doctor.go`、`keyperm_unix.go`、`keyperm_windows.go`、`doctor_test.go`、`keyperm_unix_test.go`；`internal/pkg/jwtauth/keyset.go` 及其现有测试；`cmd/jwt_perm_unix_test.go`、`doctor_test.go`、`jwt_test.go`；必要的 JWT 使用说明 |
| 4 命令/构建 | `Makefile`、`main.go`、`cmd/root.go`、`root_test.go`；拟新增同目录 `cmd/version_test.go`；`Dockerfile`；`scripts/package-vars-allowlist.txt`；`README.md`、`docs/testing.md`；自检仅在现有 scripts 中按需新增，不建新层级 |
| 5 | `go.sum`；`Makefile`；`cmd/plugins_integration_test.go`；枚举/强制模式自检放现有 scripts；`docs/testing.md`；必要时同步现有发布接线测试，不改冻结生产部署文件 |
| 6 | 本计划的执行记录及获准的测试证据产物；不借验收新增业务功能 |

`version.go`、`project.mk`、`internal/appmeta/meta.go` 当前值保持；不修改现有迁移 SQL 和业务表结构。F03 实际前缀消费只调整已盘点入口；其余 App.Name 的 telemetry service name、CLI/日志身份等用途保持。

## 6. 分阶段实施计划

所有步骤共同前置条件：取得覆盖该阶段的明确实施授权；实际读取最新指令、本计划、全局规则和错误记录；确认工作区基线和用户改动归属。共同停止条件：阶段外代码变化、需要新依赖/架构调整、失败根因不明、同一问题三轮无正向进展、需操作生产或无法确认数据归属。每行的专属停止条件是在此基础上追加。

每阶段结束必须更新实际状态和证据，失败/跳过/未验证不能勾为通过。以下“证据”均为将来应交付的证据。

### 阶段 0：本轮方案交付

| 步骤 | 前置条件 | 工作与核对 | 交付与状态 |
|---|---|---|---|
| 0.1 | 原报告可读 | 逐一保留 F01–F18 编号，核对直接相关实现，区分故障、能力差异与规范冲突 | 本文；已编制并保存 |
| 0.2 | 本文保存后 | 全文回读；核对阶段顺序、文件范围、兼容/风险/回滚/复测和授权状态 | 已逐节回读核对；未运行任何测试 |

### 阶段 1：稳定性与事务边界（F01/F02）

| 步骤 | 专属前置条件 | 具体改动 | 检查方法与证据 | 专属停止条件 | 交付 / 状态 |
|---|---|---|---|---|---|
| 1.1 | 阅读现有 cache 测试及原反证 | 将 loader panic 子进程、同 key 等待者、取消及名额收尾作为稳定回归 | 旧实现必须命中指定失败；保存子进程 exit/堆栈，不把 runner 成功当变体成功 | 子进程与根因不符，或测试无法稳定安排等待者 | 缺陷反证已完成；原失败exit保留，见阶段1 |
| 1.2 | 1.1 证据成立 | 按 4.1 恢复异步 panic，保留清理与失效屏障 | cache 相关全量 race；子进程 exit 0；多等待者、后续加载及上限断言；无秘密回显 | 需无界旁路、吞错误或放松上限才能通过 | 已完成；子进程/完整缓存race通过，见阶段1 |
| 1.3 | 同管理器 savepoint 契约获准；独立 MySQL schema 可用 | 添加 F02 的失败用例和归属/状态机制；保留原 ctx 路由，禁止独立提交 | 真实 MySQL 外层失败 count=0；内层回滚/外层可继续；跨管理器回调不执行；SQL fake 注入 SavePoint/RollbackTo 失败 | 不能可靠识别归属、rollback 失败仍可能提交、须引入分布式事务 | 已完成；真实MySQL及失败矩阵通过，见阶段1 |
| 1.4 | 上述步骤完成 | 回审所有 Do/With/DB 调用方及生命周期 | 受影响仓储/应用测试、连接池 max_open=1 和 deadline；记录未验的并行事务用法 | 需要修改已有业务事务边界或 schema | 已完成；调用方/单连接池及deadline回归通过 |

### 阶段 2：Redis 前缀消费与兼容发布（F03）

| 步骤 | 专属前置条件 | 具体改动 | 检查方法与证据 | 专属停止条件 | 交付 / 状态 |
|---|---|---|---|---|---|
| 2.1 | 旧 App.Name/Prefix/DB/key 家族的清单可核对；生产值不写入文档 | 确定保持原 namespace 的升级配置；新增统一取值和配置校验；同步示例回落规则 | 缺省/空/显式/仅空白及 APP_ 覆盖表驱动；App.Name fallback 的 key 字节完全一致 | 旧 Prefix 与实际不同却没有维护者确认处理方式 | 本地实现及兼容说明已完成；生产namespace清单未确认、不部署 |
| 2.2 | 2.1 完成 | 同时改认证、限流 KeyBuilder/Backend 和缓存三条装配 | 同 App.Name 不同 Prefix 的两个独立实例真实 key 不重叠；空 Prefix 与旧版兼容；旧撤销/刷新拒绝规则保持 | 新旧版混部会读不同 token 状态，或存在未盘点消费者 | 已完成；三条消费链及真实隔离/TTL通过 |
| 2.3 | 独立测试实例与确切 key 所有权可证明 | 验证认证轮换/注销、限流和缓存命中，演练保留 namespace 的二进制回退 | 真实 key/TTL 的去敏归属记录；注销后 access/refresh 均拒绝；回退前后状态不复活 | 必须扫描/搬迁共享 key、清库或重置用户会话才能继续 | 已完成；同namespace旧二进制回退仍拒绝撤销token；自有Redis键已清 |

### 阶段 3：响应契约与日志配置（F04/F11/F16）

| 步骤 | 专属前置条件 | 具体改动 | 检查方法与证据 | 专属停止条件 | 交付 / 状态 |
|---|---|---|---|---|---|
| 3.1 | request_id 增量契约批准；严格客户端及缓存路由/例外已盘点 | 统一信封增字段、写出收口；实现4.4明确的显式信封缓存包装及key域，不改通用缓存 | 成功及 401/403/404/405/413/422/429/500；header/body/log一致；miss/hit/共享等待者各有本次ID；条件头/HEAD/尺寸/原data语义回归 | 必须改探针/SSE协议、返回旧ID、破坏普通缓存或擅升依赖 | 本地实现及契约回归已完成；外部严格客户端未确认，上线前另验 |
| 3.2 | 生产 debug 拒绝策略批准 | 在配置校验处拒绝 prod+debug，同步示例说明 | dev/test/prod×各既有级别；空白/大小写保持；prod 错误发生在资源初始化前 | 需要静默覆盖配置或新增未批准旁路 | 已完成；配置矩阵与初始化前拒绝验证通过 |
| 3.3 | 单字段 SQL 阈值语义批准 | 类型/默认/校验/实例 logger/快照复制/示例同步 | 未配置=200ms；0 关闭专属慢日志；负数/溢出拒绝；错误仍输出、参数仍过滤、LogMode 副本及 fc 懒求值不退化 | 增加字段却未贯通默认、示例、消费或快照复制 | 已完成；SQL阈值/快照/示例与实际非敏感配置同步 |

### 阶段 4：诊断、命令与版本信息（F06/F07/F13）

| 步骤 | 专属前置条件 | 具体改动 | 检查方法与证据 | 专属停止条件 | 交付 / 状态 |
|---|---|---|---|---|---|
| 4.1 | 已区分公钥完整性与私钥保密 | 角色化 doctor 权限检查，与运行时加载规则对齐 | 临时生成→doctor→签发/验签；private0644失败/public0644通过/public0666失败；previous key、缺失/非普通文件、Unix/Windows 分流 | 需读取真实钥、放宽生产私钥或假称 Windows ACL 已验 | Unix闭环已完成；Windows真实ACL未验证 |
| 4.2 | Make 位置环境参数矩阵已列出 | 添加 run/lint/test/clean，并保留 migrate-* test；安全限定清理对象 | 临时 fixture 自检；普通 test 确实发起测试；迁移 test 不发整仓测试；clean 保留相邻哨兵；帮助文本一致 | 迁移可能误连/误执行或 clean 可删非受管文件 | 已完成；真实入口、自检和哨兵保护通过 |
| 4.3 | 版本来源与固定构建输入批准 | 增加显式 build info 入口，保留现有入口；Make/Docker 传 commit/time；同步变量认领 | 无配置/DB/Redis时 --version 退出0；go build与make build及可用的镜像输出一致；缺注入显示 unknown；release 解析格式保持 | 需要改版本主线、改部署路径、第二份版本常量或依赖当前时钟生成源码 | 原生/Make/注入契约已完成；最终应用镜像运行未验证，见X1 |

### 阶段 5：依赖整洁与真实集成门禁（F05/F08）

| 步骤 | 专属前置条件 | 具体改动 | 检查方法与证据 | 专属停止条件 | 交付 / 状态 |
|---|---|---|---|---|---|
| 5.1 | 用户 go.mod/go.sum 选择已保护，实际差异仍只有旧 sum | 整理两行旧 goerr1.4.0 sum，不更改 go.mod 版本 | tidy -diff 无输出；与阶段前快照逐项比较；记录完整 make check 结果 | tidy 涉及额外升级、replace 或删除用户新 sum | 已完成；只去除旧sum两行，用户go.mod保持 |
| 5.2 | 外置独立 APP_ROOT、库号、namespace 和临时钥准备好 | 先移除插件测试对项目配置根的强制覆盖，保留源码构建定位 | 未配置前置时发连接前失败；不会读项目真实 config；无法被 APP_ 重定向到外部 | 仍依赖项目真实钥/配置，或强制模式仍可能 skip | 已完成；隔离根/失败前置及真实套件通过 |
| 5.3 | 5.2 完成 | 枚举加入 cmd、强制 opt-in 接线及门禁自检 | 放入/移除临时 cmd integration fixture 可使判据通过/失败；真实必需套件有 pass 事件而非仅包 exit0；普通 check-ci 无 DB 可运行 | 必须拉镜像/新建容器，或把发布依赖引入日常检查 | 已完成；cmd枚举/强制接线及真实集成通过 |

### 阶段 6：总体验收与交付

| 步骤 | 专属前置条件 | 具体工作 | 检查方法与证据 | 专属停止条件 | 交付 / 状态 |
|---|---|---|---|---|---|
| 6.1 | 已获准核心步骤均完成且局部门禁通过 | 运行整仓质量与双口径验证，确认未降低门禁/掩盖 skip | 见第 8 节命令；源码 diff、工具版本、真实 exit、覆盖率、生成器/架构结果 | 为全绿需弱化规则、移除反证或越阶段修改 | 已完成；完整双口径门禁、生成器、最终lint/race及83.1%证据齐备 |
| 6.2 | 本地隔离依赖、数据与资源预算确认 | 重跑认证/HTTP/故障/迁移相关回归和回退演练，只清理可证明归属数据 | F01–F08/F11/F13/F16 逐项 closure；请求ID缓存命中、撤销状态、真实信号；残留数据如实记录 | 未知/生产连接、不可恢复清理、缺关键状态回退证据 | 本地功能/回退/故障/信号已完成；独占Redis30键已清；专用schema保留供复核、未DROP |
| 6.3 | 6.1/6.2 结果齐备 | 更新本计划及验收结论，分开核心整改、附件整体、发布容量三个口径 | 每个 F 记录修复/保留/冲突/未验证及证据；无严重未闭环；任何未知不写 Ready | X1/X2/规范冲突仍未裁决却拟宣称附件整体通过 | 报告已保存并逐段回读、34链接存在性核对通过；整体/生产不准入；不等于OpenSpec归档 |

## 7. 规范冲突与能力扩展的独立处理

### C1：F12 中间件顺序

推荐保留现有外层 Recovery→RequestID→可选 OTel→日志→Metrics→内层 Recovery→安全头→ErrorHandler→CORS→MaxBytes，再按路由分认证前后限流的实现。附件字面顺序、Guide 推荐及当前设计三者需由维护者统一。裁决前不重排代码，不声称已关闭 F12。

若要求改顺序，先修订本计划的范围及复测矩阵，再单独实施；必须验证 OPTIONS、CORS拒绝、未认证、限流拒绝、panic前后、404/405 均保留正确状态、请求标识、日志及指标。不能只测正常 200。

### C2：F17 标准 log 适配

现有 `http.Server.ErrorLog` 类型与当前适配依赖标准 `*log.Logger`，最终输出到 gtkit。推荐由规则维护者明确“仅 net/http 桥接”的窄例外；这只是待裁决建议，不是本计划授权修改 AGENTS 或认定例外已生效。若仍坚持绝对禁用，则需另行确认兼容方案；不得置空 ErrorLog、丢弃 TLS/解析错误或通过 slog 绕规则。当前服务日志保留，规则冲突保持未决。

### X1：F09 Compose 应用服务（源码已授权，容器运行未验证）

候选 A 保持依赖编排并明确调整附件承诺；候选 B 在现有 compose 文件增加可选择的应用服务。若仍要求附件三服务一键启动，推荐 B，并用应用 profile 保持现有 `dev-up` 只起依赖的习惯；明确新增的一键应用入口。

实施前锁定构建/复用镜像方式、容器内配置 host/端口/APP_ROOT、非 root 挂载权限、秘密注入、ready 探针、启动依赖和退出宽限期。预计影响 `compose.yaml`、`compose.env.example`、`Makefile`、`README.md`；现有 Dockerfile 路径契约保持。复测需真实构建并启动三服务、外部调用API、依赖失败ready503、SIGTERM排空及数据卷保留；只解析 compose config 不算通过。回滚只撤应用服务/入口并保留数据卷，不执行 down -v。镜像/容器缺失须先取得相应操作授权。

### X2：F10 Redis 集群（需专项方案，未进入核心执行）

候选 A 保留单机多 DB 并修订附件范围；候选 B 改为兼容 Cluster 的客户端/能力接口并迁移 keyspace。当前认证、缓存、队列等使用具体客户端和逻辑 DB，Cluster 只能使用 DB 0；不能仅添加多个地址后宣布完成。

若选择 B，须先逐个盘点 plugin/resource/runtime/缓存/限流/令牌/锁/队列消费者，确定多 DB 如何映射到命名空间、Lua key 同槽、拓扑发现、重定向、断线恢复、超时预算、监控和旧状态迁移。不得默认现有 redisx 版本提供所需接口，也不预选未核实新依赖。需独立拓扑与切主/分片故障测试，保持刷新 family 的 hash tag 和 fencing 单调语义。单机回退必须保留全部权威状态；只回退二进制不构成集群数据回滚。

该扩展会改变接口和部署契约，当前缺少拓扑、容量、接口兼容及迁移输入，不能伪装成零决策实施步骤。完成并获准专项方案前停止依赖该扩展的代码修改；这不阻止独立的核心修复。没有明确调整验收范围时，F09/F10 仍属于附件未满足项，不能自动豁免。

## 8. 复测标准与证据要求（实际执行见第12节与结果报告）

### 8.1 可复制的基础命令口径

下列保留可复制命令模板；本轮实际参数、环境和退出码以第12节及结果报告为准，模板本身不是执行证据。输出目录必须在仓库外；集成配置、schema、Redis DB 与 namespace 必须另外准备并确认。

```bash
AUDIT_OUT="$(mktemp -d "${TMPDIR:-/tmp}/gin-api-remediation.XXXXXX")"

# 分阶段按影响包调整；不能只跑新增单条用例
GOWORK=off GOFLAGS=-mod=readonly go test -tags scaffold -race -count=1 -timeout=5m \
  ./internal/pkg/cache ./internal/repository/dbtx

# 最终两种口径，分别保存日志、退出码和覆盖率
GOFLAGS=-mod=readonly make check COVERAGE_PROFILE="$AUDIT_OUT/workspace-coverage.out"
GOWORK=off GOFLAGS=-mod=readonly make check COVERAGE_PROFILE="$AUDIT_OUT/locked-coverage.out"
GOWORK=off GOFLAGS=-mod=readonly make check-generator

# 原生与 Linux 构建，产物不写项目
GOWORK=off GOFLAGS=-mod=readonly go build -o "$AUDIT_OUT/gin_api-native" .
GOWORK=off GOFLAGS=-mod=readonly CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -o "$AUDIT_OUT/gin_api-linux" .

# 仅在阶段5已修好夹具、隔离配置及必需依赖后执行
APP_ROOT="$TEST_ROOT" GOWORK=off GOFLAGS=-mod=readonly \
  REQUIRE_DB_INTEGRATION=1 PLUGIN_INTEGRATION=1 \
  REDIS_ITEST_ADDR=127.0.0.1:6379 REDIS_ITEST_DB="$TEST_REDIS_DB" \
  ALLOW_REDIS_SCRIPT_FLUSH=0 make test-integration
```

不能直接运行完整 `release-check` 并假定无副作用：它还包括容器、迁移验证和供应链步骤；实际执行前须逐项核对当时脚本及授权。缺少工具/镜像/隔离实例仅阻断相关验证，不替换成通过。

### 8.2 必须证明的属性

| 项目 | 通过标准 | 证据形式 |
|---|---|---|
| F01 | panic 不退出进程；同 key 广播/取消正确；不缓存错误；名额释放且后续可用 | 旧实现指定失败、新实现通过；真实子进程 exit 与受影响全量 race |
| F02 | 外层失败残留0；内层失败只回滚其 savepoint；rollback 失败不可提交；跨管理器拒绝 | 真实MySQL独立schema结果、fake失败点、完整ctx/池等待回归 |
| F03 | 缺省与旧 key字节兼容；显式 Prefix真实隔离；旧撤销/刷新状态不复活 | 双实例实际key/DB/TTL去敏记录、认证拒绝断言、回退记录 |
| F04 | 当前 request_id 在头/统一JSON/日志一致；缓存命中不重放旧ID | 正常/错误/缓存hit与miss/极早失败/探针与流式例外矩阵 |
| F05/F08 | tidy无差异；实际枚举cmd；强制必需套件执行，缺前置失败 | 真实门禁exit、命令和测试pass事件；故意漏枚举/漏opt-in的判据反证 |
| F06 | 生成结果可被doctor和runtime共同接受；私钥过宽仍拒绝 | 临时钥命令闭环及平台/角色矩阵；不得用真实私钥截图 |
| F07 | make test真执行；迁移环境兼容；clean不删哨兵 | 隔离Make fixture、实际测试事件及有限清理前后清单 |
| F11/F16 | prod debug启动前拒绝；SQL新配置默认、关闭、越界与错误分支正确 | 配置表驱动、启动失败点、日志快照及脱敏/懒求值回归 |
| F13 | --version无依赖退出0；版本来源唯一；commit/time注入与unknown回落清晰 | 原生/make构建输出、固定输入复现、旧CLI用法兼容 |
| 全局 | 不降低现有72.5%门禁；正常完整路径以原83.0%为比较基线，下降须解释并经维护者接受，不靠skip补绿 | 独立聚合coverage、skip原因、构建标签、工具版本及源码差异 |

不把 2405 个历史 pass 事件固定成未来独立场景数量。新增/调整测试后应重新计数父子测试与包事件；不能通过删除旧反证保持数字。没有真实数据库或依赖状态迁移证据时，相关数据/权限项不准入。

### 8.3 外部依赖、容量与故障边界

| 依赖 | 超时/重试与一致性约束 | 故障行为和观测 |
|---|---|---|
| 进程内加载 | 保留默认30s加载超时、64在途上限；不新增自动重试 | panic/error不缓存，等待者可取消；保留受控堆栈和请求关联 |
| MySQL | 保留现有连接参数及请求ctx；事务写入不自动重试；同物理事务原子性 | pool wait有界，失败rollback；记录耗时、池等待、rollback错误；真实DB版本沿验收基线8.0.22复测 |
| Redis | 保留现有redisx/go-redis配置的池/超时/重试；未配置项的实际默认数值在运行配置快照中核对，不能凭记忆填值 | auth强依赖失败拒绝；ready503/live200；限流各失败模式不变；恢复后重新探测 |
| HTTP响应缓存 | 保留Redis读1s/写2s、已有TTL及准入；不把缓存故障变成权威数据成功 | 记录hit/miss、fallback和当前request_id；不承诺强一致业务缓存 |
| OTLP/邮件等可选外部服务 | 不改现有预算/重试，不在此范围发真实消息 | 仅对受影响观测做替身/沙箱；无采集端时明确未验 |

部署规模、QPS、数据量、实例数、DB总连接预算及业务p95/p99/SLO目前未确认，负责人及日历排期也未指定。后续容量验收需先填真实输入：总DB连接预算=实例数×单实例池上限；在途约=请求速率×平均耗时；不得拿health冒烟的499.69req/s估算业务能力。未配置Redis客户端的默认超时/重试未实取前，其专项压力结论仍未验证。

## 9. 风险、失败处理与回滚

### 9.1 风险登记

| 风险 / 触发条件 | 影响 | 缓解与停止标准 | 决策责任 |
|---|---|---|---|
| 缓存恢复后登记清理时序变化 | 同key挂起、饱和或旧值复活 | 多等待者/取消/失效交错反证；不弱化上限；失败即停 | 阶段1实施者与维护者 |
| 嵌套回滚失败或跨管理器复用 | 数据半提交、跨库误写 | 明确归属、不可提交状态、注入rollback失败；无法证明则不交付 | 阶段1实施者与维护者 |
| 以前被忽略的 Prefix 被激活 | 撤销丢失、刷新状态不可见、限流重置 | 默认保持旧namespace；不自动双写/搬迁；不一致即停止升级 | 部署/认证状态负责人，尚未指定 |
| 增加JSON字段及缓存中的旧ID | 严格客户端不兼容或关联错乱 | 契约审阅、调用方清单、缓存命中复测；路径不可控则停 | API维护者，尚未指定 |
| prod debug校验收紧 | 旧错误配置无法启动 | 提前改info并预检；不静默降级，失败不启动资源 | 配置/部署维护者 |
| 新SQL字段可选值与快照别名 | 缺省行为变化或并发配置污染 | nil/0/越界测试及深拷贝回归；示例与消费者同步 | 阶段3实施者 |
| Make目标/集成接线变更 | 误执行迁移、读取真实配置、假全绿 | 分离位置参数，外置APP_ROOT，明确强制事件 | 阶段4/5实施者 |
| 既有用户依赖改动被覆盖 | 丢失用户选择或引入额外依赖变化 | 保存原始diff/hash；超出两行旧sum即停 | 阶段5实施者 |
| 历史证据临时路径失效、真实环境缺失 | 无法复核或验收范围被夸大 | 本文保留关键事实；阶段前核对来源；缺证据标未验并重建隔离模型 | 各阶段验收者 |

负责人是角色描述，尚无具体人名或承诺日期；不得把本表当作这些角色已接受上线风险。任何风险接受必须有对应维护者、范围、截止时间和补测安排。

### 9.2 通用失败与回滚步骤

1. 实施前保存该阶段涉及文件的原始 diff/副本及用户改动边界；记录当前配置的非秘密语义。每阶段只保存自己的改动清单，不以全仓reset作为回滚。
2. 失败先保留原命令、真实退出码和根因，再修复授权范围内问题；同一问题三轮无进展停止，报告卡点。不通过削弱断言、删失败用例、下调门禁或隐藏skip补绿。
3. 撤回代码时仅撤该阶段生成的差异，保留用户原改动和其他阶段已验收成果；未提交状态不使用 git reset --hard / clean，已提交状态是否 revert 仍取决于当时明确授权。
4. 核心方案无业务schema迁移，无需DROP/TRUNCATE或删除迁移记录。F02若已被新业务依赖，回退到不支持嵌套的旧版本前必须停用该新调用；不能把危险旧语义当作安全回滚。
5. F03仅在保持同namespace前提下直接回退二进制。只要发生状态改名/搬迁，就按单独批准的数据方案回退；不能切回旧配置并假定期间的注销/刷新记录仍完整。回滚后必须复测已注销token仍被拒绝。
6. F04新增字段被客户端依赖后，删字段会反向破坏兼容；先回退依赖该字段的客户端或保留协议修复，再回退其他代码。缓存中旧信封的升级/回退均要验证，不清空共享Redis规避问题。
7. F06保留合法私钥/公钥权限，不在回滚中 chmod 放宽秘密；F11保留已调整为非debug的生产配置。F16新增字段在回退到拒绝未知字段的旧版本前，从获准配置中同步移除；否则旧版本可能启动失败。
8. F05回滚只恢复本阶段删除的旧sum，不回退go.mod中用户的1.5.0选择。F07/F08回退不得导致必需测试再次被认为已通过；回退会重新引入缺口时结论回到未闭环。
9. 只清理确切归属本轮的文件/schema/key；保留原始证据及必要备份，不删共享前缀、不FLUSH。每次记录清理成功、失败和未确认项。

### 9.3 发布/灰度边界

本任务没有生产部署授权，也没有真实部署规模与流量输入，不写虚构的灰度百分比或观察时长。后续拟发布时先在隔离环境部署候选、执行第8节并演练回退；再由部署维护者明确单实例/灰度批次、观察窗口、错误率/延迟/池等待/认证拒绝的阈值和停止条件，填入执行记录。

任一新进程退出、外层回滚残留、已撤销token被接受、错误namespace写入、缓存返回旧request_id或doctor误判，均立即停止该批；在状态安全可证明的前提下回退。没有这些生产输入时，本计划可进入获准的本地实施，不能据此执行线上切换。

## 10. 审阅事项、里程碑与执行记录

### 10.1 阶段前须明确的决策

| 决策 | 本计划推荐 | 未决定时的边界 |
|---|---|---|
| F02嵌套语义 | 同归属savepoint；未知/跨管理器拒绝；回滚失败阻止外层提交 | 阶段1.3不启动，不由实施者临时改成独立事务或静默join |
| F03已有实例namespace | 兼容发布保留旧App.Name的实际namespace | 可以设计/实现取值与测试；具体实例升级在清单确认前停止 |
| F04协议 | 增加request_id；采用4.4显式信封缓存模式，接受该模式条件复用的边界，通用缓存保持 | 阶段3.1在契约及调用方/例外清单批准前停止 |
| F11/F16配置 | prod拒绝debug；单字段毫秒阈值，缺省200/0关闭 | 未批准不新增配置或改变启动规则 |
| F13版本 | 复用现有Version常量，main显式传值，commit/time可注入 | 未批准不扩CLI或构建契约 |
| F09/F10 | 核心修复之外独立扩展；若保留附件原要求则仍需实现 | 不擅自调整附件标准，也不开展接口/拓扑扩围 |
| F12/F17 | 保留当前实现，先统一规则；ErrorLog窄例外待维护者裁决 | 不修改规则、中间件顺序或服务器错误输出 |

批准本计划的相关契约即可作为其后获准阶段的实施依据，不要求用户重复回答已明确的选择。仍未裁决的扩展/规范项保留未决，不冒称“零决策计划已批准”。

### 10.2 里程碑与当前状态

| 里程碑 | 交付物 | 验收/进入条件 | 当前状态 |
|---|---|---|---|
| M0 文档 | 本整改方案及逐步计划 | 保存并全文回读，保留原报告编号与范围 | 已保存/回读，用户已按第11节授权实施 |
| M1 稳定性 | F01/F02代码、反证和事务失败矩阵 | 阶段1获准且关键反证全部闭环 | 已完成，见阶段1实际记录 |
| M2 状态兼容 | F03取值/消费/兼容发布证据 | M1完成，namespace契约及测试隔离已确认 | 已完成本地兼容，生产切换未执行 |
| M3 协议与配置 | F04/F11/F16及同步示例 | M2完成，协议与配置推荐被批准 | 已完成本地契约及配置回归 |
| M4 CLI | F06/F07/F13及命令闭环 | M3完成，CLI/构建契约批准 | 已完成Unix本地及构建元数据验证 |
| M5 门禁 | F05/F08、强制集成证据 | M4完成，外置测试配置及依赖已确认 | 已完成；严格错误检查和真实集成已接入 |
| M6 核心复测 | 逐项关闭记录、剩余问题与回滚证据 | 获准核心步骤完成；未知/扩展/冲突单列 | 本地已完成并交付报告；X1运行/规范差异/生产验证仍单列 |
| MX 扩展/规范 | X1/X2专项计划及C1/C2裁决 | 单独批准范围与条件 | X1源码/解析完成、运行未验证；X2用户延期；C1/C2保留实现与规范差异 |

### 10.3 后续执行记录模板

每完成一个阶段追加一条，不能覆盖历史失败：

| 字段 | 应记录内容 |
|---|---|
| 阶段/步骤与授权 | 对应编号、用户授权范围及读取的计划版本 |
| 开始基线 | commit、用户已有diff、涉及文件快照、工具/依赖版本与workspace口径 |
| 实际改动 | 精确文件、接口/配置/状态变化；与计划差异及获准说明 |
| 实测证据 | 原命令、真实exit、日志路径、pass/fail/skip、覆盖率和去敏行为证据 |
| 失败/风险/回滚 | 根因、尝试次数、数据归属、回退动作及验证，未执行要明确 |
| 阶段结论 | 通过/失败/未验证；剩余F编号；可否进入已获准的下一阶段 |

**历史文档轮结论：方案曾保存并回读，原验收不通过。当前实施授权及实时进度以第11节和后续执行记录为准；未取得证据的修复不标完成。**


## 11. 2026-10-22 实施授权修订与错误处理补充（当前有效）

### 11.1 授权和范围

需求原文：“DB 和 Redis 暂时不考虑集群，所有的 error 都不要忽略，一定要做处理好，现在 goerr 包已经更新，优化一下错误处理方式；其他的按你的方案来做”。据此：

- 已授权按原阶段1→2→3→4→5→6执行，不重排阶段；阶段0补做本次约束更新/回读。X1应用Compose的源码、示例与说明一并完成，在阶段5之后、阶段6之前验证；启动/创建缺失容器或拉镜像仍遵守本机容器规则，不以本授权隐式执行。
- DB保持现有单库连接和事务架构；Redis保持单机多DB。F10与任何DB集群/读写分离/切主建设明确延期，不以这些范围外能力阻断本次整改结论，也不把延期写为已实现。
- 方案推荐的F02同归属savepoint、F04增量信封/显式缓存模式、F11生产debug拒绝、F13版本单一来源、F16可选SQL阈值已作为本次实施依据。保持旧namespace进行兼容验证；不连接生产，也不迁移既有令牌状态。
- F12保留当前有实测依据的双Recovery顺序，更新相关说明使其一致。F17保留net/http所需的标准log类型桥接及gtkit终端输出，作为既有适配的规范问题单列，不通过删日志或换slog处理；不擅改全局规则。F15与F18的无关批量风格/忽略规则仍不扩入本期。
- 已核实实际锁定goerr为v1.5.0（go list -m JSON指向本机模块缓存），新能力是Inspect/Diagnostic/Cause。保留当前go.mod版本和用户已有改动，不另行升级。

### 11.2 所有error的处理契约

此项覆盖自有代码中真实返回error的调用：普通返回、defer、资源释放、补偿/回滚、异步任务、写出和测试夹具。`_ = c.Error(err)`这类返回*gin.Error的登记结果以及类型断言bool需按真实语义区分，不把正则命中等同未处理error。

1. 同步链路优先返回原错误或在增加动作信息时wrap；清理与主失败并存使用errors.Join。不得用cleanup错误覆盖主错误，也不得让cleanup失败被成功返回掩盖。expected EOF、已关闭、bcrypt刻意不匹配等必须明确识别并走有含义的分支，不能笼统忽略。
2. 回调签名无error出口时，在拥有该工作的终结边界记录受控错误/指标；能够由生命周期返回的必须汇入关闭/排空结果。日志写出自身失败采用有界fallback并保留可报告状态，避免递归调用同一失败日志器；不以空if、nolint或丢弃赋值满足扫描器。
3. goerr.New/Wrap/WrapStatus/WithStack沿现有状态契约使用；纯透传不重复包装。响应状态继续由既有映射决定。根错误与Join的并列原因保留到终结边界，不能因AsItem只取内层而丢失外层动作/清理失败。
4. 在统一HTTP错误日志终结处接入goerr.Inspect：选取主状态、原因树层级、每分支已有栈的函数/文件/行信息，保留一次日志。不把Diagnostic整体当API协议，不输出原始快照到客户端。Error全文仍按现有隐私约束处理，不额外暴露token/密钥/请求体。
5. Inspect本身返回error时必须显式处理：不覆盖原业务错误、不改HTTP状态；记录诊断失败类型/受控说明并保持原主失败可观测。只读失败不伪造一份完整原因树。该库不隔离任意外部Error/Unwrap方法的阻塞/panic，不能承诺超出其已核实契约的隔离能力。
6. 栈只在有必要的边界捕获；普通fmt.Errorf的原因链本身不等于已有起源栈。已有goerr.Item栈复用，不每层重抓。取消/超时纯错误与Join中夹带清理失败分别分类，不能将后者当纯取消静默丢掉。
7. 加强现有errcheck的blank检查，移除测试中允许忽略error的整类豁免；保留非error返回值与已证明无失败语义的类型不变量区别。使用当前锁定工具，不安装新工具或关闭相关lint。扫描发现的新文件先登记本节范围及对应阶段，再修改，不能凭习惯批量重构。

### 11.3 文件和阶段补充

除第5节范围，错误处理定向范围为本轮静态检索实际命中的：

- 阶段1：cache与dbtx回滚/收尾；`internal/pkg/singleton/probe.go`、`runner.go`；`internal/pkg/tasks/scheduler.go`；`internal/runtime/module/ai/module.go`、`adapters.go`；`internal/bootstrap/app.go`。只处理真实清理/释放/取消错误，不改业务调度/队列语义。
  事务故障矩阵拟新增于现有目录的 `internal/repository/dbtx/transaction_test.go`；复用已锁定sqlmock，不引入新依赖。
- 阶段3：`internal/pkg/response/response.go`及测试；`internal/middleware/auth.go`、`audit.go`；`internal/router/health.go`；`internal/diagnostics/doctor.go`；`internal/pkg/log/logger.go`及现有输出/排空测试。原因树诊断在HTTP终结处，其他边界沿原日志API记录，不另造全局错误框架。
- 阶段4：`cmd/jwt.go`、`jwt_reencode.go`、`project.go`、`modulepath.go`、`syncdir_unix.go`；`internal/scaffold/module_commit.go`、`module_templates.go`；`internal/projectinit/init.go`、`snapshot.go`；生成/回滚/解锁失败与主失败合并；改变签名时回审全部调用方与生成模板。
- 阶段5：`.golangci.yml`，`internal/testutil/redisintegration.go`、`platform.go`，`pkg/ratelimit/redis.go`，`internal/plugin/builtin/mysql/mysqlversion.go`，`internal/pkg/credential/password.go`；新增严格检查引出的相关测试夹具逐个处理并登记。类型断言、bool和无error方法不伪装成error。
- 阶段5严格检查实际清单（stage5-error-inventory：209条errcheck告警，含类型断言/预期结果，逐项分类，不能把计数等同真实缺陷数）：
  - `cmd/plugins_integration_test.go`
  - `internal/bootstrap/assembly_contract_test.go`
  - `internal/bootstrap/listener_test.go`
  - `internal/bootstrap/pprof_test.go`
  - `internal/bootstrap/serving_tls_test.go`
  - `internal/bootstrap/singleton_observer_test.go`
  - `internal/bootstrap/workermanager_test.go`
  - `internal/middleware/error_test.go`
  - `internal/middleware/logger_test.go`
  - `internal/middleware/maxbody.go`
  - `internal/middleware/metrics_test.go`
  - `internal/middleware/requestid_test.go`
  - `internal/middleware/routetimeout_test.go`
  - `internal/module/auth/application/service_test.go`
  - `internal/module/auth/transport/http/bind_isolation_test.go`
  - `internal/pkg/auditlog/auditlog_test.go`
  - `internal/pkg/breaker/breaker_test.go`
  - `internal/pkg/cache/cache_test.go`
  - `internal/pkg/captcha/captcha_test.go`
  - `internal/pkg/console/console.go`
  - `internal/pkg/credential/password.go`
  - `internal/pkg/httpcache/cache_test.go`
  - `internal/pkg/httpclient/downstreams_test.go`
  - `internal/pkg/httpclient/factory_test.go`
  - `internal/pkg/httpclient/limits_test.go`
  - `internal/pkg/jwtauth/rotating_test.go`
  - `internal/pkg/jwtauth/standard_keys_test.go`
  - `internal/pkg/log/logger.go`
  - `internal/pkg/metrics/metrics_test.go`
  - `internal/pkg/news/news_test.go`
  - `internal/pkg/response/response.go`
  - `internal/pkg/response/response_test.go`
  - `internal/pkg/response/response_validation_test.go`
  - `internal/pkg/singleton/lease.go`
  - `internal/pkg/singleton/lease_test.go`
  - `internal/pkg/singleton/probe_test.go`
  - `internal/pkg/singleton/runner_panic_test.go`
  - `internal/pkg/singleton/runner_test.go`
  - `internal/pkg/tasks/scheduler_test.go`
  - `internal/pkg/tokenstore/logic_test.go`
  - `internal/pkg/tokenstore/refresh.go`
  - `internal/pkg/tokenstore/refresh_test.go`
  - `internal/pkg/tokenstore/revocation_test.go`
  - `internal/pkg/upload/uploader_test.go`
  - `internal/pkg/verifycode/verifycode_test.go`
  - `internal/platform/data/availability_test.go`
  - `internal/platform/data/gorm_logger_contract_test.go`
  - `internal/platform/data/tracing_test.go`
  - `internal/platform/telemetry/telemetry_test.go`
  - `internal/plugin/builtin/cache/contract_test.go`
  - `internal/plugin/builtin/httpmiddleware/plugin_test.go`
  - `internal/plugin/builtin/logging/benchmark_test.go`
  - `internal/plugin/builtin/logging/hotpath_contract_test.go`
  - `internal/plugin/builtin/logging/options_test.go`
  - `internal/plugin/builtin/logging/plugin.go`
  - `internal/plugin/builtin/logging/plugin_test.go`
  - `internal/plugin/builtin/mysql/contract_test.go`
  - `internal/plugin/builtin/mysql/mysqlversion.go`
  - `internal/plugin/builtin/mysql/open_test.go`
  - `internal/plugin/builtin/mysql/plugin_test.go`
  - `internal/plugin/builtin/redis/contract_test.go`
  - `internal/projectinit/init_test.go`
  - `internal/projectinit/validate.go`
  - `internal/router/panic_observability_test.go`
  - `internal/router/ratelimit_stager_test.go`
  - `internal/scaffold/generation_atomic_test.go`
  - `internal/testutil/loopback.go`
  - `internal/testutil/platform.go`
  - `internal/testutil/redisintegration.go`
  - `pkg/ratelimit/concurrency_test.go`
  - `pkg/ratelimit/health_notify_test.go`
  - `pkg/ratelimit/integration_test.go`
  - `pkg/ratelimit/keyprefix_test.go`
  - `pkg/ratelimit/loopback_test.go`
  - `pkg/ratelimit/ratelimit_test.go`
  - `pkg/ratelimit/redis.go`
  - `pkg/ratelimit/script_lua_test.go`
- 阶段5额外配套：`internal/pkg/console/console_test.go`、`internal/testutil/loopback_test.go`、`internal/pkg/credential/password_test.go`、`internal/plugin/builtin/mysql/mysqlversion_test.go`、`pkg/ratelimit/observer.go`、`redis_test.go`、`probe_cleanup_test.go`、`README.md`和`internal/bootstrap/ratelimit.go`；复用`cmd/plugins_integration_test.go`及`scripts/check-make-entries.sh`验证隔离配置与集成接线。所有文件先读后改；不加全局豁免。
- X1：`compose.yaml`、`compose.env.example`、`Makefile`、`README.md`及必要的现有配置使用说明。保持现有dev-up依赖服务行为，应用采用显式profile/入口；非root、APP_ROOT、依赖host、挂载与ready规则同步。

外部耦合约束：保留Go1.27、module路径、gtkit日志栈、错误码/HTTP映射、JWT算法、DB schema、Redis库号/key后缀/TTL与显式前缀回落；不得因“所有error处理”自动重试非幂等操作或改变事务提交含义。

### 11.4 追加步骤与复测/回滚

| 步骤 | 前置 | 改动/交付 | 验证与证据 | 停止条件 | 当前状态 |
|---|---|---|---|---|---|
| 0.3 | 用户新指令、goerr源码及错误检索已读 | 更新本节并回读；建立隔离证据与源码hash基线 | 计划文本、模块版本JSON、baseline.json | 事实/原计划不一致且未补齐 | 已完成并回读；真实版本及baseline证据已保存 |
| 1.E | 阶段1各入口已读 | 回滚、释放、异步收尾错误全部返回/合并或终端记录 | 主错+cleanup错均errors.Is可见；panic/ctx分支；真实副作用可回退 | 只能忽略或重试非幂等操作才能通过 | 已完成；主错/清理错及生命周期回归通过 |
| 3.E | v1.5 Inspect契约已读 | HTTP终结日志保留根链/Join分支已有栈；显式处理Inspect失败 | 嵌套/Join/typed-nil/诊断限额失败；响应无诊断信息；一次日志；原HTTP/码不变 | 诊断失败改变业务主错误，或快照/秘密进入客户端 | 已完成；goerr Inspect原因树/失败/脱敏/状态回归通过 |
| 4.E | CLI/生成器补偿链已读 | 文件Close/Remove/Unlock、输出与补偿错误不丢；调用方适配 | 失败注入，补偿失败可见，原内容/键存在性兼容，生成器临时副本验证 | 需改既有数据或无授权覆盖文件 | 已完成；I/O、输出、补偿与恢复副本回归通过 |
| 5.E | 上述阶段通过，lint真实结果可读 | 收束全仓未处理error；严格blank及测试夹具；不扩大为无关重构 | errcheck/lint零未处置，源码回审，不用排除项/空分支压告警 | 同一问题三轮失败或新语义未计划 | 已完成；原209条及补充62条诊断逐项处置，无新增错误豁免 |
| 5.X1 | Compose契约及本地镜像/容器状态核对 | 应用profile、入口与示例；保留默认只起依赖 | compose解析/配置矩阵；获准条件下真实构建/启动/信号；未跑项单列 | 需隐式拉取/新建未授权容器或触达生产 | 源码/解析已完成；真实容器创建未获许可、运行未验证 |

新增error处理的回滚只撤本轮差异，保留用户goerr1.5.0选择；不把“回退后错误重新丢失”当整改已闭环。临时文件删除失败保留确切路径/归属，不能改为删共享目录。Inspect接入可局部回退日志诊断，但不能回退对客脱敏、原因链和清理失败处理。配置/接口变动继续遵循第9节的双向兼容约束。

本轮OpenSpec change为`fix-scaffold-acceptance-errors`，独立于已有其他active changes；只在本change apply-ready及本计划回读后修改应用代码。代码步骤完成后逐阶段记录实际结果，最终严格验证和归档只针对本change。

本轮证据目录：`/var/folders/_w/xwv4k3t12vn9054592gsk5200000gn/T/gin_api_remediation_20261022_51ru7lj7`。本轮执行结果见下方记录，不能把之前审计的通过数字填作本轮结果。

## 12. 本轮实际执行记录

### 阶段1：已完成（2026-10-22）

- 基线：本轮baseline.json保留原commit及逐文件SHA，用户go.mod/go.sum未动；无目录或依赖调整。
- F01：缓存loader回调内recover，受控错误保留原原因；子进程、8等待者、缓存不污染、在途名额回归。原反证cache-panic-before exit1（子进程exit2），修后cache-after/stage1-core-race exit0，stage1-core-lint-final exit0。
- F02：显式物理事务和同归属savepoint；未知/跨归属、并行/已关闭scope拒绝；主错与rollback/commit失败保留，savepoint失败禁止外层提交。dbtx-before exit1验证原缺陷，dbtx-contracts/stage1-core-race exit0。
- 真实MySQL8.0.22：复用devilbox-mysql-1，本轮新建唯一schema gin_api_remediation_20261022_51ru7lj7；integration临时表随自己的连接关闭。stage1-mysql exit0，0 skip，max_open=1外层失败及panic count=0；处理内层失败后count=2/sum=4；成功count=2/sum=3；池被占用时50ms deadline阻止回调。schema暂保留供后续迁移/HTTP验收，最终再定向清理。
- 1.E：singleton Probe改为清理最终持有token并合并清理失败；Runner默认记录错误、释放失败上报且panic等待续租协程退出；scheduler probe/注册失败关闭错误、AI构造/流关闭错误、HTTP预绑定释放错误均保留。stage1-async-race（singleton/tasks/AI/bootstrap/全部repository/application）及stage1-async-lint exit0。
- 调用方回审：现有5个repository统一经dbtx.DB；当前应用没有新增嵌套业务调用点，TxManager接口及With/From/DB保持；现有事务调用方回归通过。SQL mock仅证明注入分支，真实MySQL证据单列。
- 已记录错误：ERR-20261022-045/046原实现反证；047新类型命名及测试ctx lint已纠正；048只读路径失误已按真实清单纠正。没有把预期失败写为通过。
- 风险/回滚：未强杀无视ctx的loader，不支持并行savepoint调度；无schema业务变更，无生产操作。回滚仅撤本轮stage1文件差异（baseline.json可核对），回退代码尚未对生产执行；用户goerr1.5选择保留。
- 完整门禁仍待阶段5/6，不能据本阶段局部结果宣称全仓通过。上述日志均位于第11节本轮证据目录，命令/真实child exit见commands.jsonl。

阶段2定向测试文件补充：在既有cache插件目录新增 `prefix_integration_test.go`，复用现有RealRedisOrSkip与UniqueKeyPrefix；证明同App.Name不同显式prefix的真实缓存key隔离、TTL及借用连接不被关闭。只用本轮/测试自身可证明归属的key清理。HTTP命名空间/兼容回退脚本保存在本轮仓外证据目录。

### 阶段2：已完成（2026-10-22）

- `Config.EffectiveRedisPrefix`集中取值，非空Prefix原样优先，缺省/空串原样回落App.Name；仅空白拒绝。认证、限流KeyBuilder/backend、cache Factory同时消费；Telemetry服务名保持App.Name。
- 示例/README写明旧版忽略非空prefix的升级风险及保留namespace方案。无key搬迁、生产切换或token格式/TTL变更。
- stage2-prefix-race、stage2-lint exit0；含APP_REDIS__PREFIX、raw字节回落及显式cache Factory消费回归。
- stage2-cache-real exit0：真实Redis DB15，两个相同App.Name/不同独占prefix各生成1个缓存key，TTL3s，重复GET各只执行1次且内容隔离；自身key清理随测试执行。
- stage2-prefix-http exit0：同App.Name、显式A/B前缀各有独立刷新/撤销/限流key且TTL>0；注册、登录、轮换、注销成功，注销后access/refresh均401。空Prefix的新二进制状态由阶段1旧二进制回退读取，撤销和刷新拒绝保持，4次SIGTERM退出0。详见prefix-http-results.json（去敏，不保存令牌）。
- 回退演练仅对本轮隔离schema及namespace进行，未改任何存量实例；本轮namespace仍保留供最终验收，最终仅按精确归属清理。显式改名不保证旧二进制兼容，文档要求存量保留旧namespace。

阶段3源码核对补充：当前config插件Value由既有测试明确要求返回loader的同一配置指针，仓库未定义Config.Clone；本轮保留该只读发布契约。SQL阈值在NewGormLogger构造时解析为time.Duration值，LogMode按值复制，不持有配置指针；以构造后变更输入指针不影响logger及LogMode副本的测试验证快照独立性，不为新增字段改配置发布身份。统一信封写出新增WriteJSON供Recovery共用；HTTP终结原因树采用ErrorLogFields投影，不输出Diagnostic整体。

阶段3实际配置同步：已最小核对 `config/env/env.yml` 存在，尚无sql_slow_threshold_ms；按配置同步规则只在该现有文件补日志/前缀说明及null阈值默认，不回显或改动凭据、地址、现有prefix/日志级别。实现前检查文件为普通非symlink；回读仅输出本次新增的非敏感行。CORS预检原本无JSONbody，保持既有协议，只核对响应头ID；普通JSON 403按统一helper契约验证，不把预检空body报告为改造失败。

### 阶段3：已完成（2026-10-22）

- F04：统一信封增request_id，WriteJSON为Recovery/普通helper共同收口；来自服务端日志context，无ID的极早边界为空。上下文无关构造仍保持签名、业务码/HTTP/data不变。
- 显式UnifiedJSONCache使用独立key域和请求独有writer，重写顶层ID并去除固定验证头/摘要；不改共享ResponseCache、不改通用缓存。8并发等待者真实命中singleflight共享、每人ID独立；大整数/嵌套ID、HEAD/204/304、超限不缓存、签名/压缩/流式误用受控拒绝、回放写出错误可观测均已验证。当前内置路由没有匿名缓存挂点，交付可用适配及注册说明。
- F11/F16：prod+debug拒绝，dev/test保持；sql_slow_threshold_ms省略/null=200ms，0仅关专属慢WARN，正整数ms生效，负数/溢出/float/bool拒绝。logger按值固定阈值、LogMode独立，SQL错误/普通开关/懒求值/参数过滤保持；示例、实际配置非敏感字段和说明已同步，实际配置同步hash见runtime-config-sync.json。
- 3.E：goerr.Convert只选状态，终端从原始root Inspect后投影原因树、已有栈及完整外层/Join原因；不对客输出诊断。超限及unknown typed-nil的Inspect错误显式处理，已知nil Item安全，原业务401/404/码/公开文案保持。相同错误只终结记录一次。
- 无error出口：health/doctor恒nil的errgroup改WaitGroup，依赖失败仍独立汇总；doctor文件close/remove错误进入失败结论；日志stderr失败保留有界状态由Drain返回；Audit.enqueue真实返回bool且已有Recorder丢弃计数/节流告警，未把它当error重复告警。
- stage3-contract-fixed exit0（全部相关包race）；stage3-final-lint exit0（在随后定向消除重复准入告警前），最终母版门禁仍在阶段6。stage3-final-race首次故障注入出现额外1条no-store告警，ERR-051记录并修正为只记录一次实际写出错误，独立路由准入告警仍保留。
- stage3-http exit0：29个真实HTTP用例含200/401/403预检/404/405/413/422/429，所有普通JSON响应header/body/log ID相同；预检204/403和探针维持原协议；SIGTERM退出0。另有200次8并发health smoke全部200，仅为回环冒烟，不是容量准入。
- F12仅同步双Recovery说明，未重排实现；F17保留net/http适配，规范冲突仍单列。回滚只撤本轮对应代码/示例及实际配置新增非敏感行；客户端须允许增量request_id，显式信封缓存冷一次，未部署生产或更改存量namespace。

阶段4.E补充（实施前核对）：输出链实际另命中cmd/server.go、doctor.go、module.go、root_scaffold_test.go和server_test.go，加入本阶段定向范围。可失败stdout/stderr写出返回error；banner在资源装配前写，失败不建DB/Redis。Cobra1.10.2的--version模板本身返回写出error，沿用该实现，不另造解析器。

projectinit当前仅将原文件字节保存在内存，且无条件清理快照目录；回滚失败可能删除仍保存旧.git的恢复副本。为满足既有“原字节/存在性/回滚失败可恢复”标准，本阶段在现有snapshot.go把受影响文件的类型、原始字节、mode和symlink目标写为快照目录内0600的recovery.json（目录0700），在任何目标文件/.git变更前完成；失败即零业务写入。回滚或删除新Git失败时保留快照并在错误中写明确切目录，errors.Join/多%w保留主错与补偿错；成功恢复后才清理。该格式仅为内部恢复证据，不是业务数据/HTTP协议或新的目录架构，敏感内容不进日志。快照清理失败返回error，不再以成功+告警结束。相关私有函数签名和测试同步，不变更实例化流程顺序、删除白名单或Git授权护栏。

阶段4.2拟新增 scripts/clean.go 和 scripts/check-make-entries.sh，放既有scripts目录；clean只管理默认bin/<APP_NAME>和.cache/coverage.out，先验证全部路径在根内、普通文件且无symlink，拒绝非默认/越界输出目录，不递归删除目录；自定义输出由调用方管理。新脚本及fixture需实际验证迁移环境占位、真实test/lint/run接线和相邻哨兵。所有文件补偿仅处理本次生成的临时文件，部分写入/close/remove/unlock主错并存都要保留。

阶段4密钥写入补充：锁定gojwt1.1.0的GenerateEd25519Keys在自身失败分支忽略Close/Remove，调用方拿不到这些错误。cmd/jwt.go因此在现有生成函数内使用标准库生成同样Ed25519、PKCS#8/PKIX PEM，以O_EXCL先创建两文件再写/Sync/Close；仅清理本次成功创建并拥有的文件，保留所有写出/关闭/删除/目录fsync失败。保持既有0600/0644、kid/路径、不覆盖与输出契约；不修改gojwt版本或模块缓存。原SDK源码jwt.go已核实，不改变算法或密钥格式。

阶段4恢复副本反证：新增临时Git fixture的panic用例在当前无保护defer上真实exit1（stage4-panic-before：copies=[]）。补充要求：snapshot获取后的回调panic/Goexit等非正常返回不得清理旧Git/文件恢复副本；保留原panic身份，不伪报自动回滚成功。正常返回时只有已成功恢复或成功完成才清理，回滚失败明确保留。clean已用本机Go1.27的os.Root验证可用，删除操作同时受根句柄约束，避免验证后符号链接换向造成越界。

阶段4路径补充：projectinit的findProjectRoot原样返回相对输入，newSnapshot据相对root取父目录会使“根外快照”契约无法按物理绝对路径核对。本阶段在既有函数内统一Abs/EvalSymlinks并显式处理非NotExist错误，保持最近项目根选择；新增相对路径到独立fixture的回归。这是源码确认的工具边界，不声称曾造成真实用户数据丢失。

### 阶段4：已完成（2026-10-22）

- F06：doctor按私钥保密/公钥完整性分流，常规文件与受控mount symlink目标规则和运行时一致；public0644通过，private0644/public0666拒绝；Windows未声称ACL已验。锁定gojwt1.1的内部cleanup错误不可外传，生成器改在现有函数内用标准库同格式Ed25519，O_EXCL先取得两文件所有权，再写/Sync/Close/权限/目录同步，所有主错和cleanup错合并。
- F07：run→dev、lint→tool、test→race；迁移目标中的test仍为环境占位；clean只删默认两项受管文件，以os.Root限制根内，先校验全部对象再删除。stage4-make-entries exit0，包含真实clean与相邻哨兵/symlink/越界反例；接线stub不冒充业务测试。stage4-final-race实际通过make test进入相关完整race回归；clean独立源go vet exit0。
- F13：main.Version仍仅来自version.go v0.4.0，新增显式BuildInfo入口，旧Execute/NewRootCmd签名保留；缺注入commit/time=unknown。make build/image及Docker ARG注入，main静态linker变量不在运行时重新绑定，未添加无实际重绑的allowlist豁免。
- stage4-version-keys exit0：普通go build --version无运行配置即可成功；固定真实HEAD/UTC输入的native sonic构建与make build（Linux amd64+UPX5.1.0）输出逐字一致。Linux二进制只在已启动的本地MySQL容器内执行--version，独占临时目录已清理；没有创建容器或影响DB服务。Dockerfile镜像构建/完整应用profile运行仍待X1，不把这项二进制验证冒充镜像证据。
- 同一证据中本轮新生成private0600/public0644，doctor.jwt_keys=pass，使用新钥的真实HTTP登录与带签名token注销均200，SIGTERM退出0；不读项目真实密钥。build参数、每条child exit及去敏输出见version-key-results.json。
- 4.E：CLI报告/stdout/stderr、必须flag注册、目录fsync/文件close、临时文件remove、短写、生成锁unlock及projectinit补偿失败均处理。doctor探针cleanup失败保持根因，CLI退出1；输出失败不会假报成功，已完成的密钥不因打印失败删除。
- 追加发现：projectinit回滚不完整/异常退出原来会删除旧Git恢复副本；stage4-panic-before真实exit1证实。现在目标变更前保存0700目录/0600 recovery.json原字节/类型/存在性/权限及旧Git；失败保留目录，原panic身份保持。相对根路径统一物理绝对路径，避免快照边界歧义。
- stage4-final-race、stage4-paths-and-keys完整相关race均exit0（覆盖panic/失败恢复副本、真实临时Git、输出/close错误及相对路径）；stage4-final-lint exit0（随后新增的路径/公钥定向测试由最后全仓门禁再核对）。恢复副本和输出失败语义已同步docs/testing.md，冻结部署文件及真实Git历史未改。
- 回滚只撤本阶段源码/Make/Docker参数/文档差异；保留用户依赖与版本主线，不自动删除任何真实失败恢复副本。manifest只能按确切归属、类型、原字节人工核对，不存在未实现的自动恢复承诺。

阶段5错误处理补充设计（待实施）：console保留void入口，写出失败交现有gtkit日志终结；maxbody主关闭与补偿关闭错误合并；dummy密码比对只把成功/预期不匹配映射为ErrMismatch，损坏dummy哈希返回原因错误，现有认证调用方仍区分基础设施错误。数字版本解析溢出返回原转换错误。git帮助退出只明确接受ExitError(129)，执行异常仍返回。可选接口/ctx类型断言明确处理缺失；Redis脚本畸形返回明确拒绝。

限流probe清理失败保持能力健康的兼容约束：已有测试证明DEL ACL缺失不能阻断EVALSHA能力。保留一次DEL尝试，将首个清理错误原子保存供ProbeCleanupError查询，并提供可选WithProbeCleanupErrorHandler回调，项目bootstrap接gtkit警告；不扩大既有Observer接口，不改变公共包不自行输出日志的约定，不每请求刷日志。两个探针键原Lua均有真实TTL（约61秒），保留该回收边界；运行时能力失败仍走原失败模式。Fallback若本地执行体实际报错，返回并保留Redis主因，不能伪装本地成功。

测试夹具清理失败以t.Errorf报告并继续其他清理； setup/原本应成功的同步操作以有信息的Fatalf阻止无效后续；故意拒绝/取消/已关闭等按用例断言期望。无error返回值不虚构错误，现有不变量断言单独核对。能力探测把清理错误与“不具备能力”的跳过结果分开，前者使测试失败。

阶段5定向回归补充文件：已确认存在的`internal/middleware/maxbody_test.go`、`internal/pkg/credential/password_test.go`、`internal/pkg/console/console_test.go`、`internal/plugin/builtin/mysql/mysqlversion_test.go`、`pkg/ratelimit/probe_cleanup_test.go`、`pkg/ratelimit/README.md`。验证双关闭错误可见、掩护哈希损坏与正常不匹配区分、console终结日志、版本整数溢出原因保留，以及清理错误只通知一次且不降级可用能力。

F08接线细化（待实施）：`make test-integration`先单独运行cmd中无连接的TestPluginIntegrationPrerequisites元数据检查，通过后才并发执行真实集成包，避免其他包在cmd拒绝配置根之前已经连接。现有`scripts/check-make-entries.sh`以临时Go模块实跑“cmd fixture存在→强制失败→移除cmd fixture后恢复”并断言PLUGIN_INTEGRATION=1；stub仅用来观察其他Make入口，不替代这个真实Go集成接线验证。外置APP_ROOT、项目根和配置symlink逃逸矩阵写在既有cmd集成测试文件，不改真实配置。

5.E生成器复核发现（待修）：stage5-generator真实exit1，架构传感器指出阶段3新增的`internal/pkg/httpcache/envelope.go`包装器缺少Unwrap，阻断net/http.ResponseController的写deadline链。复用既有`envelope_test.go`先以实际Gin包装+底层deadline哨兵做反例，再补Unwrap透传；保留Flush/Hijack拒绝流式的原契约。相关httpcache/middleware完整race、架构传感器、生成器和lint均须重新通过，失败不标完成。


### X1实施细化（待实施，沿用11.1授权）

| 候选 | 对现有入口的影响 | 正确性/运维边界 | 决定 |
|---|---|---|---|
| 把app加入默认services | 原dev-up同时启动应用，可能读取未准备的钥/配置 | 改变已有依赖入口，缺运行配置就失败 | 不采用 |
| 显式app profile及独立Make入口 | 默认dev-up仍只有mysql/redis；应用由新入口显式启动 | 首次先准备独立配置/临时钥/既有迁移；可选择复用已启动依赖 | 采用原方案候选B |

实施文件：现有`compose.yaml`、`compose.env.example`、`Makefile`、`README.md`、`docs/testing.md`；验证脚本拟新增在既有scripts目录的`scripts/check-compose-app.py`，仅使用Python标准库和本机Docker Compose解析，不拉镜像/创建容器；不改冻结部署、目录架构或依赖版本。应用仅使用现有Dockerfile build，APP_NAME由Make从project.mk注入，不新增版本常量/浮动远程image引用。当前精确Go/Alpine基镜像image inspect exit0；Alpine镜像静态导出已核实/bin/sh与/usr/bin/wget均指向/bin/busybox，尚不是应用镜像运行证据。

应用契约：profile为app，配置APP_ROOT=/app、env=dev、监听0.0.0.0:8008且HTTP；宿主仅127.0.0.1映射可覆盖端口。配置文件、pem目录只读绑定且create_host_path=false，日志目录为已有可写绑定；不复制实际私密配置进镜像。默认UID/GID=1000:1000可按宿主所有权显式覆盖；容器入口先验证UID>0，再exec原/app/bin/server server，root配置必须失败。仅三项挂载路径和已有本地容器可连host/port/用户/密码/库名用DEV_APP_*参数覆盖；空密码允许显式覆盖以复用本机无密码隔离服务，不让空值被fallback误改。JWT路径由挂载配置明确指向/app/config/pem下的临时钥，0600/0644规则保持；日志路径/app/logs/app。所有新变量在模板注释中说明用途、默认/空值规则、关联约束。

启动契约：dev-app-up在显式profile下构建并启动app及其健康依赖；dev-app-up-existing使用--no-deps复用本机已有服务，不新建同类DB/Redis。dev-up继续只启动依赖。启动不自动执行DDL或覆盖已有库，初次必须先对本地专用库应用既有迁移；/ready不替代schema版本核对。新增dev-app-down只停止/移除app，不带-v、不清数据卷，不调用现有dev-reset。构建/启动显式--pull=false/--pull never；执行者仍须先确认精确基镜像和目标容器存在性，缺失遵守授权规则。

健康与退出：wget轮询容器内/ready，health timeout=3s、interval=3s、retries=20、start_period=20s；依赖健康为启动前置，运行时继续由应用/ready判定。stop_signal=SIGTERM、stop_grace_period=3m为本地默认；当前3类worker每类30s、HTTP10s、drain5s等为源码输入，3m不是任意配置下最坏耗时保证，改变应用预算须同步核对。失败不自动无界重启，不通过杀共享DB做故障注入。

验证步骤与状态：X1.1先保存/回读此方案（进行中）；X1.2改profile/Make/示例/说明并运行纯解析矩阵，证明默认仅mysql/redis、profile含app、非root/挂载/host/health/信号/版本参数正确（待执行）；X1.3源码可审阅、配置解析通过后，按本机容器规则向用户明确申请仅本次临时app容器的创建权限，再真实构建当前镜像、外部API、单容器断网ready失败与恢复、SIGTERM和自有资源清理（尚未申请/待执行）。不会为等待该许可停止独立的阶段6整仓检查。若授权未给，容器运行保持未验证，不宣称X1闭环。

X1风险/回滚：UID不匹配或0600钥不可读时停止，不放宽私钥；host误指或库归属不明时停止；不自动迁移/搬运已有DB或Redis状态。app运行失败以日志/退出码报告，回滚只撤本次profile/入口/示例或停止本次app容器；现有依赖与数据卷保持，构建缓存和镜像不执行全局prune。应用镜像容量/生产SLO仍无输入，计划及200次smoke不构成压测准入。


### 阶段5：已完成（2026-10-22）

- 5.1：实际goerr1.5.0选择保留，go.mod SHA256仍为b5eac2f63027132e8105b4899c0f2a8893dbbb3ba1357da8d02224477cc790ba；go.sum只撤旧1.4.0两行。stage5-tidy-before exit1命中准确差异，after exit0无输出，整仓check由阶段6记录。
- 5.2/5.3：插件配置根从源码根分离，继承APP_覆盖清除后固定本机连接和临时钥/namespace；源构建定位保留。外置根元数据检查在全套连接前单独执行，项目根/子目录/配置symlink逃逸拒绝。Make发现含cmd，强制插件opt-in，release接线同步。
- stage5-root-matrix exit0；missing-root预期make exit2且只执行无连接前置；missing-optin预期go test exit1。stage5-make-wiring exit0，在独立Go模块中真实证明cmd fixture能使门禁失败，移除后恢复；不是DB功能模拟。
- stage5-plugin-isolated exit0，包含启动失败恢复、memory/redis真实缓存、迁移只读装配和两种进程信号；stage5-integration exit0，11个真实依赖包含cmd，复用既有本机MySQL/Redis与本轮独立库。禁止SCRIPT FLUSH的专用实例用例仍明确未执行，不因强制集成自动授权。
- 5.E：严格扫描初始209条混合告警，不等于209个真实缺陷。真实error全部按原因返回/合并/终结日志，测试释放错误使测试失败；类型断言单独校验，故意故障按errors.Is分类。无新增豁免或空if，check-blank=true，移除测试errcheck整类豁免和std-error-handling preset。
- stage5-final-lint exit0/0 issues（sonic+scaffold+integration）。初次全仓race暴露5组期望/兼容问题，已按根因更正，stage5-error-race-fixed受影响5包完整race exit0，其他包初次已通过；最后全仓由阶段6重新汇总，不把初次全仓失败改写为通过。
- 生成器拦下新增envelopeWriter缺少Unwrap；deadline反例exit1证实控制器链中断，修后架构exit0、httpcache/middleware完整race exit0。stage5-generator-fixed exit0：当前工作树导出、真实临时模块生成、5迁移格式、go build、架构和生成范围strict lint全部通过；未读真实配置，使用现有内置迁移默认值的警告保留。
- 生产定向处理还包括console写出终结日志、maxbody主/补偿关闭错误合并、dummy损坏哈希不假装密码错误、版本数字溢出保留转换原因，以及限流probe清理首错状态/一次通知。续租异常值仍errors.Is(ErrLeaseLost)，非根错误码/响应映射不变。
- 所有命令、elapsed与真实child exit在本轮commands.jsonl；生成器副本已由脚本清理，未创建容器或拉取镜像。阶段5回滚只撤本轮自有源码/工具配置/夹具与旧sum清理，保留用户原始goerr1.5.0选择及其他并行任务。


### X1：源码及解析已完成；容器实测待明确许可（2026-10-22）

- compose.yaml显式app profile、Make三个独立入口、模板和README/testing说明已保存并回读。x1-compose-contract-final exit0；使用合成环境核对默认集合、app profile、只读挂载、显式空密码、地址/身份/构建字段、健康/退出契约及root拒绝/exec参数fixture。没有将fixture当成Docker运行证据。
- Docker Compose实际2.39.2；Dockerfile精确Go/Alpine摘要本机image inspect均exit0。Alpine静态导出验证sh/wget存在；没有拉取镜像。专属app项目gin-api-remediation-51ru7lj7、HTTP127.0.0.1:58559、UID/GID501:20、本轮独立schema与container前缀已准备，钥/配置仅本轮合成数据，目录/文件权限已明确。/var与/private/var归属比较错误在复制前被断言拦下，canonical核对后成功，ERR-060已记实并关闭。
- 已按AGENTS本机容器“缺了先问”向用户申请仅本次app/独占网络创建权限（必要场景重建，同一时间1个app）；现有DB/Redis不重启、不替换、不新建。用户回复前不执行依赖该授权的构建/容器启动/断网操作；阶段6独立源码/质量检查继续。该申请不是重复申请已批准的代码阶段。

### 阶段6：进行中

当前先执行与容器许可无关的双workspace质量门禁、构建、调用方回审与报告证据整理；实际结果完成后逐项记录。当前工作区另有并行通知专项文件`docs/notification-channels-plan.md`、`internal/pkg/news/contracts.go`、`internal/plugin/notification.go`，本任务不读取其正文或修改，已保存发现时hash；整仓命令覆盖当时工作树，不将其实现归为本轮产出。

阶段6调用方回审的说明修正：已读`internal/module/auth/application/ports.go`仍把CompareWithDummy注释写成“恒返回不匹配”，但接口既有饱和/取消及本轮损坏哈希都可返回基础设施失败；仅修正文档契约。`cmd/server.go`的newServerCmd说明需对齐本轮已实现的banner先验写出顺序；两处仅注释，随后常规受影响门禁统一覆盖，不新增运行行为。

阶段6追加核对：`internal/runtime/module/auth/adapters.go`同一密码掩护接口的旧注释也同步基础设施错误语义，行为不变。工作区make check首轮被文档路径传感器拦下，伪路径写法已改正文案，不加白名单。回审发现`scripts/check-make-entries.sh`的子Make会继承父Make的覆盖率路径/MAKEFLAGS，先在本轮自有外部哨兵路径做反证（待执行），成立后在fixture进程内清除继承的Make控制变量和夹具不应继承的目标/覆盖路径/标签；不改变真正make check的参数语义。复测带外部coverage参数的Make自检、完整双workspace门禁；任何外部哨兵变化立即停止。

上述Make反证stage6-make-override-before实际exit2，clean正确拒绝外部路径，夹具未隔离父参数才是根因。修复仅在自检子进程清除MAKEFLAGS/MFLAGS/MAKELEVEL/MAKEOVERRIDES及夹具目标/标签/coverage变量，且GOWORK=off明确使用自有临时module；父真实门禁的workspace/coverage参数不变。外部哨兵内容须前后保持。

阶段6错误链回审追加（待反证/修复）：`pkg/ratelimit/redis.go`清理错误不降级健康是兼容要求，但若调用方在DEL期间取消，构造函数仍应按既有“不吸收调用方取消”契约中止。复用既有`probe_cleanup_test.go`注入清理时cancel及独立DEL错误，先证明当前反例；成立后probe返回取消+清理原因的Join，同时保留无取消时DEL ACL缺失不阻断能力、首次错误/一次通知。前置为本轮源代码已读且未并行修改；验收是两种degradedStart模式均可errors.Is看到两个原因、受影响限流/bootstrap完整race和严格lint通过；不引入重试、接口变化或新依赖。

阶段6阶段性事实：上述取消反例exit1，修后stage6-probe-cancel-fixed完整受影响race exit0；workspace完整make check已exit0，含lint/vet/现代语法及全仓race覆盖率83.1%，门槛72.5%未降低。Make自检同一覆盖参数复测exit0且外部哨兵未变。锁定依赖check-ci仍待本轮真实结束后记录；不以workspace结果代替。

用户对容器申请的回复为“本机没有go吗，为什么需要创建容器”，这是询问原因，不是创建许可。已解释本机Go1.27.3及原生HTTP/信号证据与Compose身份/挂载/PID1验证的区别；继续原生回归，不创建容器。X1容器运行项保留未验证，不据此阻断独立核心回归或宣称附件整体通过。

阶段6交付路径与步骤补充：在既有docs目录保存`acceptance-remediation-results-2026-10-22.md`，包含F01–F18逐项处置、实际命令/退出码/证据、残余分级、兼容/回滚及未验证边界；前置为对应命令真实完成，未知不得填为通过。将原计划阶段/里程碑状态对齐本节执行记录并逐项回读，不重排阶段。仅对本change执行严格OpenSpec验证；未完成的X1运行和归档步骤保持未勾选。最后定向清理本轮唯一schema及已证明归属的Redis key家族，保留去敏证据、失败日志和并行任务文件；数据库归属或前缀不能确认时停止清理并报告。交付验收为两种口径门禁、原生最终二进制真实HTTP/信号、真实依赖集成、逐项证据及外部哨兵/他人文件hash边界可复核；未获容器许可、容量/SLO及规范冲突分别列为未验证/未决。

阶段6原生依赖故障步骤：在仓外证据目录新增本轮TCP转发夹具，仅绑定127.0.0.1随机空闲端口并转发到已核实本机3306/6379；APP_ROOT沿用本轮隔离配置，APP_DATABASE__HOST/PORT与APP_REDIS__ADDR仅对该临时应用进程覆盖，使用新独占prefix。应用就绪后逐一阻断本轮代理及其自有连接，验证/health=200、/ready=503及具体依赖失败，再恢复代理验证/ready=200；依赖本身不停止、不改配置。SIGTERM并等待本轮子进程，关闭代理及连接并记录退出码/耗时。任何连接指向未知服务、原生应用启动失败或恢复超时立即记录失败并收尾；不创建容器，不触碰共享key。该证据只覆盖原生进程依赖故障模型，不替代Compose运行验证。

阶段6回审发现阶段5.E的扫描边界遗漏（补齐后才可报告error检查闭环）：锁定golangci-lint2.14.0本机源码证实，errcheck默认豁免仍独立于已删除的std-error-handling preset；`scripts/security-eval.go`、`check-doc-refs.go`、`check-package-vars.go`、`clean.go`的build ignore也使它们不进入常规./...扫描。前三个工具确有未检查fmt输出。当前“0 issues”仅证明原检查口径，不能外推到这些文件。

补齐方案沿用原5.E→6复测顺序，不加依赖/豁免或改架构：先在仓外严格配置启用disable-default-exclusions，分别扫描应用及4个独立Go工具并保留真实结果；只按该库存补充本节实际命中文件。`.golangci.yml`同步显式关闭内置豁免；Make的tool在常规lint后逐个扫描4个独立工具，仅追加errcheck覆盖，不改变安全漏洞/文档/包变量判据。三个独立工具的标准输出/诊断写出需检查error与短写，失败非零退出；stderr自身失败直接非零退出，不递归记录。保持现有参数、正常输出文本和正常退出语义。

补齐步骤：①计划保存/回读及源码库存；②缺失错误的逐项处理（测试中意外错误令测试失败；由标准库保证不失败的写法也显式检查，不用空分支）；③临时编译四工具，正常运行及以只读文件描述符注入输出失败，验证非零和哨兵不变；④独立脚本严格errcheck、应用sonic/scaffold/integration lint、双workspace make check和生成器、受影响完整race。新源文件先按真实库存登记，再编辑；任一失败根因未知或须修改冻结部署/安全判定规则时停止。交付为补齐后的源码/门禁、初次与最终退出码及报告范围说明；回滚仅撤本段工具/检查差异，不恢复被忽略错误为“通过”。原生功能已完成的证据保留，若进一步改变应用行为须重建并复测受影响真实接口。

补齐实际库存（本轮strict-complete.yml，尚未整改）：应用22条，security工具8条，docrefs工具25条，packagevars工具7条，clean工具0条；62条诊断不能等同62个可发生缺陷，多数应用新增命中是标准库Builder/Buffer/HMAC保证不失败的写接口。应用文件：`internal/middleware/logger.go`、`internal/module/ai/application/service_test.go`、`internal/module/ai/transport/http/handler_test.go`、`internal/pkg/upload/background_error_test.go`、`internal/plugin/builtin/logging/plugin_test.go`、`internal/router/panic_observability_test.go`、`internal/runtime/module/ai/adapters.go`、`internal/scaffold/module.go`、`pkg/ratelimit/key.go`。沿用现有返回签名，能返回的写失败加入现有error；仅返回字符串的内存key拼接用等价字符串连接去掉无意义的error接口，HMAC的固定标准库实现若违背其nil-error契约则作为内部不变量失败panic（现有HTTP Recovery处理），绝不返回空key或未加密替代。工具的输出/清理检查须让defer实际执行：以main最后决定退出，内部正常返回error或仅向本工具边界传播受控失败，不在资源拥有函数中os.Exit跳过清理；主错与清理错并存保留。以上仍待实际实施及验证。

补齐已执行：stage6-complete-lint-fixed（make tool，应用及4独立脚本，内置豁免已禁用）exit0；stage6-supplement-race受影响完整9组包/子包exit0。三个工具改main最终处理退出，内部return保证defer清理和Join；正常判据/文本/参数未改。stage6-tool-output-contracts首轮最后一项误套clean具体退出码，已按原契约只修夹具，ERR-066记实；修后26项完整exit0，包含三个旧工具只读stdout却exit0的真实反证、新工具stdout失败exit1、三个新main的stderr失败exit2及clean保持exit1，外部哨兵不变、self-test目录残留0。最终双口径/生成器与扩展标签lint仍待下轮命令实际完成，不用上述局部结果替代。

阶段6边界测试补齐：新增error检查后workspace完整门禁exit0，但聚合覆盖率82.9%，低于计划参考83.0%；不能只拿72.5%门槛通过掩盖该差异。真实cover报告和既有测试核对显示，本轮新增的信封writer Hijack拒绝及WriteString接口尚无直接用例，非法media type/JSON/字段类型/未经helper的大响应也缺独立锚点。只修改已登记`internal/pkg/httpcache/envelope_test.go`，扩展现有非法准入矩阵，并验证普通WriteString信封miss/hit仍保持本次ID与data；Hijack拒绝返回可判定错误且不返回连接、不缓存，每次仍调用源handler。不得为覆盖率而改生产行为或构造标准库不可能的写失败。前置为本段保存/回读、源文件与coverage已读；验收是httpcache完整race、严格lint和最终双口径聚合覆盖率重新记录，现有参考下降若仍存在须如实解释并保留未接受状态。原生二进制/真实集成证据仍对应未变的生产源码；本步骤仅新增测试，不重复生成生产产物。

同一信封边界矩阵还需固定两个既有承诺：普通JSON的提前WriteHeaderNow不得绕过检查；极早无服务端ID的miss/hit须返回空request_id并清掉陈旧X-Request-ID，不把客户端/缓存旧ID当权威。仅在同一测试文件加入这些协议断言，不增加新功能。


### 阶段6最终交付记录（2026-10-22）

- 生产源码定稿后：stage6-workspace-final完整make check exit0，stage6-locked-final完整make check-ci exit0并真实生成ci_probe；新增最后边界测试后，stage6-boundary-full-lint扩展标签全仓exit0，双口径check-coverage均exit0/83.1%，未降低72.5%门槛。此前82.9%及所有失败日志保持，未因数字造不可能失败。
- 最终原生/CGO0 Linux构建均exit0；原生29 HTTP场景、25 JSON关联ID、200 health请求全部通过；SIGTERM exit0/1.0898s。原生专属代理分别中断MySQL/Redis后ready503/live200，恢复ready200，7条断言通过；未停共享依赖。
- 强制真实集成最终11包exit0；goerr1.5错误原因链、主错/清理错、probe取消、CLI/生成恢复副本及4独立工具输出错误均有实际证据。OpenSpec仅本change strict/JSON/no-interactive验证exit0/valid=true。
- 本轮20组精确namespace/DB清理扫描30键、DEL30、复扫0。独占schema gin_api_remediation_20261022_51ru7lj7（6表）及合成配置/临时钥保留在本机供复核，未执行DROP；不清历史app_client共享前缀，不FLUSH/SCRIPT FLUSH。既有依赖容器仍运行。
- 已保存并逐段回读[完整整改复测报告](/Users/xiaozhaofu/go/src/Project/gin_api/docs/acceptance-remediation-results-2026-10-22.md)，34个报告链接均存在；delivery-manifest.json核对451个源码/配置文件未漂移、用户go.mod保留/go.sum仅去旧两行、并行通知文件hash不变，git diff --check exit0。
- 当前结论：核心源码及本地功能复测通过；附件整体/生产不准入。F10集群依用户延期；F09仅源码/解析通过、容器运行未获许可，F12/F17规范文字未统一，F14只同步本轮相关说明，F15/F18无关治理未实施。外部严格客户端、Windows ACL、容量/SLO/生产发布未验，不使用Ready措辞。
- 清晰停止点：原计划阶段6.3是报告交付，已完成；本change任务6.3为归档，因5.4运行项未验证仍未勾选。遵守openspec-auto归档前任务全部勾选的要求，不伪造容器结果或归档成功，不处理其他active changes。后续获准相应本机资源操作时从X1.3继续，不自动重做核心阶段或创建依赖容器。
