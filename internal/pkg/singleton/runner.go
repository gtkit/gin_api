package singleton

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gtkit/gin-api/internal/pkg/errx"
	applog "github.com/gtkit/gin-api/internal/pkg/log"
)

// Guarantee 声明任务在"两个实例同时运行"时靠什么保证正确。
//
// **必须显式声明**：锁不是正确性保证。网络分区、进程长时间暂停、租约过期后
// 旧实例恢复，都会让两个实例同时在跑。这个类型强迫作者在注册时就回答
// "重复执行会怎样"，而不是把问题留到事故现场。
type Guarantee uint8

const (
	// GuaranteeUnspecified 是零值，注册时被拒绝。
	//
	// 零值非法与本仓其它安全开关一致：忘记声明必须失败，而不是默认到
	// 某一种——默认幂等会让一个非幂等任务悄悄失去保护。
	GuaranteeUnspecified Guarantee = iota

	// GuaranteeIdempotent 表示重复执行无副作用差异。
	//
	// 例：批量 `DELETE ... WHERE created_at < ? LIMIT n`。两个实例同时跑
	// 只是浪费——删同一批行的第二次影响 0 行，结果与跑一次相同。
	GuaranteeIdempotent

	// GuaranteeFenced 表示任务携带 fence 序号，且**写路径校验它**。
	//
	// 声明这一条意味着承诺：每个写操作都带上 Held.Fence，且下游拒绝比
	// 已见值更小的序号（`WHERE fence < :fence`，或唯一约束加版本列）。
	// 只把 fence 传下去而不校验，等于什么也没做。
	GuaranteeFenced
)

// String 返回稳定的低基数名称。
func (g Guarantee) String() string {
	switch g {
	case GuaranteeIdempotent:
		return "idempotent"
	case GuaranteeFenced:
		return "fenced"
	case GuaranteeUnspecified:
		return "unspecified"
	default:
		return "invalid"
	}
}

// TaskFunc 是一次任务执行。
//
// ctx 在两种情况下被取消：进程关停，或**续租失败**。后者是本包的核心语义——
// 失去租约后继续执行没有任何独占保证，因此必须立刻停下。任务实现必须监听
// 这个 ctx，否则租约机制对它形同虚设。
//
// held 携带 fence 序号，供 GuaranteeFenced 的任务传给写路径。
type TaskFunc func(ctx context.Context, held Held) error

// Runner 在持有租约的前提下周期性执行一个任务。
type Runner struct {
	lease     *Lease
	task      TaskFunc
	guarantee Guarantee
	interval  time.Duration
	// maxRuntime 是单次执行的上限。
	//
	// 必须有：一个卡住的任务会一直持有租约（续租循环仍在跑），
	// 于是别的实例永远接不了手，而这个实例什么也没做。
	maxRuntime time.Duration
	// renewInterval 是续租周期，取租期的 1/3。
	renewInterval time.Duration
	// renewTimeout 是单次续租调用的超时。
	renewTimeout time.Duration

	observer Observer
	now      func() time.Time
}

// Observer 接收 Runner 的运行期事件，便于接日志与指标。
//
// 未配置时由默认观察者记录失败。实现必须并发安全且不阻塞。
type Observer interface {
	// LeaseAcquired 在成功抢到租约时调用。
	LeaseAcquired(task string, fence int64)
	// LeaseSkipped 在租约被别的实例持有时调用。**这是正常情况**。
	LeaseSkipped(task string)
	// LeaseLost 在续租失败、任务被中止时调用。这是需要关注的事件。
	LeaseLost(task string, err error)
	// TaskSucceeded 在任务正常返回时调用。
	//
	// **必须有这个事件**：只报失败的话，指标上没有分母——"今天失败了 3 次"
	// 在跑了 3 次和跑了 300 次的情况下是完全不同的两件事，而只看失败数
	// 无从分辨。
	TaskSucceeded(task string)
	// TaskFailed 在任务返回错误时调用。
	TaskFailed(task string, err error)
}

type loggingObserver struct{}

func (loggingObserver) LeaseAcquired(string, int64) {}
func (loggingObserver) LeaseSkipped(string)         {}
func (loggingObserver) TaskSucceeded(string)        {}
func (loggingObserver) LeaseLost(task string, err error) {
	applog.Error("singleton lease failed", applog.String("task", task), applog.Err(err))
}
func (loggingObserver) TaskFailed(task string, err error) {
	applog.Error("singleton task failed", applog.String("task", task), applog.Err(err))
}

// Config 是 Runner 的配置。
type Config struct {
	// Interval 是两次尝试之间的间隔。
	Interval time.Duration
	// MaxRuntime 是单次执行的上限；留空按租期的 10 倍。
	MaxRuntime time.Duration
	// Guarantee 声明重复执行时的正确性来源，零值非法。
	Guarantee Guarantee
	// Observer 接收运行期事件；nil 时默认记录失败日志。
	Observer Observer
}

// NewRunner 构造 Runner。
//
// **零值 Guarantee 会被拒绝**：这是注册期的 fail-fast，见 Guarantee 的说明。
func NewRunner(lease *Lease, task TaskFunc, cfg Config) (*Runner, error) {
	if lease == nil {
		return nil, errors.New("singleton: lease is required")
	}
	if task == nil {
		return nil, errors.New("singleton: task is required")
	}
	if cfg.Interval <= 0 {
		return nil, fmt.Errorf("singleton: interval must be positive, got %s", cfg.Interval)
	}

	switch cfg.Guarantee {
	case GuaranteeIdempotent, GuaranteeFenced:
	case GuaranteeUnspecified:
		return nil, errors.New(
			"singleton: task must declare how it stays correct when two instances run at " +
				"once — either GuaranteeIdempotent (repeating it changes nothing) or " +
				"GuaranteeFenced (every write carries the fence and the write path rejects " +
				"stale ones). A lease alone is not a correctness guarantee: network " +
				"partitions, long pauses and expired leases all produce overlapping runs")
	default:
		return nil, fmt.Errorf("singleton: unknown guarantee %d", cfg.Guarantee)
	}

	maxRuntime := cfg.MaxRuntime
	if maxRuntime <= 0 {
		maxRuntime = 10 * lease.ttl
	}

	// 续租周期取租期的 1/3：留出两次重试的余量。取 1/2 的话，一次失败就
	// 只剩一次机会，而网络抖动经常成对出现。
	renewInterval := lease.ttl / 3
	if renewInterval <= 0 {
		renewInterval = time.Millisecond
	}

	observer := cfg.Observer
	if observer == nil {
		observer = loggingObserver{}
	}

	return &Runner{
		lease:         lease,
		task:          task,
		guarantee:     cfg.Guarantee,
		interval:      cfg.Interval,
		maxRuntime:    maxRuntime,
		renewInterval: renewInterval,
		// 续租调用用独立的短超时：它跟任务 ctx 走的话，一个慢的续租会
		// 拖到任务超时才被发现，而那时租约早就归别人了。
		renewTimeout: renewInterval,
		observer:     observer,
		now:          time.Now,
	}, nil
}

// Guarantee 返回本任务声明的正确性来源。
func (r *Runner) Guarantee() Guarantee { return r.guarantee }

// Name 返回租约名。
func (r *Runner) Name() string { return r.lease.name }

// Run 周期性尝试获取租约并执行任务，直到 ctx 取消。
//
// 每轮独立抢锁而不是"抢到后一直持有"：长期持有会让某个实例成为事实上的
// 永久 leader，它一旦变慢（但没死），别的实例也接不了手。每轮重抢的代价
// 是一次 Redis 往返，换来的是故障实例最多影响一个周期。
func (r *Runner) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	// 启动后先跑一轮，而不是等第一个 tick：一个每天跑一次的任务，
	// 等 tick 意味着重启后要等一整天。
	r.runOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.runOnce(ctx)
		}
	}
}

// runOnce 抢一次租约并在持有期间执行任务。
func (r *Runner) runOnce(ctx context.Context) {
	held, err := r.lease.Acquire(ctx)
	switch {
	case errors.Is(err, ErrNotAcquired):
		// 别的实例在跑。**正常情况**，不记为失败。
		r.observer.LeaseSkipped(r.lease.name)
		return
	case err != nil:
		// Redis 故障：不执行任务。没有租约就执行，等于所有实例同时跑——
		// 恰好是这个机制存在的原因。
		r.observer.LeaseLost(r.lease.name, err)
		return
	}

	r.observer.LeaseAcquired(r.lease.name, held.Fence)

	// 释放必须在 defer 里：任务函数 panic 时（panic 继续向上传播，由进程
	// 编排的 recover 兜底），锁同样要归还——否则只能等租期自然过期，
	// 下一轮白等一个 TTL。释放用**独立的 ctx**：任务 ctx 在此时已经取消，
	// 用它释放会立刻失败。
	defer func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), r.renewTimeout)
		defer releaseCancel()
		if releaseErr := r.lease.Release(releaseCtx, held); releaseErr != nil {
			r.observer.LeaseLost(r.lease.name, releaseErr)
		}
	}()

	// 任务 ctx 由三件事终止：进程关停（父 ctx）、最大执行时长、失去租约。
	taskCtx, cancel := context.WithTimeout(ctx, r.maxRuntime)
	defer cancel()

	// 续租循环与任务并发跑。它在失去租约时 cancel 任务 ctx——
	// 这是本包的核心语义：失去租约后继续执行没有任何独占保证。
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		r.keepAlive(taskCtx, cancel, held)
	}()
	defer func() {
		cancel()
		<-renewDone
	}()

	taskErr := r.task(taskCtx, held)

	cancel()
	<-renewDone

	switch {
	case taskErr == nil:
		r.observer.TaskSucceeded(r.lease.name)
	case errx.IsPureAbort(taskErr):
		// **纯**被中止不算任务失败：原因要么是进程关停，要么是失去租约——
		// 后者已经由 LeaseLost 报过了。在这里再报一次会让同一个事件
		// 在指标上计两次，"失败率"因此虚高一倍。
		//
		// 必须逐成员判定（errx.IsPureAbort）：errors.Is 对聚合错误任一成员
		// 命中即真，errors.Join(Canceled, realErr) 会被整体判成"被中止"，
		// 那个真实故障就从指标与告警里消失了。
	default:
		r.observer.TaskFailed(r.lease.name, taskErr)
	}
}

// keepAlive 周期性续租，失去租约时取消任务。
func (r *Runner) keepAlive(ctx context.Context, cancel context.CancelFunc, held Held) {
	ticker := time.NewTicker(r.renewInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		// 独立短超时：续租跟着任务 ctx 走的话，一个慢的续租会拖到任务
		// 超时才被发现，而那时租约早已归别人。
		renewCtx, renewCancel := context.WithTimeout(context.WithoutCancel(ctx), r.renewTimeout)
		err := r.lease.Renew(renewCtx, held)
		renewCancel()

		if err == nil {
			continue
		}

		// **失败即中止，不"再观察一次"**：无论是明确失去租约还是 Redis
		// 不可达，这个实例都已经没有独占保证了。多试一次的那段时间里，
		// 另一个实例可能正在跑同一个任务。
		r.observer.LeaseLost(r.lease.name, err)
		cancel()
		return
	}
}
