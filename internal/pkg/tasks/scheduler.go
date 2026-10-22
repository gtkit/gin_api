package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/gtkit/asynqx"

	"github.com/gtkit/gin-api/internal/pkg/errx"
	"github.com/gtkit/gin-api/internal/pkg/singleton"
	"github.com/gtkit/gin-api/internal/platform/config"
)

// SchedulerObserver 接收调度器的运行期事件。
//
// 用回调而不是在包内写日志：调用方接入自己的日志与指标栈，本包对两者一无所知。
// 各字段可为 nil，此时对应事件被忽略。
//
// 实现必须并发安全且不阻塞——事件从选主循环上同步发出。
type SchedulerObserver struct {
	// LeaseAcquired 在成为 leader 时调用。
	LeaseAcquired func(scheduler string, fence int64)
	// LeaseSkipped 在租约被别的实例持有时调用。**这是正常情况**，
	// 多副本部署下每一轮只有一个实例不会命中它。
	LeaseSkipped func(scheduler string)
	// LeaseLost 在续租失败、调度被中止时调用。这是需要关注的事件。
	LeaseLost func(scheduler string, err error)
	// RoundFailed 在一轮选主因错误结束时调用。
	//
	// 一轮失败不终止 worker：调度器非 required，Redis 抖动应当重试而不是拖垮进程。
	// 但它必须被观察到——只重试不上报，一个持续失败的调度器看起来和正常的一样。
	RoundFailed func(scheduler string, err error)
}

func (o SchedulerObserver) leaseAcquired(name string, fence int64) {
	if o.LeaseAcquired != nil {
		o.LeaseAcquired(name, fence)
	}
}

func (o SchedulerObserver) leaseSkipped(name string) {
	if o.LeaseSkipped != nil {
		o.LeaseSkipped(name)
	}
}

func (o SchedulerObserver) leaseLost(name string, err error) {
	if o.LeaseLost != nil {
		o.LeaseLost(name, err)
	}
}

func (o SchedulerObserver) roundFailed(name string, err error) {
	if o.RoundFailed != nil {
		o.RoundFailed(name, err)
	}
}

// PeriodicTask 是一条周期任务声明。
//
// 触发时刻、任务类型与重复执行保证放在代码里而不是配置里：配置里的 cron spec
// 可能指向一个没有处理器的任务类型，而那种失配的表现是任务按时入队、
// 一直重试到进死信队列，配置文件看起来完全正常。
type PeriodicTask struct {
	// Spec 是 cron 表达式，语义由队列后端解析（支持 "@every 1h" 等描述符）。
	Spec string
	// TaskType 是任务类型名，必须与已注册的处理器对应。
	TaskType string
	// Payload 随任务投递，会被序列化为 JSON。
	Payload any
	// Guarantee 声明重复投递时靠什么保证正确。零值在注册期被拒绝。
	//
	// **租约不是正确性保证。** 租约过期而旧 leader 尚未察觉的窗口内，
	// 两个实例会同时认为自己是 leader 并各自投递。网络分区、进程长时间暂停
	// （GC、宿主机挂起）都会拉长这个窗口。
	//
	// 这个声明强迫作者在注册时就回答"重复投递会怎样"，
	// 而不是把问题留到事故现场。
	Guarantee singleton.Guarantee
	// Options 是附加的投递选项（队列、重试次数、超时等）。
	Options []asynqx.TaskOption
}

// validate 校验单条声明。
func (t PeriodicTask) validate() error {
	var errs []error
	if strings.TrimSpace(t.Spec) == "" {
		errs = append(errs, errors.New("spec must not be empty"))
	}
	if strings.TrimSpace(t.TaskType) == "" {
		errs = append(errs, errors.New("task type must not be empty"))
	}
	if t.Guarantee == singleton.GuaranteeUnspecified {
		errs = append(errs, errors.New(
			"guarantee must be declared: a lease is not a correctness guarantee, "+
				"so every periodic task must state what makes a duplicate delivery safe "+
				"(GuaranteeIdempotent or GuaranteeFenced)"))
	}
	if len(errs) > 0 {
		return fmt.Errorf("tasks: periodic task %q: %w", t.TaskType, errors.Join(errs...))
	}
	return nil
}

// Scheduler 按 cron 触发周期任务，并经 Redis 租约保证多副本下只有一个实例调度。
//
// 为什么必须选主：底层调度器**没有任何跨实例协调**——每个实例的触发点到了
// 就各自投递一次，N 个副本就是每次触发投递 N 次。这不是能靠"只在一个实例上
// 启用"约束的事：那需要部署侧记住并正确执行，而它出错时没有任何症状，
// 只表现为下游被执行了 N 次。
//
// 它实现装配层 worker 管理器的三段生命周期，因此自动获得关停排序
// （生产者最先停）、Prepare 失败即启动失败、未预期退出必须被上报。
type Scheduler struct {
	cfg     config.TasksConfig
	client  redis.UniversalClient
	name    string
	entries []PeriodicTask

	lease    *singleton.Lease
	observer SchedulerObserver

	// released 保证 Cleanup 幂等。
	released sync.Once
}

// NewScheduler 构造调度器。
//
// 队列或调度器任一关闭、或没有任何周期任务时返回 nil：没有条目的调度器是一个
// 空转的 worker，注册它只会让关停多等一轮、让监控里多一个永远空闲的实例。
//
// 构造不做任何 I/O——租约的连通性与脚本能力在 Prepare 里实测。
func NewScheduler(
	cfg config.TasksConfig, client redis.UniversalClient, name string,
	observer SchedulerObserver, entries ...PeriodicTask,
) (*Scheduler, error) {
	if !cfg.Enabled || !cfg.Scheduler.Enabled || len(entries) == 0 {
		return nil, nil //nolint:nilnil // 未启用时返回 nil 调度器是正常路径，不是错误。
	}
	if client == nil {
		return nil, errors.New("tasks: scheduler requires a redis client")
	}

	var errs []error
	for _, entry := range entries {
		if err := entry.validate(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	lease, err := singleton.NewLease(
		client, schedulerLeasePrefix, cfg.Scheduler.EffectiveLeaseName(), cfg.Scheduler.EffectiveLeaseTTL(),
	)
	if err != nil {
		return nil, fmt.Errorf("tasks: scheduler lease: %w", err)
	}

	return &Scheduler{
		cfg:      cfg,
		client:   client,
		name:     name,
		entries:  entries,
		lease:    lease,
		observer: observer,
	}, nil
}

// schedulerLeasePrefix 是租约键前缀。
const schedulerLeasePrefix = "tasks"

// Name 返回低基数标识。
func (s *Scheduler) Name() string { return s.name }

// Prepare 校验配置并实测租约能力，注册期的失败在这里全部暴露。
//
// 刻意**不**在这里构造并持有调度器实例：底层调度器停止后进入终态、无法重启，
// 而本实现在每次失去租约后都要停掉它——预先构造的那个实例在第一次易主后就废了。
//
// spec 的合法性用一个临时实例验证后立即关闭。spec 写错（"0 3 * * *" 少写一段）
// 如果留到运行期才发现，表现是这个任务**永远不触发**，
// 而没有任何人在等一个不存在的信号。
func (s *Scheduler) Prepare(ctx context.Context) (err error) {
	// 只 PING 不能证明当前 ACL 能执行脚本，也不能证明锁键与 fence 键
	// 在同一个集群 slot。用随机探测键真跑一遍抢锁/续租/释放。
	if err := singleton.Probe(ctx, s.client, schedulerLeasePrefix); err != nil {
		return fmt.Errorf("tasks: scheduler lease probe: %w", err)
	}

	probe, err := s.build()
	if err != nil {
		return err
	}
	// 无论注册成功与否都要关掉这个临时实例，否则它持有的连接与 goroutine 会泄漏。
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx), s.cfg.EffectiveShutdownTimeout())
		defer cancel()
		if shutdownErr := probe.Shutdown(shutdownCtx); shutdownErr != nil {
			err = errors.Join(err, fmt.Errorf("tasks: close scheduler probe: %w", shutdownErr))
		}
	}()

	if err := s.register(ctx, probe); err != nil {
		return err
	}
	return nil
}

// build 构造一个新的底层调度器实例。
//
// 每次成为 leader 都要新建：底层实例停止后进入终态，再次启动返回"已停止"错误。
func (s *Scheduler) build() (*asynqx.Scheduler, error) {
	inner, err := asynqx.NewScheduler(
		asynqx.WithRedisInstance(s.client),
		asynqx.WithDefaultTaskTimeout(s.cfg.EffectiveTaskTimeout()),
		asynqx.WithShutdownTimeout(s.cfg.EffectiveShutdownTimeout()),
	)
	if err != nil {
		return nil, fmt.Errorf("tasks: build scheduler: %w", err)
	}
	return inner, nil
}

// register 把全部条目登记进给定实例。Register 只登记不投递，无副作用。
func (s *Scheduler) register(ctx context.Context, inner *asynqx.Scheduler) error {
	for _, entry := range s.entries {
		opts := entry.Options
		// 幂等任务附加去重窗口作为**第二道防线**。
		//
		// 它挡不住所有重复：底层的唯一性锁在任务成功完成时就释放，
		// 不是维持整个 TTL。一个很快跑完的任务配合实例间时钟偏移仍可能被投递两次。
		// 因此它不替代 Guarantee 声明——有了它就放松声明要求，
		// 正好是把"看起来能工作"当成"正确"。
		if entry.Guarantee == singleton.GuaranteeIdempotent {
			opts = append(opts, asynqx.WithTaskUnique(s.cfg.Scheduler.EffectiveUniqueTTL()))
		}
		if _, err := inner.Register(ctx, entry.Spec, entry.TaskType, entry.Payload, opts...); err != nil {
			return fmt.Errorf("tasks: register periodic task %q (spec %q): %w",
				entry.TaskType, entry.Spec, err)
		}
	}
	return nil
}

// Run 运行选主循环，阻塞至 ctx 取消。
//
// 未持有租约的实例**不投递任何任务**：它只是周期性地尝试抢锁。
func (s *Scheduler) Run(ctx context.Context) error {
	retry := s.cfg.Scheduler.EffectiveRetryInterval()
	timer := time.NewTimer(retry)
	defer timer.Stop()
	if !timer.Stop() {
		<-timer.C
	}

	for {
		if ctx.Err() != nil {
			return nil
		}

		acquired, err := s.leadOnce(ctx)
		switch {
		case err != nil && !errx.IsPureAbort(err):
			// 一轮失败不终止 worker：调度器非 required，Redis 抖动应当重试而不是
			// 拖垮进程。真正的终止条件只有 ctx 取消。
			s.observer.roundFailed(s.name, err)
		case acquired:
			// 刚失去租约（或正在关停）：立即重试一次，让接管尽快发生。
			continue
		}

		timer.Reset(retry)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
	}
}

// leadOnce 尝试成为 leader 并在持有租约期间运行调度器。
//
// 返回 acquired 表示本轮是否真的持有过租约——用于区分"没抢到，等一会儿再试"
// 与"当过 leader 但失去了，应立即重试"。
func (s *Scheduler) leadOnce(ctx context.Context) (acquired bool, err error) {
	held, err := s.lease.Acquire(ctx)
	switch {
	case errors.Is(err, singleton.ErrNotAcquired):
		s.observer.leaseSkipped(s.name)
		return false, nil
	case err != nil:
		return false, fmt.Errorf("acquire lease: %w", err)
	}

	s.observer.leaseAcquired(s.name, held.Fence)

	// 释放必须发生在所有返回路径上，且不能被关停的 ctx 取消——
	// 主动释放让接管的实例立即可用，而不是等一个完整的租期，
	// 那段时间里没有任何实例在调度。
	defer func() {
		releaseCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx), s.cfg.Scheduler.EffectiveLeaseTTL())
		defer cancel()
		if releaseErr := s.lease.Release(releaseCtx, held); releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("release lease: %w", releaseErr))
		}
	}()

	leadCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		s.keepAlive(leadCtx, cancel, held)
	}()
	// keepAlive 必须在返回前退出：它持有 cancel 并会调用它，
	// 让它活过本轮意味着下一轮的 ctx 可能被上一轮的续租失败取消。
	defer func() {
		cancel()
		<-renewDone
	}()

	inner, buildErr := s.build()
	if buildErr != nil {
		return true, buildErr
	}
	if regErr := s.register(leadCtx, inner); regErr != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(
			context.WithoutCancel(ctx), s.cfg.EffectiveShutdownTimeout())
		defer shutdownCancel()
		shutdownErr := inner.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			shutdownErr = fmt.Errorf("close unregistered scheduler: %w", shutdownErr)
		}
		return true, errors.Join(regErr, shutdownErr)
	}

	// Run 在 leadCtx 取消后触发关闭并返回。取消来自两处：进程关停，或续租失败。
	if runErr := inner.Run(leadCtx); runErr != nil && !errx.IsPureAbort(runErr) {
		return true, fmt.Errorf("scheduler run: %w", runErr)
	}
	return true, nil
}

// keepAlive 周期续租，续租失败即取消 leadCtx，从而停止调度器。
//
// 续租周期取租期的 1/3：留出两次重试的余量，一次抖动不会立刻让出租约。
func (s *Scheduler) keepAlive(ctx context.Context, cancel context.CancelFunc, held singleton.Held) {
	ttl := s.cfg.Scheduler.EffectiveLeaseTTL()
	interval := ttl / 3
	if interval <= 0 {
		interval = time.Millisecond
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		renewCtx, renewCancel := context.WithTimeout(context.WithoutCancel(ctx), interval)
		err := s.lease.Renew(renewCtx, held)
		renewCancel()
		if err == nil {
			continue
		}

		// 失去租约（或无法确认仍持有）必须立刻停止调度：继续调度意味着
		// 与新 leader 同时投递，而那正是选主要消除的情况。
		s.observer.leaseLost(s.name, err)
		cancel()
		return
	}
}

// Cleanup 释放调度器持有的资源。幂等。
//
// 租约的释放发生在 leadOnce 的 defer 里——那里才知道本轮持有的是哪个 token，
// 而用别的 token 释放会删掉新 leader 的锁。这里只做幂等收尾。
func (s *Scheduler) Cleanup(_ context.Context) error {
	s.released.Do(func() {})
	return nil
}
