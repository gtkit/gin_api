# 后台任务与调度

worker 遵守模块化边界：`module/<m>/worker` 只依赖 application，不直接 import repository / gorm。

## 基本原则

- **禁止在 `init()`、包级变量初始化、构造函数或中间件注册阶段偷偷启动 goroutine。**
  这类 goroutine 不在任何生命周期管理之下：进程关停时没人等它，它写到一半的数据没人知道。
- worker 由 `internal/bootstrap` 统一启动和停止。
- 请求内不要 fire-and-forget 执行业务副作用；需要异步时走队列 / outbox / job 表。
- 异步任务默认可能重复执行，必须幂等。

## Worker 契约

`internal/bootstrap` 的 `Worker` 接口有七个方法，其中三个是**声明**——它们必须由 worker
自己给出，管理器不做推断：

```go
type Worker interface {
    Name() string           // 低基数稳定标识，用于日志与错误上下文
    Kind() WorkerKind       // 关停排序中的位置
    Required() bool         // Prepare 失败是否即启动失败
    Lifecycle() Lifecycle   // 运行形态
    Prepare(ctx context.Context) error
    Run(ctx context.Context) error
    Cleanup(ctx context.Context) error
}
```

### `Kind`：决定关停顺序，零值非法

| 值 | 含义 |
|---|---|
| `KindBootstrap` | 启动期/一次性任务，不参与 producer-consumer 排序 |
| `KindProducer` | 任务生产者（cron/scheduler），关停时**最先**停 |
| `KindConsumer` | 任务消费者（队列/outbox），关停时在 HTTP 之后 drain |

零值 `KindUnspecified` 在注册期即被拒绝。**若零值被解释成某个具体类别，忘记声明的 worker
就会静默落进那一类**，而错的方向恰恰最难发现——一个本该最先停的 producer 被当成 consumer，
会在 HTTP 关停期间继续生产新任务。

### `Lifecycle`：必须自己声明，不能推断

`LifecycleLongRunning` 表示 `Run` 应阻塞直到 ctx 取消；**在此之前返回（包括返回 `nil`）
一律视为 `ErrUnexpectedExit`**。`LifecycleOneShot` 表示执行一次后正常返回 `nil`。

这个声明不能由管理器推断：管理器唯一能观察到的信号是「Run 返回了」，
而「长期任务意外退出」与「一次性任务正常完成」在这个信号上完全一样。
推断的结果必然是二者之一被静默误判——误判长期 worker 的代价是进程继续对外服务、
功能却已经丢了。

### `Cleanup`：必须幂等，且恰好被调用一次

契约：`Prepare` 成功后，管理器保证 `Cleanup` 最终**恰好被调用一次**，经三条路径之一——
启动回滚（其他 worker 准备失败或启动被取消，按逆序）、正常关停（`Run` 返回后）、
OneShot 完成后。

只在「不会进入 Run」时调用是不够的：那样正常 `Run` 结束后，`Prepare` 建立的连接
与持有的锁没有任何人释放，属真实泄漏。

### 注册

在 `internal/bootstrap/providers.go` 的 `scaffold:module-workers` 标记前追加构造，
`buildWorkers` 的签名不需要改动。`WorkerManager` 的构造需要一个预算：

```go
NewWorkerManager(budget WorkerBudget, workers ...Worker) (*WorkerManager, error)
```

`WorkerBudget` 有三项：`Prepare`（单个 Prepare 上限）、`Startup`（覆盖全部 Prepare
与全部 required OneShot 的 Run）、`Cleanup`（单个 Cleanup 上限）。默认值取
`DefaultWorkerBudget()`。

`buildWorkers` 对自身的进程级副作用承担回滚责任：它注册的指标回调在**任何**错误出口
都必须被撤销，因此回滚写在 `defer` 里而不是末尾——扩展段加入多个「构造 + 注册指标」块之后，
任何中途 return 都不得让此前已注册的回调逃逸。

## 生命周期与关停顺序

worker 的根 context **不直接**跟随 SIGINT / SIGTERM。`App.Run` 用 `context.WithoutCancel`
从信号 context 独立派生 worker context，于是：

```
启动顺序：workers → HTTP
关停顺序：HTTP → workers
```

HTTP 优雅关停完成后才显式取消 worker context，**保证关停期间在飞请求写入的任务仍能被消费**。
改动这个顺序前先看 `app_test.go` 的关停顺序用例。

其余约束：

- loop 必须监听 `ctx.Done()`。
- 外部调用必须设置 timeout。
- 并发数必须有上限，不能按数据量无限开 goroutine。
- required worker 运行期异常会触发全进程优雅关停并非零退出，不做静默重启。
  纯治理型任务若不希望拖垮进程，应在内部记日志后继续，向外返回 nil。

## 单实例执行

多副本部署下定时任务在每个实例上都会跑。`internal/pkg/singleton` 提供基于 Redis 租约的
单实例执行。

**注册时必须声明重复执行时靠什么保证正确**，零值会被拒绝：

| 声明 | 含义 |
|---|---|
| `GuaranteeIdempotent` | 重复执行无副作用差异（如批量 `DELETE ... LIMIT`，第二次影响 0 行） |
| `GuaranteeFenced` | 每个写操作携带 `Held.Fence` 序号，且**写路径拒绝比已见值更小的序号** |

这个约束不是形式主义。**锁不是正确性保证**：网络分区、进程长时间暂停（GC、宿主机挂起）、
租约过期后旧实例恢复，都会让两个实例同时在跑。声明强迫作者在注册时就回答
「重复执行会怎样」，而不是把问题留到事故现场。

两个容易混淆的概念：

- **随机 token** 只解决「别删掉/续期别人的锁」（compare-and-delete / compare-and-renew）；
- **fence 序号**才解决「租约过期后旧实例仍在写」。它必须单调递增，且由下游拒绝旧序号。

把随机 token 当 fencing 用，是一个看起来能工作、实际毫无保护的组合：
两个随机值之间没有先后关系，下游无从判断谁更旧。

单实例 worker 的 `Prepare` 会在启动期用随机探测键真跑一遍抢锁/续租/释放——
只 PING 不能证明当前 ACL 能执行脚本，也不能证明锁键与 fence 键在同一个集群 slot。

## 定时任务

两种机制，按「按间隔」还是「按时刻」选：

| | `singleton.Runner` | 周期任务调度器 |
|---|---|---|
| 触发 | 固定间隔（`time.Ticker`） | cron 表达式 |
| 能表达「每天 03:00」 | 否 | 是 |
| 重启后 | 相位漂移到重启时刻 | 时刻不变 |
| 依赖 | Redis | Redis + 任务队列 |
| 执行方式 | 本进程直接执行 | 投递到队列，由消费者执行 |

固定间隔的任务用 `Runner`——它更简单，且不依赖任务队列。

通用约束：

- 可重复执行；重复运行不造成重复扣款、发送、删除。
- 明确时区、错过执行的补偿策略、最长运行时间。
- 长任务拆小批次，记录游标或 checkpoint。
- 调度入口只做调度，业务逻辑下沉 application service。

### 周期任务调度器（`internal/pkg/tasks`）

默认关闭，配 `tasks.scheduler.enabled` 启用（要求 `tasks.enabled` 也开着——
调度器投递的任务需要有人消费）。

在 `internal/bootstrap/providers.go` 的 `scaffold:periodic-tasks` 标记处追加声明：

```go
tasks.PeriodicTask{
    Spec:      "0 3 * * *",              // 每天 03:00
    TaskType:  "cleanup:ai-conversations", // 必须有对应的已注册处理器
    Payload:   cleanupPayload{Scope: "expired"},
    Guarantee: singleton.GuaranteeIdempotent,
},
```

**触发时刻、任务类型与重复执行保证写在代码里，不在配置里**：配置里的 cron spec
可能指向一个没有处理器的任务类型，而那种失配的表现是任务按时入队、
一直重试到进死信队列，配置文件看起来完全正常。

#### 多副本下的重复投递

底层调度器**没有任何跨实例协调**——每个实例的触发点到了就各自投递一次，
N 个副本就是每次触发投递 N 次。

因此调度经 Redis 租约选主：只有持有租约的实例运行调度器，失去租约立即停止。
这不是能靠「只在一个实例上启用」约束的事——那需要部署侧记住并正确执行，
而它出错时没有任何症状，只表现为下游被执行了 N 次。

**租约把重复降到易主窗口内，消除不了它。** 租约过期而旧 leader 尚未察觉的窗口内，
两个实例会同时认为自己是 leader。网络分区、进程长时间暂停（GC、宿主机挂起）
都会拉长这个窗口。

所以每条周期任务都**必须声明**重复投递时靠什么保证正确，零值在构造期被拒绝——
与 `singleton.Runner` 同一约束、同一套 `Guarantee` 类型。

声明了 `GuaranteeIdempotent` 的任务会自动附加投递去重窗口（`tasks.scheduler.unique_ttl`）
作为第二道防线。它同样不是保证：底层的唯一性锁在**任务成功完成时**就释放，
不是维持整个 TTL，一个很快跑完的任务配合实例间时钟偏移仍可能被投递两次。
有了它就放松声明要求，正好是把「看起来能工作」当成「正确」。

#### 生命周期

调度器是 `KindProducer` + `LifecycleLongRunning` + 非 required：

- 关停时**最先**停——先停生产者再停消费者，在飞的任务才能被处理完；
- 关停时主动释放租约，让接管的实例立即可用，而不是等一个完整的租期；
- 非 required：调度器不可用时实例仍能提供同步接口，而周期任务本就允许迟到。

`Prepare` 会实测租约能力（真跑一遍抢锁/续租/释放）并验证全部 cron spec 可解析。
spec 写错如果留到运行期才发现，表现是这个任务**永远不触发**——
而没有任何人在等一个不存在的信号。

## 异步任务队列（`internal/pkg/tasks`）

`asynqx` 的装配，**默认关闭**。队列是有状态的基础设施：一旦开始入队，任务就必须有人消费——
预置打开只会在某个部署里留下一堆没人处理的积压。

**一个开关管两端**：只开生产端会攒下没人处理的积压；只开消费端则是一个空转的 worker
占着并发配额。

### 入队

```go
if err := p.tasks.Enqueue(ctx, "email:send", EmailPayload{To: addr}); err != nil {
    if errors.Is(err, tasks.ErrDisabled) {
        // 队列未启用。这是一个错误而不是静默丢弃——
        // "以为任务排上了、实际什么也没发生"是这类能力最难查的失效形态。
    }
}
```

### 注册处理器

在 `internal/bootstrap/providers.go` 的 `scaffold:task-handlers` 标记处追加一个
`tasks.Registrar`：

```go
func(w *asynqx.Worker) error {
    return asynqx.Handle(w, "email:send", func(ctx context.Context, p EmailPayload) error {
        return mailer.Send(ctx, p)
    })
},
```

注册发生在 `Prepare` 而不是 `Run`：注册失败属装配错误，必须表现为**启动失败**，
而不是让实例起来之后才发现某个任务类型没人处理（表现为任务一直重试到进死信）。

没有处理器时消费者**根本不注册**——而不是注册一个空转的 worker 占着名字。

### 配置与关停

`tasks.redis_db` **必须同时出现在 `redis.dbs` 里**，否则队列按「redis 未配置」处理。
队列权重必须为正。

`shutdown_timeout` **必须小于容器的 SIGKILL 宽限期**（常见 30s）：否则等 drain 的过程
会被直接杀掉，正在处理的任务既没完成也没重新入队。

消费端是 `KindConsumer`，关停时在 HTTP 之后 drain。它**非 required**：队列不可用时实例
仍能提供同步接口。设为 required 意味着 Redis 抖一下整个实例被摘流，
而队列本身的重试与积压正是用来吸收这类抖动的。

## 队列消费者通则

- ack 发生在持久化副作用成功**之后**。
- 失败路径按队列能力 nack / retry / dead-letter。
- 重试有最大次数或租约边界。
- 大 payload 不直接塞消息体，传业务 ID。
- 顺序有要求时明确 partition / key / 锁策略。

## Outbox 与事务边界

- DB 写入和异步副作用需要一致性时用 outbox。
- **禁止在事务未提交前发消息、调支付、发邮件或触发不可回滚的副作用**——
  事务回滚不会撤销已经发出去的邮件。
- outbox 记录状态、重试次数、下次重试时间和错误原因。

## 测试

- worker 的核心处理函数可单测，不依赖真实 ticker。
- 覆盖 success、可重试失败、不可重试失败、ctx cancel、重复消息幂等。
- 涉及并发处理时跑 `-race`。
- 关停顺序、Cleanup 恰好一次这类契约必须有反证测试（见 [`testing.md`](testing.md)）。
