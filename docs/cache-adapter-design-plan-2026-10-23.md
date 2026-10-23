# internal/pkg/cache 可插拔适配器方案与实施计划

日期：2026-10-23（Asia/Taipei）  
状态：已落盘并回读，待用户审阅；仅本轮文档准备已获授权，P1 及以后均未获实施授权。  
工作流会话：`01a16786-0c0d-7e73-8258-d9e85eb2afcb`  
本次独立 change：`add-pluggable-cache-adapters`；不接手其它 change。  
基线提交：`109ccd47432d9339beaad2e2e83ff9ecd23eeace`；工作区存在其它任务的未提交改动，不能当作干净基线。

## 1. 需求来源与授权边界

用户原文：

> 先给 internal/pkg/cache 设计可插拔适配器的完整方案和实施计划，保留现有功能，支持后续接入 FreeCache、BigCache 或 Ristretto，暂时不改代码。

本轮只读取必要材料并保存本方案及对应的设计制品。**没有修改 Go 代码、依赖、配置或既有缓存说明，没有运行测试、build、vet、lint、modern、race、覆盖率或性能测试。**文中的接口、文件及测试均是拟实施内容，不是已经存在的能力。

计划审批与实施审批分开：读完或接受设计不自动授权 P1–P5；选定某个第三方库也不自动授权其它两个库。工作流保存/绑定只登记计划，不扩大用户的本轮授权。

## 2. 已核实事实与现状

### 2.1 文件、依赖和调用边界

| 材料 | 本轮读到的事实 |
|---|---|
| `internal/pkg/cache/cache.go` | 泛型 `Cache[T]`；存储与回源治理在同一结构中；无后端接口、后端注入入口或 Close 方法 |
| `internal/pkg/cache/cache_test.go` | 15 个现有测试，覆盖构造约束、合并加载、命中、错误、LRU、TTL、抖动、取消、超时、在途限制、失效 |
| `internal/pkg/cache/panic_test.go` | 2 个现有测试；子进程检查 loader panic 不终止进程；检查共享错误、错误原因和名额恢复 |
| `openspec/specs/cache-preset/spec.md` | 要求保留 typed GetOrLoad、singleflight、TTL 抖动、有界容量、独立加载超时、提前准入和失效屏障 |
| 当前源码引用搜索 | 对 `internal` 下 Go 文件搜索该包完整 import 路径和 `cache.New[`，未找到包外调用；仅为文本检索证据，不宣称覆盖仓库外消费者或所有动态用法 |
| `internal/pkg/httpcache/cache.go` | 另一套 HTTP 响应缓存；其 memory 分支已接入 gincache 的 Ristretto 适配器，不是本包的后端 |
| `go.mod` | `go 1.27.3`；`golang.org/x/sync v0.24.0`、`github.com/gtkit/goerr v1.5.0`、`github.com/gtkit/logger/v2 v2.5.0`；Ristretto 为 `github.com/dgraph-io/ristretto/v2 v2.4.2` |
| 第三方依赖 | FreeCache、BigCache 当前没有出现在直接依赖中；本方案不添加它们。未来选型时核实具体稳定版本与 Go 兼容性，不提前虚构版本 |

### 2.2 必须逐项保留的行为和外部耦合

| 编号 | 当前事实／约束 | 拟处理方式 |
|---|---|---|
| C01 | `New[T](maxItems int, ttl time.Duration, opts ...Option[T]) (*Cache[T], error)` | 签名、调用方式不变；仍创建默认 LRU |
| C02 | `Option[T]`、`WithClock`、`WithJitterRatio`、`WithLoadTimeout`、`WithMaxInflight` | 保留符号、签名及已存在的有效值／忽略规则；不借重构重定默认值 |
| C03 | `maxItems <= 0`、`ttl <= 0` 构造失败 | 默认构造保持；新入口同样拒绝非正 TTL |
| C04 | 默认抖动 ±10%、加载超时 30s、最多 64 个不同在途键 | 保持，不增加全局配置项 |
| C05 | key 是原样的 `string`；没有本包自带前缀、租户键或 Redis 库号 | 原样透传；本轮不增加命名空间、连接、库号或远端交互 |
| C06 | 默认值直接保存 `T`，没有序列化、深拷贝 | 默认路径保留；指针、切片等引用值仍要求调用方自行保证共享访问安全 |
| C07 | 同键在飞调用共享 loader 结果；不同键分别加载 | 仍由外层 singleflight 和登记表控制，不转交供应商库实现 |
| C08 | 加载名额在 `DoChan` 创建 goroutine 之前判断；同键加入不额外占名额 | 保持；后端拒绝写入也必须释放原有名额 |
| C09 | 缓存命中先于 `ctx.Err()` 判断 | 保持：已取消的调用仍可能得到已缓存值；未命中且已取消则不启动 loader |
| C10 | `WithoutCancel` 保留调用上下文值；独立超时控制 loader；单个等待者取消不取消共享加载 | 保持；不宣称能强制中断忽略 ctx 的 loader，它仍占名额至实际退出 |
| C11 | loader 返回错误不缓存；成功填充时才开始计算 TTL | 保持；不新增负缓存、错误缓存或 stale-while-revalidate |
| C12 | TTL 到点（`now >= expiresAt`）即不命中；命中不续期；默认过期访问会删除条目 | 由外层统一判断逻辑有效期；默认 LRU 的惰性删除和容量释放保持 |
| C13 | 默认按条目数限制、精确 LRU；`Len()` 包含尚未惰性删除的过期条目 | 默认路径完全保留；不把它等同于进程内存硬上限 |
| C14 | `Invalidate(key)` 无返回值；删除不存在的键安全 | 保留；另加可观察错误的入口，不强制迁移调用方 |
| C15 | 失效阻止在途旧值回填，不取消 loader，也不撤回它对已有等待者的结果 | 保持边界；失效后仍在同一 flight 内的调用可能共享旧结果，不承诺读己之写或跨进程强一致 |
| C16 | `ErrLoadsSaturated`、`ErrLoaderPanic` 及错误 panic 的 cause 链 | 保留 `errors.Is` 行为；panic 文案不泄漏原值；继续使用现有 goerr/logger |
| C17 | 无后台清扫器、无网络、无数据库事务、无配置加载和应用自动装配 | 基础改造继续保持；只为后续本地缓存引擎提供接口 |
| C18 | `finishLoad` 删除登记并 Forget，避免登记与 singleflight 生命周期错位 | 保留，成功、拒绝、错误、panic 路径都要覆盖 |

以上清单来自实际源码、测试与规格，不把测试文件存在当作本轮测试通过。

### 2.3 当前实现不能冒充已验证的边界

- G1：`cache.go` 在读出 `state.fenced` 后解锁，再调用 `set`；`Invalidate` 也分开标记与删除。静态时序允许“检查 false → Invalidate 标记并删除 → 旧值 set”。现有测试覆盖“先失效、后释放 loader”，未封住这个更窄窗口。本轮没有运行复现；P1 必须先用明确屏障形成反例，P2 在本包内将失效与提交串行化。
- G2：singleflight 结果直接断言为 `T`；`T=any` 且 loader 返回真正的 nil 接口时，断言存在失败路径。当前未做运行验证。P1 记录现象；P2 用带类型的内部结果封装承载值，不用 nil 代表 miss。该项属于新后端通用结果契约的必要边界，不扩展到其它包。
- QPS、真实 value 分布、命中率、可接受延迟与进程内存预算未提供；不据此推荐三个库中的“最快者”，也不写性能提升百分比。

## 3. 目标与非目标

目标：旧调用无需迁移；默认 LRU 和 17 个现有测试继续成立；新增一个显式注入入口；用同一组契约用例验证不同后端；明确错误、关闭与异步写入边界，为将来单独选择一个库准备完整接法。

本轮及基础改造不做：实际接入三个第三方库、运行时热切换、修改目录层级、拆独立 module、引入插件注册表、配置后端名称、复用 HTTP CacheStore、Redis/L1-L2 联动、自动负缓存、增加 metrics 服务、部署或性能承诺。

## 4. 备选与决策

| 方案 | 正确性和兼容性 | 依赖／改动面 | 取舍 |
|---|---|---|---|
| A：直接将内部 map 换成某个库 | 引擎语义直接进入 GetOrLoad；后续换库仍要改主逻辑 | 现在就增加选定库；影响默认路径 | 不采用，用户尚未选库 |
| B：`Cache[T]` 保留治理，抽取类型化存储后端 | 默认存储可行为等价迁移；新语义集中在小契约 | 基础阶段不新增依赖；仅原包内文件 | **采用** |
| C：全部改用 gincache 的 `persist.LocalStore` | 面向 `any` 与编解码；本包默认直接保存 T 的语义会改变 | 业务缓存反向绑定 HTTP 缓存库的接口；额外迁移 | 不采用 |

不同时建立两套“后端接口＋插件框架”；不强迫默认 LRU 经过 JSON 或字节缓冲。基础阶段唯一实现是原有 LRU 的适配结果。

## 5. 拟新增接口与职责

以下是待实现的 Go 契约草案，尚未编译或落入源码：

```go
// Record 是缓存内部传递的值和逻辑过期点。
type Record[T any] struct {
    Value     T
    ExpiresAt time.Time
}

// Backend 只承载本地存储；一个实例仅交给一个 Cache 使用。
type Backend[T any] interface {
    Get(key string) (record Record[T], found bool, err error)
    Set(key string, record Record[T]) (stored bool, err error)
    Delete(key string) error
    Len() int
}

func NewWithBackend[T any](backend Backend[T], ttl time.Duration,
    opts ...Option[T]) (*Cache[T], error)

func (c *Cache[T]) InvalidateErr(key string) error
```

### 5.1 构造和范围

- `New` 仍接收 `maxItems`，创建未导出的 `lruBackend[T]`，然后复用治理层初始化；不导出多余的默认后端工厂。
- `NewWithBackend` 不接 `maxItems`：第三方容量可能按字节或 cost 设置，不能假装是条目数。后端构造者必须提供正值容量；其真实上限属于适配器契约和验收内容。
- 新入口拒绝 nil 接口及包装 nil 指针的 backend，返回构造错误；nil 检测限定在构造期，不放到每次命中路径。
- `Option[T]` 保持原形式，不添加 `WithBackend`，避免默认容量参数与外部容量互相覆盖。
- `Cache[T]` 只借用 backend；不自动关闭它。所有权固定由创建适配器的装配者持有，不增加 owned/borrowed 配置开关。
- 后端必须是进程内、有界、同步返回的存储；本契约不接网络 I/O，不为阻塞任意用户代码创建额外 goroutine 或承诺硬超时。

### 5.2 数据与返回语义

- `found=false, err=nil` 是正常 miss；合法零值、nil 指针不能与 miss 混同。
- `stored=false, err=nil` 是正常容量／准入拒绝。它不应使成功 loader 变成业务错误，也不代表以后必定命中。
- `stored=true` 表示本次写入已完成处理，且在该操作完成的某一时点被存储接受；后端仍可按容量策略随即淘汰。**任何 Set 返回都不能留下会在后续 Delete 后生效的旧异步写入。**
- `Delete` 幂等：键不存在是成功。成功返回后，该实例中此前的 Set 不得再使该键复活。
- `Record.ExpiresAt` 由 Cache 在 loader 成功后，以注入时钟和一次抖动计算；每个写入只计算一次。Backend 不修改该期限。
- 每次命中由 Cache 用当前注入时钟再次检查逻辑期限，不依赖供应商的清扫周期。过期与损坏 record 不返回给业务。
- 默认 backend 的 `Len` 保留精确条目计数和惰性过期语义；其它 backend 的 `Len` 是调用时的存储条目快照，可能包括尚未清扫的过期记录，不能用于证明有效键数、LRU 或内存用量。Ristretto 后续适配可以用锁定版本已有的 `IterValues` 计数，代价 O(n)，不新增一份无限键索引。
- 默认 backend 不复制 T；字节后端必须显式传入 value codec，业务需接受序列化后的值语义。不能承诺指针身份、未导出字段等跨序列化不变。

### 5.3 失败行为

| 事件 | 调用方结果 | 缓存处理 |
|---|---|---|
| 正常 miss | 走原有合并 loader | 成功后尝试一次写入 |
| 正常准入拒绝 | 返回本次 loader 的值、nil | 不重试、不强制绕过供应商准入 |
| Get／Set 返回真正错误或 record 损坏 | 命中不可用则走 loader；loader 成功仍返回值 | 将本 Cache 标为 bypass，之后不再读写 backend |
| Delete 返回错误 | `InvalidateErr` 返回包装原因；旧 `Invalidate` 保持 void | 在同一状态锁内标 bypass，后续不能读到未删掉的旧值 |
| 已 bypass 后调用 `InvalidateErr` | 返回保存的后端故障原因 | 不自动探测恢复、不回到可能残留旧值的存储 |
| loader 错误／panic | 保留现有错误契约 | 不缓存；完成登记回收 |

采用单个 bypass 状态而不是无限增长的“删除失败 key 黑名单”。进入 bypass 后保留 singleflight、加载超时和在途限制；恢复需要装配者建立新的空后端和 Cache。该保守策略会牺牲故障后的命中率，必须在文档中明确，不能静默自动恢复。

`Invalidate` 委托 `InvalidateErr`；发生故障时只记录一次进入 bypass 的诊断。先在锁内更新状态，释放所有状态锁后再输出日志。沿用 `gtkit/logger/v2`，记录固定操作名和错误类别，不记录缓存 key、值、编码字节或供应商错误原文。正常 miss/准入拒绝不输出错误日志。Cache 不反向调用业务模块。

## 6. 并发、失效与生命周期

### 6.1 锁与提交顺序

- `loadMu` 继续保护在途登记、准入和 singleflight 接入。
- 现有 `mu` 作为存储／状态锁：保护 backend 操作、bypass、逻辑过期删除，以及每个 loadEntry 的 fenced 标记。
- 需要同时取锁时只允许 `loadMu → mu`；持有 `mu` 时不得再取得 `loadMu`。Backend、codec、成本回调不得重入 Cache。
- loader 在锁外执行。成功后仅持有 `mu`，检查 fenced 与 bypass，再提交 backend。检查和提交不可拆开。
- `InvalidateErr` 按固定顺序定位在途项、标记 fenced 并删除存储；标记与删除相对 loader 提交串行化。
- 若 loader 提交先完成，Invalidate 随后删除它；若失效先完成，loader 不能回填。过期清理与新写入也由同一锁串行化，不能删掉后来写入的新值。
- loader 返回值使用内部 `loadResult[T]` 包装，让 nil 接口值也能经过 singleflight 返回；保留既有 panic cause 与结果广播。
- Backend 自身清扫／淘汰可以并发发生，但不能回调 Cache 或改变上述“旧写入不得晚于删除生效”的契约。

这保留现有存储路径的粗粒度同步形态，不宣称提高并行命中吞吐。第三方库潜在吞吐收益需之后实测，不能因其原生并发能力就提前移除正确性锁。

### 6.2 Close 的所有权

Backend 不要求 Close 方法，避免默认内存实现和无资源适配器承担虚假生命周期。持有后台资源的具体适配器另实现 `Close() error`，由创建者保存具体实例并负责调用：

1. 停止接受新业务调用并等待使用者、在途 loader 结束；不能在仍使用 Cache 时关闭底层引擎。
2. 调用适配器 Close。适配器串行化读写与关闭，保证重复关闭安全、底层资源最多释放一次，关闭后方法返回明确错误而非 panic。
3. 原 Cache 不再使用。需要重启时创建全新实例，不复用已关闭引擎。

本轮不改 bootstrap/runtime 的关闭接线。未来真正接入时必须明确装配者、关闭顺序与等待预算；不把构造函数接受 backend 当作所有权转移。忽略 ctx 的 loader 仍需由业务提供有界退出，不能用关闭引擎冒充其已退出。

## 7. 三种库后续如何接入

本节是适配约束；三个具体适配器均**尚未实现**，均不属于 P1–P5 的默认代码清单。

| 引擎 | 容量和类型 | 逻辑 TTL 与关键约束 |
|---|---|---|
| FreeCache | 明确的正值字节预算；key/value 为字节 | codec 编码 T；record 携带精确过期点。原生 TTL 秒级，只可协助回收，不能作为逻辑有效性依据；不把亚秒 TTL 截断为 0 后意外永久缓存 |
| BigCache | 正值 `HardMaxCacheSize`，单位 MB；value 为字节 | codec 编码 T；record 期限由外层复检。`LifeWindow`／`CleanWindow` 不等于逐 key 精确 TTL，不能只靠清扫器保证过期不可见 |
| Ristretto | 沿用项目锁定的 v2.4.2；类型化 `Record[T]`；明确 `MaxCost` 和成本单位 | 每次写入完成缓冲处理后再返回；`Set=true` 和 `Wait` 都不单独代表准入成功。需要观测本次 record 的处理结果，拒绝正常返回 false；Delete 也需完成顺序屏障 |

### 7.1 编码规范

- FreeCache／BigCache 的适配器构造函数显式接收 codec：`Encode(T) ([]byte, error)` 和 `Decode([]byte) (T, error)`；本包默认路径不选 JSON、不新增 codec 依赖。
- 字节 envelope 使用固定格式：1 字节版本（值为 1）＋8 字节大端有符号 Unix 秒＋4 字节大端纳秒（必须小于 1e9）＋codec payload。截断、未知版本、非法纳秒均是解码错误，不是 miss。
- 不直接用单个 UnixNano 保存期限，以免扩大可表示时间范围时溢出；序列化会丢失 monotonic 部分，逻辑比较使用注入时钟对应的时间点。
- 编码边界复制必要缓冲区；调用方修改 codec 返回的临时字节不得污染已缓存数据。各适配器测试真实库的所有权约定。
- 仅允许一个 Cache 使用一个独立 backend；不与 HTTP 缓存或其它类型的 Cache 共用同一底层实例，不依赖自行约定的 key 前缀防止类型碰撞。

### 7.2 Ristretto 顺序与准入

- 不能用每次 `Wait()` 就“必然命中”作为验收断言。真正拒绝仍须返回 false，并保持业务结果成功。
- 适配器串行完成 Set、处理屏障和本次写入确认；内部给写入 record 添加递增序号以区分旧值，序号只用于适配，不进入业务 T。
- Delete 不仅移除当前值，还需消化此前缓冲操作，避免旧写入晚到。缓冲拒绝、策略拒绝、容量淘汰分别测试。
- Close 与 Set/Wait/Delete 不能并发进入原生引擎。适配器锁只保护引擎生命周期，不得由回调取得 Cache 的锁。
- `MaxCost` 是成本预算，不自动等于进程 RSS。复杂 T 需要显式成本函数；不能把 `unsafe.Sizeof(T)` 宣称为包含其引用对象的真实内存。

### 7.3 第三方版本与待提供输入

| 项目 | 当前结论 | 何时必须补齐 |
|---|---|---|
| 首个库的选择 | 用户尚未选择，不阻塞基础接口设计 | P6 开始前 |
| FreeCache／BigCache 版本 | 未选定、未安装 | 选择对应 P6 分支后，核实官方稳定版本、module 路径、许可证及 Go 兼容性，再获得依赖授权 |
| Ristretto 版本 | go.mod 已锁定 v2.4.2，默认不升级 | 选择 Ristretto 后复核当时 go.mod 与真实包声明 |
| value 类型／codec／容量预算 | 未提供 | P6 开始前由真实调用方提供；不能用示例容量代替生产预算 |
| 性能和关闭预算 | 未提供 | 实际装配、压测或上线前；本方案不填写伪造 QPS、P99、期限与负责人 |

## 8. 文件清单与依赖边界

### 8.1 本轮只准备的文档

- `docs/cache-adapter-design-plan-2026-10-23.md`（本文件，完整方案、计划、事实及进展的唯一汇总）。
- `openspec/changes/add-pluggable-cache-adapters/` 的 `.openspec.yaml`、`proposal.md`、`design.md`、`tasks.md`、`specs/pluggable-data-cache/spec.md`（本会话独立设计制品）。

不创建额外的根目录 task_plan/findings/progress 文件，不改 `.git/info/exclude`；本轮仅维护指定方案范围的文档。

### 8.2 P1–P5 获准后才可以修改的基础实现文件

| 文件 | 拟用途 |
|---|---|
| `internal/pkg/cache/cache.go` | 保留公开入口，接入 backend，统一过期、失效提交和错误降级 |
| `internal/pkg/cache/backend.go`（新增） | Record、Backend 和 NewWithBackend；契约注释及构造期校验 |
| `internal/pkg/cache/backend_lru.go`（新增） | 将原 map/list 存储逻辑迁入未导出默认实现；没有新依赖 |
| `internal/pkg/cache/cache_test.go` | 保留原有测试和确定性兼容边界；不削弱断言凑通过 |
| `internal/pkg/cache/panic_test.go` | 保留子进程与共享 panic 回归，必要时复用原入口 |
| `internal/pkg/cache/backend_test.go`（新增） | 注入、nil、后端错误、拒绝、失效失败与接口契约 |
| `internal/pkg/cache/backend_contract_test.go`（新增） | 默认 LRU 与受控假后端复用的契约用例、确定性并发屏障 |
| `docs/cache.md` | 仅更新“进程内缓存”部分，加入新入口、所有权、失败语义及后端差异；不顺手修改 HTTP 部分 |

不修改 `go.mod`、`go.sum`、配置、示例 YAML、`internal/pkg/httpcache`、业务模块、bootstrap、工具链或项目规则。现有工作区中的其它改动须保持。

### 8.3 P6 选择后才进入的新范围

只允许以下一个分支：`backend_freecache.go`/`backend_freecache_test.go`，或 `backend_bigcache.go`/`backend_bigcache_test.go`，或 `backend_ristretto.go`/`backend_ristretto_test.go`，均位于现有 `internal/pkg/cache/`。同时明确必要的 `go.mod`/`go.sum` 与 `docs/cache.md` 差异，另存该阶段契约并取得授权；不一次引入三个库，不自动改业务装配。

## 9. 可复核验收矩阵

所有行当前均为“未执行”；预期是测试目标，不是结果。

| 编号 | 目标／反例 | 证据要求 |
|---|---|---|
| T01 | 原 17 项测试经旧 New 路径成立 | cache 全包普通测试输出，逐项无 fail/skip |
| T02 | 新入口 nil／typed-nil／非正 TTL；保留各 option 行为 | table-driven 边界和明确错误 |
| T03 | 默认 maxItems、LRU 顺序、Len 含未清理过期项不变 | 小容量精确操作序列，不用近似命中率替代 |
| T04 | 命中不加载；取消 ctx 命中仍返回；miss 已取消不加载 | loader 次数与返回值断言 |
| T05 | 单飞、同键加入、不同键名额上限 | 显式屏障；不得用 Sleep 猜调用方已加入 |
| T06 | loader 接收 context 值、独立超时；忽略取消的 loader 占位直到退出 | 不同键超限、最终释放和同键恢复证据 |
| T07 | loader 错误、panic 共享与原因链；下次可重载 | 保留真实子进程用例；不泄漏 panic 私有文案 |
| T08 | T 的零值、nil 指针、`T=any` 的 nil 接口值 | miss 与合法值分开；先记录 G2 的原实现现象 |
| T09 | 相同／不同 key 独立；原始 key 字节保持 | 空字符串和 Unicode key 的读写失效 |
| T10 | 恰好到期、过期后不返回、只在写入时抖动、命中不续期 | 注入时钟，检查 backend 接到的同一 ExpiresAt |
| T11 | G1：检查 fenced 后、提交前的失效；反向先提交再失效 | 可控制假后端／内部提交屏障；两种顺序最终均无旧缓存 |
| T12 | 过期清理不能删除后写的新值 | Get/Set/Delete 可控制顺序，无定时碰运气 |
| T13 | 正常准入拒绝 | 成功 loader 返回值、nil；下次可重新加载；名额释放 |
| T14 | Get、Set、Delete 故障与坏 record | 进入 bypass；成功业务结果不丢；后续旧值不可命中；故障原因可识别 |
| T15 | 旧 Invalidate 和新 InvalidateErr | 旧签名可编译；不存在键成功；删除失败可观察且不吞掉不安全状态 |
| T16 | 默认与假后端相同的控制面契约 | 同一测试工厂跑相同用例，不复制两套相近测试 |
| T17 | 对原实现恢复 G1 的“解锁后写入”反例必失败 | 仅实现期在隔离副本验证，保留失败及修复输出；本轮不执行 |
| T18 | 三种真实引擎各自的 TTL、容量、缓冲与关闭 | 只在 P6 对选定真实库运行；fake 不能代替这一证据 |

覆盖场景而不机械要求覆盖率数字；本轮没有计算覆盖率。不同 backend 的淘汰策略不强行按 LRU 断言，精确 LRU 只对默认实现要求。

## 10. 分阶段实施计划

每个阶段开始前重新读本文件、错误记录、最新指令及对应 Guide；源文件发生变化先核对本方案，禁止用记忆重排阶段。P1–P5 形成完整基础改造；P6 是用户未来另选的具体适配器任务。

| 阶段／步骤 | 前置条件 | 具体工作 | 核对／证据 | 停止条件／交付物 | 当前状态 |
|---|---|---|---|---|---|
| P0.1 | 本轮仅设计授权 | 读取规则、源码、现有测试、规格和依赖 | 2 节事实与 2.2 清单可定位原文件 | 必要来源读不到即停；现状清单 | 已完成读取 |
| P0.2 | P0.1 完成 | 保存本方案与独立设计制品 | 回读需求、接口、文件、风险、阶段与状态一致 | 不能把机器分类当代码授权；完整方案 | 已保存并回读；仅设计完成 |
| P1.1 | 用户明确批准基础实现；工作流绑定同一方案 | 先跑旧 cache 全包普通测试，保存环境、命令和退出码；不跑 cmd 或全仓 | `cache-functional` | 既有失败先归因，不改无关包；基线记录 | 未授权／未执行 |
| P1.2 | P1.1 结果可解释 | 为 T02–T12、G1/G2 编写确定性兼容／反例测试 | 新例应在对应缺陷处失败；其余旧例不削弱 | 未能稳定观察指定时序则停；反例及行为清单 | 未授权／未执行 |
| P2.1 | P1 范围仍获准 | 抽取 Record/Backend、lruBackend、NewWithBackend；旧 New 复用 | 默认构造与 T01–T10；`cache-build` | 引入库依赖或改目录则停；默认后端可编译 | 未授权／未执行 |
| P2.2 | P2.1 完成 | 统一 mu 内失效标记与提交；保留 loadMu 顺序及类型化 flight 结果 | T07/T08/T11/T12，完整 cache 普通测试 | 死锁、名额泄漏、原异常原因丢失则停；并发边界修复 | 未授权／未执行 |
| P3.1 | P2 完成 | 接入正常拒绝、故障 bypass、InvalidateErr 及一次性诊断 | T13–T16，全包回归 | 缓存错误盖过业务成功、继续返回残留旧值则停 | 未授权／未执行 |
| P3.2 | P3.1 完成 | 固定借用所有权；用假后端检查关闭后错误和顺序约束 | 契约用例证明 Cache 不擅自 Close | 出现新后台任务／自动重试／无限黑名单则停；完整扩展契约 | 未授权／未执行 |
| P4.1 | P1–P3 功能完成 | 更新 docs/cache 的业务缓存段；检查新旧调用示例与真实签名 | 完整普通测试和 build；T01–T16 逐项记录 | 来源或行为仍有未解释差异则停；基础交付候选 | 未授权／未执行 |
| P4.2 | P4.1 完成 | 需求、接口、错误、生命周期和源码人工回审；实现期加载 backend-acceptance | 只声明实际证据；T18 标后续不适用 | 不把 fake 通过写成三个库已接入 | 未授权／未执行 |
| P5.1 | 全部基础功能完成 | 向用户说明拟运行的受控检查并请求单独授权 | 记录具体获准 ID，不从方案推定许可 | 未获准不运行；保持 not_run | 未授权／未执行 |
| P5.2 | 对应 ID 明确获准 | 按授权跑定向 race/vet/lint/modern，检查日志和退出码 | 新代码指纹对应真实结果；无关旧问题单列 | 工具版本不符或失败不绕过、不扩大修复；最终验收记录 | 未授权／未执行 |
| P6.1 | 用户选择一个具体库并给出业务 value／预算／所有者 | 核实版本、包声明、codec/成本和原生 API，保存该分支精确文件清单 | 第 7 节每项有实际来源与依赖授权 | 缺版本／预算／owner 则不进入接入 | 后续任务，未授权 |
| P6.2 | P6.1 另行批准 | 在原包内实现唯一选定适配器，连接真实库；不自动装配进业务 | T18 + T01–T16 适用项，真实容量／到期／拒绝／关闭证据 | fake 或编译不能代替真实库；单个引擎交付 | 后续任务，未授权 |

阶段结束更新本文件的实际状态、证据和未验证项。连续三轮同问题修正仍失败即停，报告真实现象；用户停止后不重试。

## 11. 检查与授权清单

以下命令是**未来候选命令**；本轮全部未执行。普通 Go test 显式 `-vet=off`，不借 go test 默认 vet 绕过授权。

| ID | 命令 | 执行条件 |
|---|---|---|
| cache-functional | `go test -vet=off -count=1 -timeout=120s ./internal/pkg/cache` | 基础实施获准后；仅测试本包 |
| cache-build | `go build ./internal/pkg/cache` | 基础实施获准后 |
| cache-race | `go test -race -vet=off -count=1 -timeout=120s ./internal/pkg/cache` | 全部功能完成后另行获准 |
| cache-vet | `go vet ./internal/pkg/cache` | 全部功能完成后另行获准 |
| cache-lint | `golangci-lint run ./internal/pkg/cache` | 全部功能完成后另行获准；先满足项目锁定版本 |
| cache-modern | `go fix -diff ./internal/pkg/cache` | 全部功能完成后另行获准；不自动应用改写 |
| cache-spec | 当前 change 的 strict validate（命令见末尾契约） | 实施交付时，仅验证本 change；本轮不执行 |

ERR-20261023-091/101/115 已记录 cmd 全包及全量 coverage 超时，故本计划不安排重跑 cmd 或 `make check`。ERR-20261023-100 的 linter 版本阻断不通过安装工具、改版本声明或使用另一版本绕过。未来若必须做全仓检查，再明确范围和授权，不能从本表推导出来。

## 12. 风险、容量与失败处理

| 风险 | 影响 | 固定处理／验收 |
|---|---|---|
| 抽取时改变默认引用／LRU／取消语义 | 旧调用无声变化 | C01–C18 与 T01–T12；新后端差异另行明示 |
| 删除失败仍使用旧存储 | 已失效旧值重新可见 | 同锁进入永久 bypass；不维护无界失败 key 集合 |
| Ristretto 晚到的写入 | 删除后复活 | Set/Delete 均完成顺序屏障，真实库验证 |
| 精确 TTL 被秒级清扫替代 | 超期值可见 | record 精确期限由 Cache 检查；不向上取整逻辑期限 |
| 借用实例被其它调用方改写或关闭 | 类型污染、panic 或时序破坏 | 一个实例一个 Cache；owner 先停调用再 Close |
| 第三方比默认 LRU 更早淘汰 | 命中率变化 | 允许提前 miss，但不允许超期 hit；不能承诺相同淘汰顺序 |
| bypass 后回源压力增加 | 持续故障时命中率归零 | 保留最多 64 个不同键在途的默认限制；记录一次诊断；由 owner 显式替换实例 |
| 粗粒度锁与 codec 增加延迟 | 引擎原生吞吐收益被抵消 | 不提前去锁；P6 使用真实数据测量后另提优化，当前无性能增益结论 |

已知容量边界：默认存储最多 maxItems 条、不同键同时在途默认最多 64；忽略取消的 loader 不新增无限 goroutine。payload 可变大，因此这两个数字不等于进程 RSS 硬上限。

后续容量计算使用真实输入：`N × (平均 key 字节 + 平均编码 value 字节 + record/索引开销) + 分片/队列固定开销`。各项目前未测，不能填入虚构常数或假装这是实测。三种引擎的字节预算／cost 也须计入引擎以外的对象、codec 临时分配与 singleflight 结果；不得复制第三方 README 的旧基准作为本项目性能结论。

## 13. 迁移、回滚和交付

- 基础实现上线仍走原 `New` 默认路径，无配置切换、无持久数据迁移、无 DB 变更。
- 首个外部 backend 只在未来批准的真实调用方显式构造；不增加字符串注册／全局默认引擎。旧 New 路径继续可用。
- 触发回滚：现有用例失败、旧接口不能编译、过期值可见、失效后旧值回填、名额泄漏、Close 顺序错误，任一成立即停止交付。
- 回滚基础代码只撤销本任务记录的 patch；不能 `reset --hard`、覆盖整个目录或丢弃其它任务修改。若已提交，则由用户批准对应 revert；本轮不提交。
- 外部引擎回滚顺序为停止新调用、排空使用者、关闭该 owner 的资源，重新用原 New 创建空 LRU；预热是可选业务动作，缓存内容不迁移。
- 完成条件：P1–P4 必要项真实通过；已获准的 P5 项真实通过；未获准项明示未执行。不能把基础接口完成写成 FreeCache/BigCache/Ristretto 已可用，不能把设计制品齐全写成运行验收通过。

## 14. 本轮记录与尚未验证事项

- 已读全局规则中的拒绝猜测和错误防复发、项目 AGENTS、相关 Guide、缓存源码／测试／规格、go.mod，以及错误日志中当前 open 项和缓存 panic 历史项 ERR-20261022-045。
- 当前原生请求状态把本轮标为 `mode=code, implementation=true`，但用户原文明示“暂时不改代码”；以用户授权边界为准，本轮只交付文档。
- 首次直接执行 `openspec list --json` 被 hook 以 unsupported/direct check 拒绝；停止该入口。随后读取错误记录中已登记的 `.openspec-auto/tools/run_openspec.py`，通过该正式包装入口成功 list 并创建本次独立 change，未修改 guard、用户配置或会话授权。
- `git hash-object` 的只读基线指纹命令也被 guard 拒绝，未换工具补算。提交 ID 已由 `git rev-parse HEAD` 取得；不声称已核对三份源文件哈希。
- 已通过当前会话的 `workflow.py read` 完整读取本方案和 required_context；错误日志按 1、1001、2001、3001、4001 行分段读取。`activate --plan docs/cache-adapter-design-plan-2026-10-23.md` 实际 exit 0，绑定 `stage=design-only`、`change=add-pluggable-cache-adapters`。未运行 workflow run、ready 或 finish。
- 已实际回读 proposal、design、tasks、规格与本方案；OpenSpec status 返回四项设计制品 done，只表示制品齐全。strict validate 未执行，所有功能、构建及受控检查仍未执行。
- 对 `internal/pkg/cache`、`go.mod`、`go.sum`、`docs/cache.md` 的 `git diff` 实际为空；本轮未改动这些源文件。未根据被拒的 hash 命令生成指纹证明。
- 本文件集中保存本次事实与流程问题；不借规划任务修改其它项目规则、历史错误状态或未授权的代码。

## 15. 官方资料

- [FreeCache README](https://github.com/coocood/freecache#readme)：字节存储、容量预算、秒级到期及提前到期范围。
- [BigCache README](https://github.com/allegro/bigcache#readme)：字节存储、HardMaxCacheSize、LifeWindow／CleanWindow。
- [Ristretto v2.4.2 源码](https://github.com/dgraph-io/ristretto/blob/v2.4.2/cache.go)：SetWithTTL、缓冲／准入、Wait、Delete、IterValues 和 Close。

引用用于解释约束，不构成三个库与本项目兼容、性能达标或已经接入的证据。

## 16. 本会话计划登记

下面的机器契约登记未来基础实现的精确文件范围和候选命令。运行器要求 code 文件与 functional 命令才能登记，因此列出 P1–P5 范围；**它们在本轮均未获执行授权**。stage 固定为 design-only，不运行 ready/finish 来伪装功能已完成。真正实施时先取得用户指令，更新阶段并重新 read/activate。

<!-- harness-contract:v1 -->
```json
{
  "schema_version": 1,
  "stage": "design-only",
  "change": "add-pluggable-cache-adapters",
  "allowed_files": [
    {"path": "docs/cache-adapter-design-plan-2026-10-23.md", "kind": "document"},
    {"path": "docs/cache.md", "kind": "document"},
    {"path": "internal/pkg/cache/cache.go", "kind": "code"},
    {"path": "internal/pkg/cache/backend.go", "kind": "code"},
    {"path": "internal/pkg/cache/backend_lru.go", "kind": "code"},
    {"path": "internal/pkg/cache/cache_test.go", "kind": "code"},
    {"path": "internal/pkg/cache/panic_test.go", "kind": "code"},
    {"path": "internal/pkg/cache/backend_test.go", "kind": "code"},
    {"path": "internal/pkg/cache/backend_contract_test.go", "kind": "code"},
    {"path": "openspec/changes/add-pluggable-cache-adapters/.openspec.yaml", "kind": "document"},
    {"path": "openspec/changes/add-pluggable-cache-adapters/proposal.md", "kind": "document"},
    {"path": "openspec/changes/add-pluggable-cache-adapters/design.md", "kind": "document"},
    {"path": "openspec/changes/add-pluggable-cache-adapters/tasks.md", "kind": "document"},
    {"path": "openspec/changes/add-pluggable-cache-adapters/specs/pluggable-data-cache/spec.md", "kind": "document"}
  ],
  "required_context": [
    {"path": "AGENTS.md", "kind": "rules", "name": "project-rules"},
    {"path": ".harness/error-journal.md", "kind": "guide", "name": "error-journal"},
    {"path": ".harness/guides/workflow-contract.md", "kind": "guide", "name": "session-contract"},
    {"path": ".harness/guides/architecture.md", "kind": "guide", "name": "architecture"},
    {"path": ".harness/guides/internal-pkg.md", "kind": "guide", "name": "internal-pkg"},
    {"path": ".harness/guides/worker-and-cache.md", "kind": "guide", "name": "worker-and-cache"},
    {"path": ".agents/skills/harness-plan/SKILL.md", "kind": "skill", "name": "harness-plan"},
    {"path": "/Users/xiaozhaofu/.agents/skills/go-cache-consistency/SKILL.md", "kind": "skill", "name": "go-cache-consistency"},
    {"path": "internal/pkg/cache/cache.go", "kind": "guide", "name": "cache-source"},
    {"path": "internal/pkg/cache/cache_test.go", "kind": "guide", "name": "cache-tests"},
    {"path": "internal/pkg/cache/panic_test.go", "kind": "guide", "name": "cache-panic-tests"},
    {"path": "openspec/specs/cache-preset/spec.md", "kind": "guide", "name": "cache-baseline-spec"}
  ],
  "checks": [
    {"id": "cache-functional", "kind": "functional", "argv": ["go", "test", "-vet=off", "-count=1", "-timeout=120s", "./internal/pkg/cache"], "timeout_seconds": 180},
    {"id": "cache-build", "kind": "functional", "argv": ["go", "build", "./internal/pkg/cache"], "timeout_seconds": 180},
    {"id": "cache-race", "kind": "race", "argv": ["go", "test", "-race", "-vet=off", "-count=1", "-timeout=120s", "./internal/pkg/cache"], "timeout_seconds": 180},
    {"id": "cache-vet", "kind": "vet", "argv": ["go", "vet", "./internal/pkg/cache"], "timeout_seconds": 180},
    {"id": "cache-lint", "kind": "lint", "argv": ["golangci-lint", "run", "./internal/pkg/cache"], "timeout_seconds": 180},
    {"id": "cache-modern", "kind": "modern", "argv": ["go", "fix", "-diff", "./internal/pkg/cache"], "timeout_seconds": 180},
    {"id": "cache-spec", "kind": "schema", "argv": ["python3", ".openspec-auto/tools/run_openspec.py", "validate", "add-pluggable-cache-adapters", "--type", "change", "--strict", "--json", "--no-interactive"], "timeout_seconds": 120}
  ],
  "regressions": [
    {"error_id": "ERR-20261022-045", "reproduction": "保留原有 loader panic 子进程及共享结果用例，验证进程存活、原因链不泄露文案、后续同键和其它键可加载且名额恢复。", "checks": ["cache-functional", "cache-race"]}
  ]
}
```
