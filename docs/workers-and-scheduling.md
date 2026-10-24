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

在 `internal/bootstrap/modules.go` 的 `buildWorkers` 中追加构造，
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

## 执行指标

默认 `buildWorkers` 为 OneShot 注入 `metrics.Recorder`；队列消费者通过
`tasks.NewObservedConsumer(..., recorder, handlers...)` 接入每次处理尝试。

实际任务每次结束记录 `worker.executions` 与 `worker.execution.duration`（秒），
按装配期 worker 名、kind、有限 outcome 聚合。队列重试各计一次。
自定义 LongRunning 循环需在每次处理工作时调用 `Recorder.RunWorker`，
不要把进程存活时间当任务耗时。指标口径见
[Worker 单次执行](observability.md#worker-单次执行)。

## 定时任务

只需在本进程按固定间隔执行时，在普通 LongRunning worker 中使用
`time.Ticker`，监听 `ctx.Done()` 并为每次执行设置超时。
需要 cron 表达式并通过队列消费时，使用下面的周期任务调度器。

### 周期任务调度器（`internal/pkg/tasks`）

默认关闭，配 `tasks.scheduler.enabled` 启用，同时启用 `tasks.enabled`。
在 `internal/bootstrap/modules.go` 的 `buildWorkers` 中向 `tasks.NewScheduler` 追加声明：

```go
tasks.PeriodicTask{
    Spec:     "0 3 * * *",                // 每天 03:00
    TaskType: "cleanup:ai-conversations", // 必须有对应的已注册处理器
    Payload:  cleanupPayload{Scope: "expired"},
},
```

触发时刻、任务类型和投递选项写在代码里。调度器只在当前进程内运行，
每个启用它的进程都会独立投递；将来部署多个副本时，应单独安排调度职责。

队列仍可能重试，处理器需要按业务要求保证幂等。明确任务时区、错过执行的补偿策略
和最长运行时间；长任务拆小批次，记录处理进度。调度入口只负责投递，
业务逻辑放在 application service。

投递选项通过 `PeriodicTask.Options` 显式设置，例如队列、重试次数和超时。
调度器不自动添加去重窗口；业务需要时可显式使用 `asynqx.WithTaskUnique`，
但投递去重不能替代处理器的业务幂等。

### 生命周期

调度器是 `KindProducer` + `LifecycleLongRunning` + 非 required：

- `Prepare` 构造并注册一个实例，校验 cron、payload 和投递选项；注册失败会回收该实例。
- `Run` 启动这个实例，等待取消；关停时先停止调度器，再停止队列消费者。
- 首次 `Shutdown` 按 `tasks.shutdown_timeout` 等待；超时后继续等待同一实例实际退出，并保留超时错误。
- `Cleanup(ctx)` 支持重复等待，遵守调用方预算。
- 非 required：准备失败会降级该能力，实例仍可提供同步接口。

底层尚未退出时，`WorkerManager` 保持 worker 的未退出状态，
`App` 不提前关闭它使用的扩展和共享 Redis。实例停止后，管理器完成清理。

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

在 `internal/bootstrap/modules.go` 的 `buildWorkers` 中向 `tasks.NewObservedConsumer` 追加一个
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
