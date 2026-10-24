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
	// LeaseLost 在续租失败、发出停止请求时调用，不表示底层已经退出。
	LeaseLost func(scheduler string, err error)
	// RoundFailed 报告轮次故障，包括尚在等待真实退出的关闭超时。
	// 此时不能假定该轮已结束或下一轮已开始。
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
	// Guarantee 只接受 GuaranteeIdempotent：处理器必须能安全处理重复投递。
	// 租约不能消除易主窗口内的重复；当前消息链路不传递或校验 fence。
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
	switch t.Guarantee {
	case singleton.GuaranteeIdempotent:
	case singleton.GuaranteeUnspecified:
		errs = append(errs, errors.New(
			"guarantee must be declared as GuaranteeIdempotent: a lease does not make duplicate delivery safe"))
	case singleton.GuaranteeFenced:
		errs = append(errs, errors.New(
			"GuaranteeFenced is not supported for periodic tasks: the message and consumer do not carry or enforce a fence"))
	default:
		errs = append(errs, fmt.Errorf("unknown guarantee %d: periodic tasks require GuaranteeIdempotent", t.Guarantee))
	}
	if len(errs) > 0 {
		return fmt.Errorf("tasks: periodic task %q: %w", t.TaskType, errors.Join(errs...))
	}
	return nil
}

// Scheduler 按 cron 投递任务，使用 Redis 租约减少多副本重复调度。
// 租约自然过期或进程暂停时仍可能重叠，正确性依赖处理器的业务幂等。
type Scheduler struct {
	cfg     config.TasksConfig
	client  redis.UniversalClient
	name    string
	entries []PeriodicTask

	lease    *singleton.Lease
	observer SchedulerObserver

	newRunner func() (schedulerRunner, error)
	// 实例与代次只在短临界区内读写，Start/Shutdown 和观察器始终在锁外执行。
	mu         sync.Mutex
	generation uint64
	current    *schedulerInstance
}

// schedulerRunner 的 Shutdown 可重复等待同一停止过程；错误表示等待被 ctx 中止。
type schedulerRunner interface {
	Register(context.Context, string, string, any, ...asynqx.TaskOption) (string, error)
	Start(context.Context) error
	Shutdown(context.Context) error
}

var _ schedulerRunner = (*asynqx.Scheduler)(nil)

type schedulerInstance struct {
	runner     schedulerRunner
	generation uint64
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

	s := &Scheduler{
		cfg:      cfg,
		client:   client,
		name:     name,
		entries:  entries,
		lease:    lease,
		observer: observer,
	}
	s.newRunner = s.build
	return s, nil
}

// schedulerLeasePrefix 是租约键前缀。
const schedulerLeasePrefix = "tasks"

// Name 返回低基数标识。
func (s *Scheduler) Name() string { return s.name }

// Prepare 实测租约能力，并用临时实例验证任务注册与 cron 表达式。
// 临时实例退出后才返回；底层停止后无法重启，Run 每轮另建实例。
func (s *Scheduler) Prepare(ctx context.Context) (err error) {
	// 只 PING 不能证明当前 ACL 能执行脚本，也不能证明锁键与 fence 键
	// 在同一个集群 slot。用随机探测键真跑一遍抢锁/续租/释放。
	if err := singleton.Probe(ctx, s.client, schedulerLeasePrefix); err != nil {
		return fmt.Errorf("tasks: scheduler lease probe: %w", err)
	}

	probe, err := s.newRunner()
	if err != nil {
		return err
	}
	instance := s.hold(probe)
	// Prepare 失败也必须等本实例退出，不能把仍持有共享客户端的探针留给启动回滚。
	defer func() {
		err = errors.Join(err, s.shutdownInstance(ctx, instance))
	}()

	if err := s.register(ctx, probe); err != nil {
		return err
	}
	return nil
}

// build 构造一个新的底层调度器实例。
//
// 每次成为 leader 都要新建：底层实例停止后进入终态，再次启动返回"已停止"错误。
func (s *Scheduler) build() (schedulerRunner, error) {
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
func (s *Scheduler) register(ctx context.Context, inner schedulerRunner) error {
	for _, entry := range s.entries {
		// 唯一性锁在任务成功后即可释放，只是附加去重，不替代业务幂等。
		opts := append([]asynqx.TaskOption(nil), entry.Options...)
		opts = append(opts, asynqx.WithTaskUnique(s.cfg.Scheduler.EffectiveUniqueTTL()))
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
	timer.Stop()

	for {
		if ctx.Err() != nil {
			return nil
		}

		acquired, err := s.leadOnce(ctx)
		if ctx.Err() != nil {
			// 完整关闭才是正常结束；本轮排空超时或释放失败不得被父取消吞掉。
			if errx.IsPureCancellation(err) {
				return nil
			}
			return err
		}
		if err == nil && acquired {
			continue
		}

		timer.Reset(retry)
		select {
		case <-ctx.Done():
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
		err = fmt.Errorf("acquire lease: %w", err)
		s.observeRoundFailure(ctx, err)
		return false, err
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
			releaseErr = fmt.Errorf("release lease: %w", releaseErr)
			s.observeRoundFailure(ctx, releaseErr)
			err = errors.Join(err, releaseErr)
		}
	}()

	leadCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		s.keepAlive(leadCtx, cancel, held)
	}()
	stopRenewal := func() {
		cancel()
		<-renewDone
	}
	defer stopRenewal()

	inner, buildErr := s.newRunner()
	if buildErr != nil {
		s.observeRoundFailure(ctx, buildErr)
		return true, buildErr
	}
	instance := s.hold(inner)
	// 先结束续租，再排空底层实例；退出确认前保留本轮归属和原 token 的释放责任。
	defer func() {
		stopRenewal()
		err = errors.Join(err, s.shutdownInstance(ctx, instance))
	}()
	if err := s.register(leadCtx, inner); err != nil {
		s.observeRoundFailure(ctx, err)
		return true, err
	}
	if err := inner.Start(leadCtx); err != nil {
		err = fmt.Errorf("tasks: start scheduler: %w", err)
		s.observeRoundFailure(ctx, err)
		return true, err
	}

	<-leadCtx.Done()
	return true, nil
}

func (s *Scheduler) observeRoundFailure(ctx context.Context, err error) {
	if err != nil && (ctx.Err() == nil || !errx.IsPureCancellation(err)) {
		s.observer.roundFailed(s.name, err)
	}
}

func (s *Scheduler) hold(inner schedulerRunner) *schedulerInstance {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generation++
	instance := &schedulerInstance{runner: inner, generation: s.generation}
	s.current = instance
	return instance
}

func (s *Scheduler) forget(instance *schedulerInstance) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == instance && s.generation == instance.generation {
		s.current = nil
	}
}

// shutdownInstance 的首次预算只限制等待，超时后仍由当前 goroutine 跟踪真实停止。
func (s *Scheduler) shutdownInstance(ctx context.Context, instance *schedulerInstance) error {
	waitCtx := context.WithoutCancel(ctx)
	shutdownCtx, cancel := context.WithTimeout(waitCtx, s.cfg.EffectiveShutdownTimeout())
	shutdownErr := instance.runner.Shutdown(shutdownCtx)
	cancel()
	if shutdownErr == nil {
		s.forget(instance)
		return nil
	}

	shutdownErr = fmt.Errorf("tasks: scheduler shutdown incomplete: %w", shutdownErr)
	s.observer.roundFailed(s.name, shutdownErr)
	// asynqx 的 Shutdown 仅因等待 ctx 结束返回错误；无取消的再次等待要到真实退出。
	if waitErr := instance.runner.Shutdown(waitCtx); waitErr != nil {
		waitErr = fmt.Errorf("tasks: wait for scheduler shutdown: %w", waitErr)
		s.observer.roundFailed(s.name, waitErr)
		return errors.Join(shutdownErr, waitErr)
	}
	s.forget(instance)
	return shutdownErr
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

		cancel()
		s.observer.leaseLost(s.name, err)
		return
	}
}

// Cleanup 按调用方预算等待当前实例的同一停止过程；超时保留归属。
// 租约仍由 leadOnce 在真实退出并结束续租后，使用原 token 释放。
func (s *Scheduler) Cleanup(ctx context.Context) error {
	s.mu.Lock()
	instance := s.current
	s.mu.Unlock()
	if instance == nil {
		return nil
	}
	if err := instance.runner.Shutdown(ctx); err != nil {
		return fmt.Errorf("tasks: cleanup scheduler: %w", err)
	}
	s.forget(instance)
	return nil
}
