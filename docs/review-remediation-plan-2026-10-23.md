# 全项目审查修复方案与分阶段实施计划

> 2026-10-27 位置更新：限流包已移至 `internal/pkg/ratelimit`，仅供当前项目使用。
> 下文保留当时的目录、命令和执行证据，旧路径不代表当前导入位置。当前方案见
> [限流包迁移计划](ratelimit-internal-migration-plan-2026-10-27.md)。

计划日期：2026-10-23；续作日期：2026-10-24。当前项目：`D:\go\gin_api`（沿用现有目录和架构）。

**当前状态：阶段 1—6 的修复、测试适配及本次相关验收已完成。** R01—R06 已包含在最后提交 `29a58cd`，本次续作补齐 R07—R10、S01—S03 及遗漏说明。用户随后明确授权“测试文件也可以一起写了，进行该有的测试”，替代先前测试延期边界；第 9.1—9.2 节保留实施历史，最终结果与验证范围见第 9.3 节。修改保留在当前工作区，未提交或部署。

## 1. 需求来源、范围与授权

需求来源为本会话的全项目只读审查，以及用户本轮指令：“针对这 10 个问题和 3 处可简化项，先制定完整修复方案和分阶段实施计划，保存到项目文档并回读核对。沿用现有目录和架构，明确兼容性、风险、回滚和验收标准，先不要改代码或运行检查。”

初次计划轮仅新增本文；当前实施轮按最新授权修改第 5 节生产文件及其对应说明，不修改依赖、安全 hook 或用户配置。不接手其他会话的 change，不把此前只读审查的门禁例外扩展到本轮实施。本文使用 R01—R10、S01—S03 固定对应上次报告的顺序，后续不得重排编号或替换问题。

每阶段必须核对用户最新授权、本文、全局规则、项目规则及可用错误记录。最初的测试和检查延期要求已由追加测试指令替代，本次执行相关功能测试、构建、vet、lint 和 race；未执行项及环境边界见第 9.3 节。

### 1.1 目标

1. 修复身份精度、限流健康判定、Redis 超时与重复扣减、慢桶过早失效、验签服务强制持有私钥这六项运行路径问题。
2. 修复通用缓存的失效竞态及取消等待者积累，修复周期调度器退出跟踪与不实 fencing 契约这四项扩展能力问题。
3. 删除空模块回滚机制，统一 `platform/data` 的实际包名，精简限定范围内的过时注释。
4. 每项交付有明确失败场景、兼容边界、可重复验收方法及独立回滚单元。

### 1.2 非目标与不变量

- 不移动任何现有目录、文件或模块，不调整分层与依赖方向，不更换 DI/生命周期框架。
- 保留 `bootstrap → runtime/module → application` 的显式装配；HTTP 适配、repository、models 的现有边界不变。公共 `pkg/ratelimit` 不引入任何 `internal/*` 依赖。
- 保留 Owner 的资源所有权、Resources 的借用关系、真实 provider 清理、WorkerManager、account 独立装配及双 Recovery 边界。
- 不升级 Go 或第三方依赖，不增加数据库表、索引、迁移，不改变 Redis 键名、DB 号、现有桶字段、任务消息格式和 HTTP 响应结构。
- 不增加微服务、通用框架、配置开关、后台扫描器、全局重试层或新的外部组件。
- 不修复报告之外的缺陷，不执行全仓注释重写、导入别名清洗或性能调参。
- 不承诺 QPS、P99、RSS 或吞吐提升百分比；本次没有容量测量。部署规模、生产流量与上线窗口未确认，不据此设定副本数或告警阈值。

## 2. 当前事实与证据边界

### 2.1 证据口径

最初审查和计划以源码、已锁定依赖源码、规则和既有记录为依据，当时没有运行检查。以下设计保留问题基线和验收标准；2026-10-24 的实际运行证据统一记录在第 9.3 节，不能将历史记录或源码回读替代本次验证。

缓存与周期调度器目前属于扩展能力：审查未发现该通用缓存接入生产业务，也未发现当前注册周期任务。仍修复其契约，但不将风险表述为已经发生的线上事故。

错误记录 `ERR-20261023-143` 曾记载同类缓存失效问题及 nil 结果问题已修复；当前 `cache.go` 仍存在分离的 fenced 检查与写入。应以当前源码为实施基线，历史通过不能替代本次验证，也不凭此推断变更丢失的原因。`ERR-20261022-045` 的异步 panic 恢复契约必须保留。

### 2.2 锁定版本

| 项目 | 当前已读取的版本；本计划全部保持不变 |
|---|---|
| Go / Gin / GORM | Go 1.27.3；Gin v1.12.0；GORM v1.31.2 |
| JWT | `github.com/golang-jwt/jwt/v5 v5.3.1`；`github.com/gtkit/gojwt v1.2.0` |
| Redis | `github.com/gtkit/redisx v1.4.0`；`github.com/redis/go-redis/v9 v9.23.0` |
| 任务 | `github.com/gtkit/asynqx v1.6.0`；`github.com/hibiken/asynq v0.26.0` |
| 同步与日志 | `golang.org/x/sync v0.24.0`；`github.com/gtkit/logger/v2 v2.5.0` |

已核对：redisx 提供无参 `WithContextTimeoutEnabled()`；go-redis 的命令支持 `NoRetry() bool`，但默认 `Cmd` 返回 false，`EvalSha` 会立即交给执行器；asynqx 的 `Shutdown(ctx)` 可重复等待同一停止过程，第一次等待超时不等于底层已退出。这些是方案依据，不是本轮运行验证。

### 2.3 保护现有工作区

准备阶段 `git status --short` 显示以下七个文件已有修改：`cmd/MODULE_GENERATOR.md`、`docs/architecture.md`、`internal/bootstrap/rollback.go`、`internal/platform/redis/owner.go`、`internal/platform/redis/owner_test.go`、`internal/scaffold/module.go`、`scripts/check-make-entries.sh`。

以上是 2026-10-23 的历史工作区。2026-10-24 续作基线为 `29a58cd`，开始时工作区干净；这些历史修改已在提交内，本次不回退或重做。

计划轮未触碰这些修改。当前实施前已读取并保留上述七文件差异作为比较依据；只追加获准的局部差异。不得用 `git restore`、整文件覆盖或重置分支回滚这些文件。若内容已变化，重新定位同一修复点；无法区分修改归属时停止该步骤。

## 3. 问题、决策与验收映射

| 编号 | 对应审查问题/简化项 | 决策摘要 | 阶段 |
|---|---|---|---|
| R01 / P1 | Laravel 数字 sub 经 float64 丢失用户 ID 精度 | JSON Number 精确解析，明确整数表示边界 | 1 |
| R02 / P1 | 请求取消累计为共享 Redis 限流后端失败 | 区分取消与依赖故障，探测归属可核对 | 2 |
| R03 / P2 | Redis 命令 socket 未受请求 deadline 约束 | 统一启用 context timeout | 2 |
| R04 / P2 | EVALSHA 自动重试可能重复扣减 | 扣减命令单独禁止不确定结果重放 | 2 |
| R05 / P2 | 24 小时 TTL 小于慢桶补满时间 | 保留 TTL 上限，对不满足边界的 Redis 策略启动报错 | 2 |
| R06 / P2 | external 模式仅验签仍强制读取私钥 | 按模式装配签发/验签能力 | 3 |
| R07 / P2 | Invalidate 与旧值提交存在竞态 | 失效、校验与提交使用同一同步边界 | 4 |
| R08 / P2 | DoChan 为取消等待者保留 channel | 每个在途键共享一个完成通知，不存等待者列表 | 4 |
| R09 / P2 | 调度器关闭超时被吞且旧实例失去跟踪 | 超时可观测；保持归属至真实退出 | 5 |
| R10 / P2 | 周期任务接受 Fenced 但未传递 fence | 周期任务只接受已实现的 Idempotent 保证 | 5 |
| S01 | 无生产登记项的模块 rollbackStack | 删除空栈及无效清理接线，保留真实 provider 清理 | 6 |
| S02 | `platform/data` 的实际包名为 dao | 原目录内改为 data，同步消费者 | 6 |
| S03 | 历史长注释及已删除 API 的残留说明 | 限定文件清理，保留有效并发和生命周期约束 | 6 |

### 3.1 关键方案取舍

| 决策 | 采用方案 | 未采用方案及原因 |
|---|---|---|
| R04 重试 | 对非幂等扣减命令设置 NoRetry；复用现有连接池 | 全局关闭 Redis 重试会扩大影响；独立连接池增加资源及所有权；请求去重记录会改变键和状态协议 |
| R05 慢桶 | 维持 24h 资源边界，校验补满时间加 60s 余量不超过该值 | 无条件放大 TTL 会改变内存驻留上界，并需重新解决 Duration/Lua 数值范围；继续截断会保留错误 |
| R06 密钥 | 增加验签密钥集构造，保留严格签发构造 | 将所有 KeySet 的私钥都改为可选会削弱 local 模式；新建一套独立 JWT 框架造成重复 |
| R07/R08 缓存 | 用现有 loading 登记承担结果合并 | 叠加 singleflight 和等待者限流会保留两份状态；每请求启动桥接 goroutine 会把 channel 积累转成 goroutine 积累 |
| R09 生命周期 | 等待超时与真实退出分开；同一 Run 跟踪旧实例 | 超时后直接 return 或继续下一轮会漏跟踪；修改 asynqx 依赖超出本次范围 |
| R10 保证 | 显式拒绝当前无法兑现的 Fenced | 实现端到端 fencing 要改消息载荷和消费者持久化约束，本次没有对应业务和授权 |

## 4. 逐项修复设计

### R01：Laravel 用户 ID 精度

**现状**：`internal/pkg/jwtauth/laravel.go` 的 `ParseToken` 未启用 `WithJSONNumber`；`laravelUserID` 会把 float64 转成 int64。有效签名的数字 `sub=9007199254740993` 可能被解释为相邻 ID。触发还需启用 external/Laravel 兼容、处于接受窗口，并经过现有用户校验；这不是绕过签名。

**改动**：在不可被调用方选项覆盖的基础 parser options 中加入 `jwtv5.WithJSONNumber()`；数字走 `json.Number.Int64()`，字符串继续 TrimSpace 后 ParseInt。移除不再来自解析器的 float64 身份分支，避免保留有歧义的精度路径。仍要求 ID > 0，保持签名算法、角色映射、exp/nbf、兼容截止时间与授权检查不变。

**兼容**：普通整数字面量和整数形式字符串不变；完整支持正 int64 范围。明确收窄：`123.0`、`1e3` 这类过去经浮点路径接受的数字表示将被拒绝，发送方应使用 `123` 或 `"123"`。是否存在这种上游编码未确认，阶段 1 批准前应确认接受这一边界；不通过再次转 float64 维持模糊行为。

**验收 A01**：使用真实签名的固定 token，覆盖 1、2^53−1、2^53、2^53+1、MaxInt64、溢出、0、负数、小数、指数、缺失、数字字符串。`UserID` 与 `Subject` 必须精确一致；中间件的校验桩必须收到原 ID，不得查询相邻 ID。无效签名仍失败。

### R02：取消不污染共享后端健康

**现状**：`pkg/ratelimit/redis.go` 的 `check` 把所有 evaluate 错误交给 `HealthState.Observe`。共享健康策略默认连续三次失败降级，因此三个取消请求可影响其他正常请求。

**改动**：提交前检查 `ctx.Err()`，已结束请求不获取探测名额、不调用 Redis、不采样健康状态。执行中的纯 `context.Canceled` 错误树视为无样本：既不计失败，也不清零失败计数或增加成功计数。聚合树只要包含真实网络/服务端失败仍按失败处理。不以字符串匹配错误，不把所有 `DeadlineExceeded` 都当取消；提交后的真实超时仍是依赖失败样本。

探测归属使用包内准入凭证，包含健康代次及是否取得探测名额。只有同代次的探测拥有者能释放名额；旧普通请求的迟到取消不能释放新探测名额。探测被取消时回到可重探状态，保留已有成功/失败计数，按原 ProbeInterval 控制重探，不能永久停在 probing，也不能当作成功恢复。凭证及状态转移留在 `health.go`；沿用现有公开入口，避免让公共包依赖内部 errx。

**兼容**：失败策略、状态名称、阈值、冷却、HTTP 映射不变。错误仍向当前调用者返回；仅修正它对其他请求的共享影响。针对取消的健康变化不应触发“Redis 已坏”的误导日志。

**验收 A02**：Healthy 下三个纯取消后仍 Healthy；真实失败累计不被取消清零；取消探测后能再次探测且同时最多一个；旧代次完成不干扰新代次；`Join(Canceled, networkError)` 仍计故障；正常限流拒绝是成功样本。

### R03：Redis deadline 贯穿 socket I/O

**现状**：`internal/platform/redis/open.go` 的 buildRedisOptions 未启用 context timeout。当前 redisx 默认读超时 3s，ready 的依赖预算为 2s，后者不能保证约束已经进入 socket 读的命令。

**改动**：统一追加 `redisx.WithContextTimeoutEnabled()`，适用于该入口创建的全部 DB 客户端。保留现有 Dial/Read/WriteTimeout 配置与默认值，使实际 deadline 取请求预算和连接超时中较早者。Owner、启动回收、恢复重建都复用同一入口，不另造客户端。

**兼容**：不改配置键和默认数值；原先超出请求预算才成功的命令可能更早返回超时，这是有意修正。无 deadline 的 context 不因此获得“即时打断任意 socket I/O”的承诺。需回审响应缓存、refresh token 与健康检查的错误传播。

**验收 A03**：本地数字回环地址的受控 Redis 协议对端完成握手后暂停业务回复；100ms 请求 deadline 下业务调用应在 500ms 容差内退出，且不等到 3s。无 deadline 路径继续受所配 socket timeout 限制；ready 的 2s 预算另测在 2.5s 容差内退出。容差是测试目标，不是性能 SLA；不得以纯 mock 证明 socket 行为。

### R04：扣减不因自动重试重复执行

**改动**：在 `pkg/ratelimit/redis.go` 内用嵌入 `*redis.Cmd` 的私有命令实现 `NoRetry() bool { return true }`，通过 client.Process 执行 EVALSHA，显式设置与标准 EVALSHA 一致的参数和首键位置。最小 `scriptRunner` 接口用 `Process(context.Context, redis.Cmder) error` 取代 EvalSha，保留 ScriptLoad、Del。统一用于能力探针与业务扣减，避免仅修其中一路。

同步修改现有 `internal/bootstrap/ratelimit.go` 的 `redisCommands`：每次 Process 仍从 Resources 借用当时的 DB 0 客户端，原样传递命令对象，保留 NoRetry 属性；客户端暂不可用时，给同一命令设置并返回 errRedisUnavailable。不能重新构造普通 Cmd 丢失属性，也不能缓存旧客户端而破坏 Redis 恢复接线。其他 tokenstore/verifycode/singleton 的 EvalSha 不在本项修改范围。

首次响应明确为 NOSCRIPT 时，仍允许加载相同脚本后执行一次：该响应意味着此前脚本未执行。不因 EOF、读超时或连接断开重放可能已经执行的扣减。其他 Redis 命令及连接池的重试设置保持不变。

**兼容**：Redis 键、Lua 参数与结果、失败策略、观测字段不变。源码兼容边界：直接传入 go-redis 客户端的调用者不变；仅实现旧三方法的自定义 runner/测试桩须实现 Process。仓库外是否存在此类适配器未确认，不能称为对所有使用者零破坏。结果不明时可以已有一次扣减，但绝不能宣称“失败一定未扣减”或擅自退回 token。

**验收 A04**：受控对端记录 EVALSHA 已执行一次后断开回复，确认扣减只执行一次，错误按原 FailureMode 传播；NOSCRIPT 分支最多重新执行一次且最终只扣一次；真实 Redis 的正常 Lua 路径及启动探针不退化；构造命令保留脚本键的路由信息。

### R05：补满时间与 TTL 一致

**决策**：保留现有 `maxBucketTTL=24h`、`ttlSlack=60s`，Redis 策略必须满足 `ceil(FixedPointBurst / FixedPointRate) + 60 <= 86400`（单位秒）。以脚本实际使用的 microtoken 整数计算；使用商及余数实现向上取整，先校验正数和数值范围，不先做可能溢出的乘法或 Duration 转换。

将约束放在现有 Policy.Validate 的 Redis 分支，确保配置装配、NewRedisLimiter 和 NewFallbackBackend 都不能旁路；本地策略不额外套用 Redis 驻留约束。`bucketTTL` 复用同一补满计算，保留非法直接调用的防御边界，不能再对合法策略静默截短补满窗口。

**兼容**：这是配置接受范围收窄。`Rate=0.000001, Burst=1` 的补满时间为 1,000,000s（公式计算，非实测），将被明确拒绝。当前 example 的五组 rate/burst 未超过此边界，但实际部署值未读取、未确认。上线前须在受控配置清单中核对；不自动增大 rate、减小 burst 或改 FailureMode。新旧实例对仍受支持的策略可共用原键；无需清空 Redis。

**验收 A05**：补满 86340s 加余量恰等于 24h 通过，超过边界失败；最小速率大桶不发生溢出；按 fixed-point 舍入后的速率计算；非法策略在首次业务请求之前报出 policy ID、实际补满时间及上限；受支持桶不会在恢复满额前被 TTL 回收。同步结构体注释、example 与 README，不增加新配置项。

### R06：external 模式不持有签发私钥

**改动**：保留 `NewKeySet(SigningKey, previous)` 的严格签发校验；在原 `keyset.go` 增加 `NewVerificationKeySet(VerificationKey, previous)`，仅复用两条真实路径共同需要的公钥、kid 唯一性、previous 上限与索引构造。仍用同一 KeySet/Parser，不引入平行框架。

`Assemble` 明确按 Auth.Mode 分支：local 读取并校验匹配的公私钥，构造 Issuer+Parser；external 只读取 active/previous 公钥，构造 Parser，Issuer 为 nil，忽略未使用的私钥路径，不因其不存在导致失败。未知模式报错，不将未知值默认为 external。`NewIssuer` 对没有有效签发私钥的 KeySet 明确报错。

`validateJWTRotation` 接收认证模式并按 local/external 校验私钥必填性；`Runtime.TokenIssuer` 显式处理 nil Issuer，避免返回持有 typed-nil 的接口。local 的 auth runtime、外部模式仍需要的 account 校验、旧公钥轮换、无 kid 接受窗口均保持不变。

**兼容**：配置字段不改名；external 从“必须有私钥”放宽为“不加载私钥”。`jwt generate` 仍按现有规则生成完整密钥对，未传 out-dir 时两条输出路径仍必填；`jwt reencode` 仍显式要求完整匹配密钥对，不把运行期验签放宽传播到这两条命令。私钥权限规则仍适用于实际需要读取私钥的路径。

**验收 A06**：只挂载公钥的 external 装配成功、Issuer 接口等于 nil，签名验证、用户/VIP/角色判定正常，local 端点仍不注册；external 私钥路径缺失或指向不存在文件不触发读取；local 缺私钥、错配、错误权限仍失败；previous 重复 kid/错误公钥仍失败；两条 JWT CLI 原语义不变。

### R07/R08：用一份在途状态完成缓存合并与失效

**改动**：把原 `loadEntry` 扩展为私有泛型登记项：一个 done channel、typed value、error、fenced 标记。每个在途键只有一个登记项和一个 loader；所有调用方直接 select 自己的 ctx 与同一个 done，不保存每个等待者的 channel，不为等待者另起 goroutine。删除本文件 singleflight.Group、DoChan、Forget 及二者配合的冗余状态；x/sync 在其他模块仍有用途，不改依赖。

miss 准入在 loadMu 内二次核对缓存与 loading；仅首次创建登记项时占用 maxInflight 名额。loader 仍使用发起者 context 的值、WithoutCancel 和独立 loadTimeout。取消等待不取消共享加载；忽略 ctx 的 loader 占用原名额直至真实退出，不能因超时移除登记再启动无限个替代 loader。

固定锁顺序为 `loadMu → mu`。TTL/jitter 等可调用函数在提交锁外计算；随后在同一临界区核对登记项归属与 fenced，并完成缓存写入。Invalidate 在相同顺序内标记在途项并删缓存。结果赋值、登记移除绑定同一登记项；发布完成后在锁外关闭 done。避免旧代次清理删除新代次状态，也避免调用注入函数时持锁重入。

**兼容**：GetOrLoad、Invalidate、Len、Option、LRU、TTL/jitter、错误不缓存、调用方独立取消、panic 的固定错误及内部日志语义不变。失效只承诺旧加载不回填；已经参与旧加载的等待者仍可能得到该次结果，不宣称读写强一致。typed value 同时避免通过 any 断言误拒绝合法 nil，但不扩大缓存语义。

**验收 A07/A08**：通过受控阻塞点使失效发生在旧加载返回与提交之间，完成后缓存不得出现旧值；新加载正常缓存；高并发同键只执行一次 loader；1,000 个短 ctx 等待者依次取消后只保留该键的一份登记与完成通知；64 个忽略 ctx 的不同键占满默认名额，第 65 个快速 ErrLoadsSaturated。解除阻塞后名额归还；panic、错误、合法 nil 均广播相同结果且不泄漏登记。使用确定性时序及实际分配/驻留证据，不能只凭 Len 或一次 GC 后 RSS 下结论。

### R09：调度器关闭超时与真实退出分离

**现状**：asynqx.Run 在关闭等待超时时返回 DeadlineExceeded，但底层停止仍在进行；当前代码将其作为纯中止吞掉，离开 leadOnce、释放租约，并可能开始下一轮。Cleanup 的空 Once 不能跟踪旧实例。

**改动**：在原 scheduler.go 内显式使用底层 Start、Shutdown，不依赖 Run 返回值推断生命周期。把本轮 inner 的归属保留到真实停止；首次 Shutdown 使用现有 tasks.shutdown_timeout（默认 25s）作为等待预算。超时立即通过现有 RoundFailed 观察器报告，随后同一个 Run goroutine 再次等待同一实例的 Shutdown 完成；不重建实例、不 busy loop、不创建无限后台收尾者。

后续等待仅承担“仍在运行”的归属跟踪，可不附加会提前放弃跟踪的 deadline；它不是新的业务 I/O。顶层 WorkerManager 继续拥有整体关停预算：若底层始终不退出，则 worker 始终未退出，既有安全门应保留其依赖资源并报告未完成，而不是谎报已清理。该行为必须由装配层回归验证，不能仅测 Scheduler。

取消续租并等待续租 goroutine 结束；只有 inner 确实停止后才结束本轮并尝试按原 token 释放租约。失联或进程暂停时租约仍可能自然过期，因此不承诺跨实例绝不重叠。下一轮必须晚于上轮真实退出。保留启动/注册失败与释放失败的聚合错误；移除将所有 DeadlineExceeded 当正常中止的判断。正常父 ctx 取消且完整关闭才返回正常结束。

Cleanup 改为读取当前持有实例并按传入 ctx 等待同一幂等 Shutdown；超时不清除归属。实例指针及代次用同一把短临界区锁保护，锁外执行 Start/Shutdown/观察器；清理完成仅能移除自己持有的那一代。删除空 released Once。包内仅增加用于真实故障注入的最小 runner 接口（Register/Start/Shutdown）与构造接缝，不扩展成通用生命周期框架。首次排空超时须保留在本轮错误/观察记录中，晚退出不能把该超时改写为“从未发生”；一次故障不重复上报。观察器文案避免在排空未完成时声称“已停止/即将重试”。

**验收 A09**：让底层关闭超过首次预算：可观测一次超时、Run 不宣告退出、无第二实例、共享资源未释放；允许旧实例完成后才结束/重试并释放原租约。覆盖进程取消、续租失败、注册失败、重复 Cleanup、Cleanup 等待取消和释放失败。测试必须释放自己的阻塞桩，不能留下不可结束的测试 goroutine。

### R10：周期任务只声明可兑现的保证

**改动**：PeriodicTask.validate 使用明确 switch，只接受 GuaranteeIdempotent；零值、GuaranteeFenced、未知枚举均报出任务名及原因。更新注释和错误文本，说明 UniqueTTL 仅是附加去重，不证明业务幂等。保留一般 singleton.Runner 的 Held.Fence 能力，不全局删除 GuaranteeFenced。

**兼容**：当前无周期任务注册，现有消息载荷、队列名与消费处理器不变。接入方若使用 Fenced 必须停止注册并另行设计端到端 token 传递与写入校验；不得机械改成 Idempotent 后声称安全。

**验收 A10**：Idempotent 可注册并带原有唯一性选项；零值/Fenced/未知值在启动前拒绝；普通 singleton 的 fencing 测试仍维持原语义；文档不再对周期任务承诺 fencing。

### S01：移除空模块回滚装配

只移除当前没有生产 push 调用的 rollbackStep/rollbackStack、services.cleanup、空 modules 登记及相关无效 unwind。`buildServicesWithBudget` 失去预算用途后改为 `buildServices`，同步移除 assembly_contract_test.go 中原同名包装，直接测试生产入口；同步生成器说明中的函数名。

保留 `providerCleanup`、真实 extensions/telemetry/metrics/task producer 清理、关闭顺序、rollbackBudget 的其他用途及关闭错误聚合。删除空栈专属测试，迁移有真实价值的清理断言，不能为了旧私有测试保留死代码。若实施前已出现真正的模块资源登记，本项停止，先修订方案，不能删除有效所有权。

**验收 AS01**：生产与生成代码不引用被删类型/字段；失败回滚仍逆序，正常关闭仍按既有阶段；真实 provider 的失败不阻止后续清理，重复关闭不重复释放。生成器标记与输出编译契约保留。

### S02：目录与 package 一致

`internal/platform/data` 五个生产文件及八个测试文件的包声明由 dao 改为 data，原目录不移动。同步已读取的 13 个导入文件中对该路径的称呼；无冲突的 `data ".../data"` 去掉冗余显式名，有真实局部遮蔽时只在该处使用职责清晰的名称。不更改其他依赖的别名。

**验收 AS02**：该目录的 package 一致，导入解析到实际 data 包，导出函数/类型/方法和资源行为保持不变。此项不改变模块导入路径；可能影响外部源码中依赖默认 dao 名称的引用，但该包受 internal 可见性约束，仓库内消费者必须全部同步。

### S03：限定范围精简注释

范围仅为 `internal/middleware/routetimeout.go`、`internal/router/init.go`、`internal/bootstrap/providers.go` 及本次真实改动的 Redis/cache/scheduler 函数旁说明。移除已删除的 RouteTimeout、TimeoutPolicy、StreamKeepAlive、routespec 自动约束等陈旧说明，改成当前 Timeout、StreamTimeout、RenewStreamDeadline 的真实责任；合并重复解释和无助理解的事故叙述。

保留协作取消不强停 goroutine、socket deadline 不保证 504、锁顺序、资源归属、失败策略、兼容时间窗等有效 why 注释。仅修改文字，不据注释直接改变行为；不把历史段落搬进新建框架文档。`/user` 作为普通示例路径不自动认定错误。

**验收 AS03**：逐块源码回读，注释引用当前存在的 API、约束归属真实；纯文字差异单独可审阅。此项本身不要求新增测试或运行任何检查。

## 5. 文件范围

以下为原定生产、文档及测试影响面。追加授权后已同步测试；新增用例的实际落点见第 9.3 节，未列出的旧测试保持原契约回归。JWT/限流结构体实际定义在 `internal/platform/config/types.go`；S01 改名和删除空栈后同步 `docs/architecture.md` 对应装配段落及标记表，不移动目录或改变架构。

| 阶段 | 生产与文档文件 | 测试文件与只读影响面 |
|---|---|---|
| 1 / R01 | `internal/pkg/jwtauth/laravel.go` | 新增 `internal/pkg/jwtauth/laravel_precision_test.go`；扩展 `internal/middleware/auth_laravel_test.go` |
| 2 / R02—R05 | `pkg/ratelimit/health.go`、`redis.go`、`policy.go`；`internal/platform/redis/open.go`；`internal/platform/config/ratelimit.go`；`internal/bootstrap/ratelimit.go`；`pkg/ratelimit/README.md`；`config/env.yml.example` | `pkg/ratelimit/redis_test.go`、`ratelimit_test.go`、`concurrency_test.go`、`health_notify_test.go`、`probe_cleanup_test.go`、`loopback_test.go`、`keyprefix_test.go`、`integration_test.go`、`script_lua_test.go`；新增 `pkg/ratelimit/cancellation_test.go`、`retry_contract_test.go`；`internal/platform/redis/contract_test.go`、`probe_test.go`；新增 `internal/platform/redis/deadline_test.go`；`internal/router/health_test.go`、`readiness_test.go`；`internal/platform/config/validation_test.go`；`internal/bootstrap/ratelimit_test.go` |
| 3 / R06 | `internal/pkg/jwtauth/assemble.go`、`keyset.go`、`rotating.go`；`internal/platform/config/jwt.go`、`validate.go`；`internal/bootstrap/runtime.go`；`config/env.yml.example`、`SCAFFOLD.md` | `internal/pkg/jwtauth/keyset_test.go`、`rotating_test.go`、`reencode_assembly_test.go`；新增 `internal/pkg/jwtauth/assemble_external_test.go`；`internal/platform/config/jwt_test.go`；`internal/bootstrap/assembly_contract_test.go`、`providers_token_test.go`；只读回归 cmd/jwt*、middleware、runtime/module/account、runtime/module/auth |
| 4 / R07—R08 | `internal/pkg/cache/cache.go`；`docs/cache.md` | `internal/pkg/cache/cache_test.go`、`panic_test.go`；新增 `internal/pkg/cache/load_contract_test.go` |
| 5 / R09—R10 | `internal/pkg/tasks/scheduler.go`；`internal/bootstrap/worker_scheduler.go`；`internal/platform/config/tasks.go` 的关停语义注释；`config/env.yml.example` 的同项说明；`docs/workers-and-scheduling.md` | `internal/pkg/tasks/scheduler_test.go`；新增 `internal/pkg/tasks/scheduler_shutdown_test.go`；`internal/bootstrap/worker_scheduler_test.go`、`workermanager_shutdown_test.go`；只读回归 singleton 与真实资源关闭安全门 |
| 6 / S01 | `internal/bootstrap/rollback.go`、`providers.go`；`internal/scaffold/module.go` 的对应函数说明；`cmd/MODULE_GENERATOR.md` 的对应说明 | `internal/bootstrap/rollback_test.go`、`assembly_contract_test.go`、`module_cleanup_test.go`；`internal/scaffold/module_test.go` |
| 6 / S02 | data 目录及其导入文件，详见下表 | 包声明/导入之外不改行为 |
| 6 / S03 | 第 4 节明确的注释范围 | 只回读差异，不新增测试 |
| 2、3、5 / 配置说明 | `config/env/env.yml` 中与 R05/R06/R09 对应的注释；仅在该阶段获准、安全核对后同步，不改变实际值 | 与 example 和结构体注释逐项比对；不回显敏感字段 |
| 所有阶段记录 | 本文 | 以后出现新的真实实施错误时，再按获准范围维护项目错误日志；本轮不修改 |

S02 精确范围：

- data 生产文件：`internal/platform/data/{resources,availability,init,tracing,logcaller}.go`。
- data 测试文件：`internal/platform/data/{native_logger,resources,redis_configured,init,gorm_logger_benchmark,gorm_logger_contract,availability,tracing}_test.go`。
- 导入文件：`internal/bootstrap/{resources_test,lifecycle_contract_test,ratelimit,runtime,providers,ratelimit_test,assembly_contract_test,health,resources}.go`；`internal/platform/redis/owner.go`；`internal/platform/mysql/owner.go`；`internal/platform/responsecache/owner.go`；`internal/platform/logging/hotpath_contract_test.go`。花括号仅为清单缩写，不是 shell 执行命令。

配置说明同步：R05、R06、R09 已同步 example/结构体说明。`config/env/env.yml` 属于历史环境的实际配置路径，当前项目目录没有该文件；本轮未创建配置或修改实际值。实际部署配置的兼容性核对仍由部署环境负责。

## 6. 分阶段实施步骤

所有阶段共用以下门槛：①当前阶段得到明确授权；②重新读取本文及最新规则/错误记录；③核对基线和文件归属；④预期文件范围与用户要求一致。任何一项未满足，停在只读/计划范围。每个步骤完成后记录真实文件差异、验证方法、命令/退出码/证据路径；未运行填“未执行”，不能勾选通过。

表中保留原实施顺序与验收方法；“当前状态”已按追加测试授权后的最终结果更新。具体命令、失败处理和验证限制见第 9.3 节。

| 阶段/步骤 | 前置条件 | 具体动作 | 验收方法与证据 | 停止条件/交付物 | 当前状态 |
|---|---|---|---|---|---|
| 0.1 | 仅文档授权 | 复核 13 项及实现边界，读取规则、错误记录与必要源码 | 逐项映射到第 3—5 节 | 缺关键事实时标明，不猜实现 | 已完成 |
| 0.2 | 0.1 完成 | 保存本文，逐段回读并纠正文档内冲突 | 完整文件读取记录，13 项对应表 | 交付本文；不进入阶段 1 | 已保存并完整回读 |
| 1.1 | 阶段 1 授权；接受 R01 整数表示边界 | 增加签名 token 与中间件 ID 传递用例 | A01；保留旧实现的真实结果 | 不同意表示范围收窄则修订方案，不改解析 | A01 用例已补齐；反证与 race 通过 |
| 1.2 | 1.1 用例就绪 | 修改 Laravel 数字解析与身份分支 | F01；逐个核对身份/时间/角色结果 | 任一合法整数映射改变则停止；交付 R01 差异和证据 | 29a58cd 实现已验收；A01/F01 通过 |
| 2.1 | 阶段 2 授权；最小 runner 兼容范围确认 | 给扣减命令加 NoRetry，适配仓库内限流 runner 及动态借用客户端的装配适配器 | A04、F02、F09；对端执行次数、恢复后客户端借用及 NoRetry 属性 | 不得退回全局禁重试作为临时补丁 | 测试桩已同步；丢回复/NOSCRIPT/真实 Redis 通过 |
| 2.2 | 2.1 接口确定 | 增加取消分类、准入凭证及同代探测完成逻辑 | A02；状态序列与调用次数 | 取消能恢复健康/释放他人探测时停止 | A02 状态、取消及代次回归通过 |
| 2.3 | 2.1—2.2 明确 | 统一 Redis context timeout，回审其他消费者 | A03、F03；实际 socket 等待结果 | 仅 mock 成功不能验收；缺真实条件标未验证 | A03 真实 socket/ready 及消费者回归通过 |
| 2.4 | 接受 R05 启动校验收窄 | 校验有效补满时间；统一 TTL 计算；同步说明 | A05、F02、F04；边界表及配置错误 | 不改实际策略数值规避失败；交付 R02—R05 整阶段差异 | A05 边界及启动拒绝通过；实际配置缺失 |
| 3.1 | 阶段 3 授权；阶段 1 完成 | 增加验签构造，保持签发构造严格 | A06 密钥集及 Issuer 错误用例 | 不得放宽 local 私钥/匹配校验 | A06 验签/签发模式及严格私钥回归通过 |
| 3.2 | 3.1 完成 | 按认证模式装配；修正 typed-nil；同步配置说明 | F05、F06；模式/路由/CLI 矩阵 | external 意外拥有 Issuer 或 local 接口退化则停止；交付 R06 | 装配、路由及 JWT CLI 通过；README 已同步 |
| 4.1 | 阶段 4 授权；复核历史 ERR-143 与当前源码差异 | 写失效提交交错、取消等待、panic/nil、名额用例 | A07/A08；确定性调度证据 | 不凭历史“已通过”跳过当前基线 | 交错、堆驻留、64 名额及广播用例已补齐 |
| 4.2 | 4.1 就绪 | 单登记项替换 DoChan；统一锁顺序与结果发布 | F07；同键一次加载、64/65 边界、无等待者列表 | 失效后旧值回填、死锁或名额提前释放则停止；R07/R08 一起交付 | A07/A08/F07 及反证、race 通过 |
| 5.1 | 阶段 5 授权；阶段 2 完成 | 收紧周期任务保证，并同步文档与错误文本 | A10、F08；声明枚举矩阵 | 不把 Fenced 自动替换成 Idempotent | A10 保证枚举及真实 UniqueTTL 通过 |
| 5.2 | 5.1 完成 | 跟踪真实退出、关闭超时观察、Cleanup 同实例等待 | A09、F08；两代实例数量、租约释放顺序 | 未退出即返回/重建/关共享资源则停止 | A09 两代、关闭超时、租约及 Cleanup 通过 |
| 5.3 | 5.2 完成 | 复核 WorkerManager 与装配层资源安全门 | F09；超时期间资源保持、晚退出后的清理 | 安全门不满足则停止并修订范围，不自行重写 WorkerManager；交付 R09/R10 | F09 阻塞关停和 App 资源安全门通过 |
| 6.1 | 阶段 6 授权；前五阶段完成；仍无模块登记项 | 删除空栈/空清理，移除同名测试包装，同步生成器描述 | AS01、F09、F10；真实清理顺序 | 一旦发现真实拥有资源的模块则停止此简化 | 旧测试已迁移；真实清理/生成器回归通过 |
| 6.2 | 6.1 完成；确认已有修改归属 | 改 data 包声明与列明的消费者导入 | AS02、F11、F12；导出契约与差异回读 | 目录/import 路径/导出接口变化则停止；交付 S02 | 测试包/消费者已同步；F11/F12 通过 |
| 6.3 | 6.2 完成 | 仅按 S03 修订过时注释 | AS03；逐块文字差异 | 不为注释清理改变运行逻辑；交付 S01—S03 | 限定注释已修订并回读 |
| 7.1 | 实现全部完成；普通验收获准 | 汇总 A01—A10、AS01—AS03；运行未完成的相关普通功能验收 | 第 7 节清单；已验证且代码未变的不重复跑 | 关键项缺证据时只能报“实现完成，验收未完成” | 相关 A/AS/F 验收完成；证据及范围见 9.3 |
| 7.2 | 7.1 完成；另行取得具体受控检查授权 | 仅运行明确获准的 lint/vet/modern/race/覆盖率/架构门禁 | 每项命令、版本、退出码、日志、未执行项 | 版本不符/既有失败分别记录；不升级工具、不降低门槛、不绕过 | 相关 build/vet/lint/race 通过；未运行项见 9.3 |

阶段 1、2、3、4、5、6 均可独立回滚；R02—R05 同阶段确认后再进入依赖其行为的阶段 5。R07 与 R08 共用状态结构，按一个回滚单元处理。阶段 7 不引入新的修复范围。

## 7. 验收入口与运行边界

### 7.1 普通功能验收入口

下表保留 F01—F12 的目的和入口。本次将相关包合并为 Linux `-race -tags=scaffold` 验证，vet 单独执行；JWT CLI 仍限于指定测试前缀。真实 Redis 验证使用本任务专用实例，结果见第 9.3 节；未连接现有业务服务，也未以跳过用例证明真实链路通过。

| ID | 计划命令 | 主要目的 |
|---|---|---|
| F01 | `go test -vet=off -count=1 -timeout=180s ./internal/pkg/jwtauth ./internal/middleware` | R01 身份、解析与中间件传递 |
| F02 | `go test -vet=off -count=1 -timeout=180s ./pkg/ratelimit` | R02/R04/R05 状态、重试、TTL 及 runner 适配 |
| F03 | `go test -vet=off -count=1 -timeout=180s ./internal/platform/redis ./internal/router ./internal/pkg/tokenstore` | R03 实际 deadline、ready 与 tokenstore 消费者回归 |
| F04 | `go test -vet=off -count=1 -timeout=180s ./internal/platform/config` | 模式与限流配置边界、example 约定 |
| F05 | `go test -vet=off -count=1 -timeout=240s ./internal/pkg/jwtauth ./internal/bootstrap ./internal/runtime/module/auth ./internal/runtime/module/account` | R06 装配、授权、local/external 隔离 |
| F06 | `go test -vet=off -count=1 -timeout=180s -run '^Test(Bare)?JWT' ./cmd` | 仅 JWT CLI，包含 TestJWT 和 TestBareJWT 前缀用例 |
| F07 | `go test -vet=off -count=1 -timeout=180s ./internal/pkg/cache` | R07/R08 并发交错、取消与 panic |
| F08 | `go test -vet=off -count=1 -timeout=240s ./internal/pkg/tasks ./internal/pkg/singleton` | R09/R10 调度、租约、保证声明 |
| F09 | `go test -vet=off -count=1 -timeout=240s ./internal/bootstrap` | 生命周期与真实资源清理 |
| F10 | `go test -vet=off -tags=scaffold -count=1 -timeout=240s ./internal/scaffold` | 原生成器契约与 S01 |
| F11 | `go test -vet=off -count=1 -timeout=240s ./internal/platform/data ./internal/platform/mysql ./internal/platform/redis ./internal/platform/responsecache ./internal/platform/logging` | S02 消费者及资源行为 |
| F12 | `go build ./...` | 最终普通构建；不能替代 A01—A10 |

F06 不运行 cmd 全包，以避免重新触发已记录的 `TestServerUsesDefaultConfigurationLoader` 真实服务启动问题（ERR-091/101/115）；这不是全仓测试通过声明。发生新的同类环境问题时保留证据并停止，不顺手改 server/config 回落逻辑。

### 7.2 并发、内存与性能证据

- 本次已针对包括 ratelimit/cache/tasks/bootstrap 在内的 18 个受影响包执行 race，覆盖本计划明确的交错；未声称全仓 race 或覆盖率结果。普通测试成功不证明没有 data race。
- R08 的机制目标：缓存条目数 ≤ maxItems；在途 loader 数 ≤ maxInflight（默认 64）；同键完成通知与结果存储为 1 份，不随历史已取消等待者总数增长。活跃调用者自己的 goroutine/栈不计作可消除的缓存内部常数内存。
- 已对同键阻塞 loader、三批各 10,000 个取消等待者采集 GC 后 HeapAlloc/HeapObjects，另记累计 TotalAlloc；并以临时保留逐等待者 channel 的实现反证。结果仅支持该受控场景的驻留契约，不推导生产 RSS 或内存降低比例。
- 复用 ERR-090 的教训：基准输出、解析警告、有效样本数都须核对；退出码 0 不自动使统计有效。本计划不新增无需求依据的全链路压测任务。

### 7.3 失败处理

失败时先保存真实现象、命令、环境、退出码与影响范围。仅修本阶段引入且获准范围内的问题；同一问题连续三轮仍不清楚则停止汇报。缺本机依赖、工具版本不符、旧失败、新回归分别记录。禁止通过忽略错误、删除关键断言、降低覆盖率或修改业务配置使检查“变绿”。

## 8. 兼容性、风险与回滚

### 8.1 必须显式接受的兼容性变化

| 项 | 变化 | 发布/回滚注意 |
|---|---|---|
| R01 | 数字 sub 只接受整数字面量，拒绝小数/指数表示；大整数精确解析 | 先核对上游 token 表示；不得以回滚重新信任被舍入的用户身份 |
| R04 | 自定义 runner 需实现 Process；结果不明不再自动重扣 | 调用者可能更早看到真实错误；不要通过全局开启重试掩盖 |
| R05 | Redis 策略补满加余量 >24h 将启动失败 | 实际部署策略未确认；不自动改额度；回滚不能消除旧版本的过早满额风险 |
| R06 | external 不再有签发器；不读取私钥 | 旧二进制仍要求私钥。公钥-only 部署不能直接回退旧版后假定能启动；保留本版已验证修复制品作为恢复点，不能为了回退随意给验签实例分发私钥 |
| R09 | 关闭超时后保持 worker 未退出状态 | 可能暴露过去被隐藏的停机不完整；不通过提前释放共享客户端使退出看似成功 |
| R10 | 周期任务 Fenced 与非法保证被拒绝 | 未来接入方需提供真实业务幂等；不自动改声明 |
| S02 | 包默认名称从 dao 变成 data | 仓库内同步；import 路径与导出 API 保持不变 |

### 8.2 风险控制与负责人

代码实施者负责每阶段差异、回归和证据；用户负责阶段批准与兼容性取舍。实际上游 JWT 提供方、部署配置负责人、上线负责人和日期均未指定，保持未确认；不虚构已沟通或容量已确认。

重点风险：R02 凭证处理错误造成探测卡住；R04 只测 stub 未覆盖真实重试；R05 校验与 Lua 的舍入口径不一致；R06 typed-nil 或意外放宽 local；R07/R08 锁顺序反转、panic 不广播；R09 无界实例重建或错误释放共享资源。分别由 A02、A04、A05、A06、A07/A08、A09 验证，未通过不得进入该项交付。

### 8.3 回滚操作

1. 每阶段开始前保存当时基线与本阶段差异清单；回滚仅逆向撤销该阶段自己的 hunks，保留用户和其他会话改动，不重置仓库。
2. 回滚生产代码时同步回滚对应测试、example 与文字，避免说明与行为不一致；R07/R08 一起回滚，S02 的包声明与全部消费者一起回滚。
3. 本方案没有数据库 schema 或消息载荷迁移。不得通过删库、清空 Redis、删除队列或批量刷新 token“恢复基线”。不确定是否已扣减的 R04 请求不能自动补 token；历史错误身份访问不能靠代码回滚追溯消除。
4. 上线尚未授权。未来发布先在可控环境验证，再按真实部署拓扑安排实例替换；必须先确认 R01/R05 配置兼容和 R06 恢复制品。没有部署事实，不编造灰度比例、观察时长或“10 分钟一定可回滚”。
5. 未来发布一旦出现身份映射错误、重复扣减、失效后旧值回填、旧调度器未退出就新建实例，立即停止扩大部署并按对应单元恢复已验证制品；恢复后在同样条件复核。不能把恢复旧漏洞当作风险已经消除。

## 9. 计划轮历史记录与本轮实施状态

- 已读取全局规则中的拒绝猜测与历史错误要求、项目 AGENTS、相关 guides、项目错误记录、必要源码和锁定依赖源码。
- 本轮只完成方案准备；未修改代码/实际配置/依赖，未运行任何功能或质量检查，未部署。
- 已读取的既有 `docs/confirmed-review-defects-plan-2026-10-23.md` 属于另一组历史问题；本文不复用其编号、批准状态、change 或执行结果。
- 运行时报告 OpenSpec CLI `--version` 探测超过 2s，状态未读取、未变更；本会话 workflow status 可读，但将本次仅计划请求标为 code。该分类不能覆盖用户“先不要改代码或运行检查”的明示边界。
- 本会话按运行端要求通过 `workflow.py read` 完整读取本文、AGENTS.md 与架构 Guide，均 exit 0。随后一次 `activate --plan docs/review-remediation-plan-2026-10-23.md` 返回 exit 1，原始错误为 `[workflow] OpenSpec CLI state could not be read:   simplify-startup-banner`。没有选择该历史 change，没有激活成功记录，没有执行测试或修改产品代码；本轮不继续诊断或绕过门禁。
- 为后续实施预留独立 change 名 `repair-audit-safety-and-simplification`，仅为待创建名称；本轮没有创建、验证或归档 change。使用时先确认无其他会话占用，不能选择历史 change。
- harness 提示项目模板 `09972a20a8ef` 落后于模板仓库 `db2e0cdfa25b`。已提醒可用 `go-harness update` 或 `go-harness --force`；本轮不执行更新。ERR-148 记载过更新冲突，后续不能据提醒自动覆盖本地规则。
- 完整回读状态：已完成；已核对 10 个问题、3 个简化项、阶段依赖、文件范围、兼容变化、回滚及验收一一对应。阶段 1—7：均未授权、未开始；所有 A/AS/F 验收项：未执行。读取成功仅证明文档已核对，不证明代码修复或运行期安全。

### 9.1 生产实施授权历史（后续测试授权见 9.3）

配置说明补充落点：`config/README.md` 的 JWT 启动校验段仍声明两种模式都需要私钥。本轮随 R06 同步该段与权限表的适用模式，保留 doctor/CLI 原行为说明；属于已批准的配置说明同步，不扩展运行代码范围。

- 用户已批准阶段 1—6 的全部生产实现、相应说明和本计划状态更新，并接受第 8 节列明的兼容性取舍；部署仍未授权。
- 原阶段 1.1、4.1 的测试编写，以及各步骤中的测试增删/适配和阶段 7 的全部检查均延期。原生产步骤的测试前置条件本轮替换为源码契约核对；编号、实施先后和验收目标不变。
- 不新增、修改或删除 `_test.go`。S01 的旧测试包装、S02 的测试包声明，以及依赖旧 runner/私有签名的测试适配也延期；不得为旧测试保留无用生产封装。由此存在测试源码尚未同步的已知限制，本轮不能声称可编译或测试通过。
- 所有 A/AS/F 验收项仍未执行；本轮仅可报告代码已写入并回读，不报告性能提升、并发安全证明或发布准入。
- 已通过本会话的 OpenSpec 包装入口读取列表（exit 0），并创建独立变更 `repair-audit-safety-and-simplification`（exit 0），未使用任何历史 change。
- 上述授权记录写入时，阶段 1—6 的生产步骤为“已授权、尚未开始”；2026-10-24 的实际进度见第 9.2 节。阶段 7 及所有测试编写继续按用户指令延期。

本节记录当时边界；后续追加指令已授权测试编写及必要验证，最终范围以第 9.3 节为准。

### 9.2 2026-10-24 生产续作历史（追加测试前）

用户指令：“根据最后一次提交信息和文档 docs/review-remediation-plan-2026-10-23.md，继续做未完成的修复”。沿用第 9.1 节生产实现授权及测试/检查延期边界，不创建 worktree、不提交或推送、不部署。最后提交为 `29a58cdea62cdbad4a03696edf15f052c4703c9a`；提交说明与实际源码共同作为续作基线，不仅凭提交标题判断完成项。

| 范围 | 本次核对或写入结果 | 验收状态 |
|---|---|---|
| R01 | `29a58cd` 已启用 JSON Number，数字 sub 通过 Int64 精确解析，移除 float64 身份分支 | A01/F01 未执行 |
| R02—R05 | `29a58cd` 已有取消分类、健康代次凭证、NoRetry 扣减命令、动态 Process 转发、context timeout 与慢桶 TTL 校验；本次回读相关生产路径 | A02—A05/F02—F04 未执行 |
| R06 | `29a58cd` 已有仅验签 KeySet、按模式加载及 nil Issuer；本次补齐 `config/README.md` 的模式、权限及 CLI 边界说明 | A06/F05/F06 未执行 |
| R07/R08 | `cache.go` 改用泛型 loadEntry，每键一份结果和完成通知；取消等待不登记额外 channel。加载准入二次核对缓存；失效、归属校验及提交遵循 loadMu → mu，时钟与 jitter 在锁外执行；错误/panic 不缓存，名额留到 loader 真实退出；同步 `docs/cache.md` | 生产实现已写入；A07/A08/F07 及驻留证据未执行 |
| R09 | 显式 Start/Shutdown；首次超时立即观察并保留错误，再由同一 Run goroutine 等待同一实例。实例/代次由锁保护，Cleanup 超时不清除归属，原 token 在真实退出及续租结束后释放；补齐观察日志、配置及生命周期说明 | 生产实现已写入；A09/F08/F09 未执行 |
| R10 | 周期任务只接受 GuaranteeIdempotent，明确拒绝零值、Fenced 与未知枚举；一般 singleton 的 Held.Fence 能力保留，UniqueTTL 仍只作附加去重 | 生产实现已写入；A10/F08 未执行 |
| S01 | 已确认没有生产模块 push，删除 rollbackStep/rollbackStack、services.cleanup 和空 modules 清理相位；生产入口改为 buildServices。真实 providerCleanup、回滚预算、逆序回滚与正常关闭阶段保留；同步生成器及架构说明 | 生产实现已写入；旧测试删除/迁移与 F09/F10 延期 |
| S02 | data 目录五个生产文件改为 package data，八个生产消费者去掉冗余显式导入别名；不移动目录，不改导入路径或导出 API | 生产实现已写入；八个测试包声明及五个测试导入的同步延期；F11/F12 未执行 |
| S03 | 限定文件删除 RouteTimeout/TimeoutPolicy/StreamKeepAlive/routespec 残留说明，压缩 router/providers/cache/scheduler 的历史叙述，保留取消、socket deadline、锁顺序、所有权和兼容窗口说明 | 文字已写入；仅源码回读，未执行检查 |

源码调用方回读：`GetOrLoad/Invalidate` 未改公开签名；现有仓库未发现通用 cache 的生产业务接入。Scheduler 经 `asynqSchedulerWorker` 转发生命周期，装配入口和 PeriodicTask 载荷不变。`WorkerManager.StopKind` 超时后保留 stuck 条目，`cleanupPrepared` 跳过未退出任务，supervise 在 Run 返回后执行 Cleanup；`App.closeExtensions/closeResources` 对仍存活 worker 保留扩展和共享资源。本次未重写这些安全门，运行期结论仍需 A09/F09 验证。`buildServices` 的生产调用点和生成器说明已改名，测试包装按授权延期。

独立只读复审已结束，未发现本轮范围内新的高置信度可操作缺陷。复审覆盖缓存锁顺序与发布、实际 asynqx v1.6.0 停止契约、排空超时错误保留、同代清理和真实 provider 所有权。复审未修改文件，也未运行测试、构建或其他检查；此结果不能替代上述验收项。

本机范围差异：当前目录不存在仓库 `AGENTS.md`、`.harness/` 或 `openspec/`，故使用用户本轮提供的目录规则及现有 `docs/architecture.md`，没有创建或激活替代流程。历史错误日志在本机不可读，不将历史通过结果作为证据。`config/env/env.yml` 也不存在，本机实际配置说明未同步；仅更新 example 与结构体注释，不创建配置文件、不读取或回显凭据、不改变任何实际值。

已知未闭环项：本轮没有新增、修改或删除任何 `_test.go`。旧 scriptRunner 测试桩仍需适配 Process，旧 JWT 私有签名/配置断言仍需适配；`assembly_contract_test.go` 中同名 buildServices 包装及 svc.cleanup 调用、rollback/module_cleanup 测试中的已删除类型/相位仍需迁移，data 测试仍为 package dao。这些是尚未同步的源码限制，不能把当前状态宣称为可构建、测试通过或发布就绪。

本轮只运行读取、检索与差异回读命令并写入以上生产/说明文件；没有运行测试、构建、lint、vet、modern、race、coverage、架构或其他门禁。A01—A10、AS01/AS02 的运行验收、F01—F12、并发/内存证据均未执行，AS03 仅完成文字回读。不提供性能或并发安全证明。所有修改保留在当前项目工作区，后续先按已批准清单完成测试源码适配，再在获准范围内执行阶段 7。

### 9.3 追加测试授权与最终执行记录

用户最新指令：“测试文件也可以一起写了，进行该有的测试”。现同步延期测试，并执行本次修复所需的定向功能、构建和并发 race 等验证。既有目录、依赖和无提交/推送/部署约束不变，不连接现有业务数据库或 Redis；测试所需受控回环服务由各用例自行启动和清理。

初次 `go test -vet=off -count=1 -timeout=240s ./internal/bootstrap ./internal/pkg/tasks`：exit 1。tasks 原有测试通过；bootstrap 编译失败于旧 cleanupModules/rollbackStack 和 redisCommands.EvalSha 引用，属于此前明确延期的适配。本轮同步这些测试，不为旧测试恢复已删除生产封装。

**最终结果：本次相关功能与并发验收通过。** R01—R06 的已有实现已补回归；R07—R10、S01—S03 的实现和测试已同步。数据测试包声明改为 `data`，bootstrap 的旧空模块回滚用例改为真实 provider 清理断言，限流测试桩改用 Process，签发测试显式使用 local 模式。保留真实资源清理顺序、失败继续清理及重复关闭断言。

实际新增测试落点与覆盖：

| 验收项 | 测试落点及关键断言 |
|---|---|
| A01 | `internal/pkg/jwtauth/laravel_precision_test.go`、`internal/middleware/auth_laravel_test.go`：原始 JSON 实签，正 int64/2^53 边界、非法表示及签名、parser 选项覆盖，精确 ID 到达用户校验器和 handler |
| A02 | `pkg/ratelimit/health_cancellation_test.go`：纯取消与混合错误树、连续失败阈值、探测间隔、旧代次结果、请求预取消及本地影子额度 |
| A03 | `internal/platform/redis/deadline_test.go`、`internal/router/redis_deadline_test.go`：真实 TCP 握手后阻塞业务回复，DB0/DB3 的 100ms deadline 在 500ms 内返回，readiness 的 2s 预算在 2.5s 内返回；无 deadline 时仍受 socket timeout 约束 |
| A04/A05 | `pkg/ratelimit/retry_contract_test.go`：服务端执行后丢回复只扣一次、同客户端普通 GET 仍重试、明确 NOSCRIPT 仅加载后再执行、24h/60s 余量与定点舍入边界；bootstrap/config 断言非法 Redis 策略启动失败而 local 策略可用 |
| A06 | `internal/pkg/jwtauth/assemble_external_test.go`、既有 config/bootstrap/middleware/模块用例：external 忽略空/缺失/非法私钥路径、active/previous 验签、Issuer 真 nil、local 私钥缺失/错配/权限仍失败、错误 previous/重复 kid 仍失败 |
| A07/A08 | `internal/pkg/cache/load_contract_test.go`：loader 返回至提交之间失效、同键 100 等待者共享成功/错误/panic/typed nil、三批各 10,000 次取消的堆驻留、64 个真实 loader 占满默认名额且忽略取消期间不提前释放 |
| A09/A10 | `internal/pkg/tasks/scheduler_shutdown_test.go`、`internal/bootstrap/worker_scheduler_shutdown_test.go`：首轮超时只观察一次且继续跟踪、失租后旧代退出才启新代、同代 Cleanup/晚清理、原租约释放、App 延后关闭扩展/Redis；保证枚举白名单、真实队列入队及 UniqueTTL=7s |
| AS01—AS03 | bootstrap/data/资源消费者/scaffold 的既有测试回归、包/导入同步、生成器契约及限定注释回读；真实 provider 清理路径保留 |

最终命令和证据（相对路径均位于当前项目）：

| 环境/命令 | 结果 | 证据 |
|---|---|---|
| Windows `go build ./...` | exit 0 | `.cache/review-remediation-2026-10-24/windows-build-final.{log,exit}` |
| Windows `go vet -p=4 -tags=scaffold ./...` | exit 0 | 同目录 `vet-final.{log,exit}` |
| Windows golangci-lint v2.15.0 `run --concurrency=4 --timeout=10m ./...` | exit 0，0 issues；未修改 lint 配置或排除规则 | 同目录 `lint-final.{log,exit}` |
| Linux Go1.27.3 `go build -p=4 -tags=sonic ./...` | exit 0 | 同目录 `linux-build-final.{log,exit}` |
| Linux `go test -p=4 -race -vet=off -tags=scaffold -count=1 -timeout=240s -json` 加下述 18 包 | exit 0；18 包、784 个顶层测试、1948 个含子用例；0 失败/跳过/race 警告 | 同目录 `linux-race-final.{jsonl,exit}` |
| Linux `go test -p=4 -race -vet=off -tags=integration -count=1 -timeout=180s -run '^TestIntegration' -json ./pkg/ratelimit ./internal/pkg/tokenstore ./internal/platform/responsecache` | 3 包 8 项通过，0 失败/跳过/race 警告；连接本任务专用真实 Redis DB15 | 同目录 `redis-consumers-final.jsonl` |
| Linux `go test -p=4 -vet=off -tags=scaffold -count=1 -timeout=180s -run '^Test(Bare)?JWT' -json ./cmd` | 12 个顶层测试、27 个含子用例通过，0 失败/跳过 | 同目录 `jwt-cli.jsonl` |
| Linux `go test -race -vet=off -count=10 -run '^TestRecoveryOutcomeLogs$' ./internal/platform/redis` | exit 0；测试计数器修正后连续 10 次通过 | 同目录 `redis-recovery-race-green.log` |

18 包为 `internal/pkg/{jwtauth,tokenstore,cache,tasks,singleton}`、`internal/middleware`、`pkg/ratelimit`、`internal/platform/{config,redis,data,mysql,responsecache,logging}`、`internal/router`、`internal/bootstrap`、`internal/runtime/module/{auth,account}` 和 `internal/scaffold`。这组 race 覆盖 F01—F05、F07—F11 的包范围；F06 单独执行，F12 为最终普通构建。

驻留证据：Linux race 最后一轮的三批取消完成并 GC 后，新增存活 HeapAlloc 分别为 305336/306336/306320 字节，HeapObjects 为 1135/1142/1143；累计 TotalAlloc 为 3147680/5870136/8592904 字节。分配累计增长符合调用发生，存活量未随累计取消次数持续增长。该测试不使用 RSS 证明无泄漏，也不据此宣称生产内存改善比例。

最小反证均在当前源码上临时修改目标点、运行定向测试，并在 finally 中恢复原始文件字节；没有保留反证实现或复制项目：

- 去掉 fenced 提交保护，交错测试捕获旧值回填（`cache-fence-red.log`）。
- 人工保留逐等待者 channel，堆驻留测试捕获三批存活量持续增长（`cache-waiters-red.log`）。
- 首次 Shutdown 超时即返回，装配测试捕获 StopKind 过早确认退出（`scheduler-drain-red.log`）。
- 取消扣减 NoRetry，TCP 对端记录 4 次执行，断言要求 1 次（`no-retry-red.log`）。
- 去掉 JSON Number，2^53+1 的两种 parser 选项路径失败（`laravel-number-red.log`）。

失败处理与复审：第一次 Linux race 在既有 `TestRecoveryOutcomeLogs/recovered` 发现测试计数器 `calls` 的并发读写；已仅将该计数器改为 atomic.Int32，保留原断言，定向 10 次与最终 18 包 race 均通过。lint 首次运行有超时和局部问题；修正等价表达式、错误检查和测试辅助类型归属后重跑为 0 issues。`redisView` 的使用者是 integration 文件，因此将其移入该标签文件，原断言和真实 Redis 回归保留。独立只读复审指出的 64 名额实际计数、失租后第二代和 UniqueTTL 三处覆盖缺口均已补齐，未留下已知未处理发现。

环境和范围限制：

- Windows 普通 JWT/config 测试分别遇到既有符号链接权限和绝对路径断言限制；没有添加兼容补丁或跳过用例，Linux 同包完整测试已通过。
- Linux 使用本机缓存镜像、同版 Go1.27.3 工具链和 gcc，当前项目只读挂载到 `/workspace`，缓存单独可写；仅解压工具链，没有源码副本或替代工作树。最初共享目录工具链构建过慢而终止（143），未记为通过。
- 真实 Redis 仅使用本次专用容器和网络，未暴露宿主端口，DB15 无业务数据；两只测试容器、所属匿名卷和专用网络已清理（`docker-cleanup.{log,exit}`，exit 0），未连接或停止既有 devilbox 服务。
- 未运行全仓 cmd/server 测试、真实 MySQL 集成、覆盖率、全链路压测、modern/架构聚合脚本或安全扫描；本次没有依赖、数据库结构或消息协议变更，不将相关包测试结果扩展成全仓、生产容量或发布准入结论。
- 实际 `config/env/env.yml` 缺失，未创建或改变配置；仅同步 example、结构体及 README。`go.mod`/`go.sum` 无差异，源码、测试和文档都保留在 `D:\go\gin_api` 工作区，未执行提交、推送、合并、打标签或部署。

## 10. 本轮全部生产阶段的读取契约

以下保留 2026-10-23 的生产读取契约，不代表本机已执行该工作流。它没有纳入后续测试授权，不能覆盖第 9.3 节的最新范围；本机缺少其 required_context 及 OpenSpec 状态的事实见第 9.2 节，不据此重建或激活历史流程。

<!-- harness-contract:v1 -->
```json
{
  "schema_version": 1,
  "stage": "all-production-stages",
  "change": "repair-audit-safety-and-simplification",
  "allowed_files": [
    {
      "path": ".harness/error-journal.md",
      "kind": "document"
    },
    {
      "path": "internal/pkg/jwtauth/laravel.go",
      "kind": "code"
    },
    {
      "path": "pkg/ratelimit/health.go",
      "kind": "code"
    },
    {
      "path": "pkg/ratelimit/redis.go",
      "kind": "code"
    },
    {
      "path": "pkg/ratelimit/policy.go",
      "kind": "code"
    },
    {
      "path": "internal/platform/redis/open.go",
      "kind": "code"
    },
    {
      "path": "internal/platform/config/ratelimit.go",
      "kind": "code"
    },
    {
      "path": "internal/platform/config/types.go",
      "kind": "code"
    },
    {
      "path": "internal/bootstrap/ratelimit.go",
      "kind": "code"
    },
    {
      "path": "internal/pkg/jwtauth/assemble.go",
      "kind": "code"
    },
    {
      "path": "internal/pkg/jwtauth/keyset.go",
      "kind": "code"
    },
    {
      "path": "internal/pkg/jwtauth/rotating.go",
      "kind": "code"
    },
    {
      "path": "internal/platform/config/jwt.go",
      "kind": "code"
    },
    {
      "path": "internal/platform/config/validate.go",
      "kind": "code"
    },
    {
      "path": "internal/bootstrap/runtime.go",
      "kind": "code"
    },
    {
      "path": "internal/pkg/cache/cache.go",
      "kind": "code"
    },
    {
      "path": "internal/pkg/tasks/scheduler.go",
      "kind": "code"
    },
    {
      "path": "internal/bootstrap/worker_scheduler.go",
      "kind": "code"
    },
    {
      "path": "internal/platform/config/tasks.go",
      "kind": "code"
    },
    {
      "path": "internal/bootstrap/rollback.go",
      "kind": "code"
    },
    {
      "path": "internal/bootstrap/providers.go",
      "kind": "code"
    },
    {
      "path": "internal/scaffold/module.go",
      "kind": "code"
    },
    {
      "path": "internal/platform/data/resources.go",
      "kind": "code"
    },
    {
      "path": "internal/platform/data/availability.go",
      "kind": "code"
    },
    {
      "path": "internal/platform/data/init.go",
      "kind": "code"
    },
    {
      "path": "internal/platform/data/tracing.go",
      "kind": "code"
    },
    {
      "path": "internal/platform/data/logcaller.go",
      "kind": "code"
    },
    {
      "path": "internal/bootstrap/health.go",
      "kind": "code"
    },
    {
      "path": "internal/bootstrap/resources.go",
      "kind": "code"
    },
    {
      "path": "internal/platform/redis/owner.go",
      "kind": "code"
    },
    {
      "path": "internal/platform/mysql/owner.go",
      "kind": "code"
    },
    {
      "path": "internal/platform/responsecache/owner.go",
      "kind": "code"
    },
    {
      "path": "internal/middleware/routetimeout.go",
      "kind": "code"
    },
    {
      "path": "internal/router/init.go",
      "kind": "code"
    },
    {
      "path": "docs/review-remediation-plan-2026-10-23.md",
      "kind": "document"
    },
    {
      "path": "pkg/ratelimit/README.md",
      "kind": "document"
    },
    {
      "path": "config/env.yml.example",
      "kind": "document"
    },
    {
      "path": "config/env/env.yml",
      "kind": "document"
    },
    {
      "path": "SCAFFOLD.md",
      "kind": "document"
    },
    {
      "path": "config/README.md",
      "kind": "document"
    },
    {
      "path": "docs/cache.md",
      "kind": "document"
    },
    {
      "path": "docs/workers-and-scheduling.md",
      "kind": "document"
    },
    {
      "path": "cmd/MODULE_GENERATOR.md",
      "kind": "document"
    },
    {
      "path": "docs/architecture.md",
      "kind": "document"
    }
  ],
  "required_context": [
    {
      "path": "AGENTS.md",
      "kind": "rules",
      "name": "project-rules"
    },
    {
      "path": ".harness/guides/architecture.md",
      "kind": "guide",
      "name": "architecture"
    }
  ],
  "checks": [
    {
      "id": "laravel-identity",
      "kind": "functional",
      "argv": [
        "go",
        "test",
        "-vet=off",
        "-count=1",
        "-timeout=180s",
        "./internal/pkg/jwtauth",
        "./internal/middleware"
      ],
      "timeout_seconds": 240
    }
  ],
  "regressions": []
}
```
