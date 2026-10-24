package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/gtkit/asynqx"
	"github.com/redis/go-redis/v9"

	"github.com/gtkit/gin-api/internal/pkg/errx"
	"github.com/gtkit/gin-api/internal/platform/config"
)

// PeriodicTask 声明一条周期任务。TaskType 必须有对应的队列处理器。
// 队列可能重试投递，处理器仍需按业务要求保证幂等。
type PeriodicTask struct {
	Spec     string // cron 表达式，也支持 "@every 1h" 等描述符。
	TaskType string
	Payload  any
	Options  []asynqx.TaskOption
}

func (t PeriodicTask) validate() error {
	var errs []error
	if strings.TrimSpace(t.Spec) == "" {
		errs = append(errs, errors.New("spec must not be empty"))
	}
	if strings.TrimSpace(t.TaskType) == "" {
		errs = append(errs, errors.New("task type must not be empty"))
	}
	if len(errs) > 0 {
		return fmt.Errorf("tasks: periodic task %q: %w", t.TaskType, errors.Join(errs...))
	}
	return nil
}

// Scheduler 在当前进程内按 cron 投递任务。
// Prepare 完成注册，Run 启动一次；管理器在 Run 退出后调用 Cleanup。
type Scheduler struct {
	cfg       config.TasksConfig
	client    redis.UniversalClient
	name      string
	entries   []PeriodicTask
	newRunner func() (schedulerRunner, error)

	prepareOnce sync.Once
	prepareErr  error
	mu          sync.Mutex
	current     schedulerRunner
	started     bool
}

// Shutdown 可重复等待同一停止过程；错误表示等待被 ctx 中止。
type schedulerRunner interface {
	Register(context.Context, string, string, any, ...asynqx.TaskOption) (string, error)
	Start(context.Context) error
	Shutdown(context.Context) error
}

var _ schedulerRunner = (*asynqx.Scheduler)(nil)

// NewScheduler 在队列或调度器未启用、或没有周期任务时返回 nil。
// 构造不连接 Redis；cron、payload 和投递选项在 Prepare 中校验。
func NewScheduler(
	cfg config.TasksConfig, client redis.UniversalClient, name string, entries ...PeriodicTask,
) (*Scheduler, error) {
	if !cfg.Enabled || !cfg.Scheduler.Enabled || len(entries) == 0 {
		return nil, nil //nolint:nilnil // 未启用时返回 nil 调度器是正常路径。
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
	s := &Scheduler{cfg: cfg, client: client, name: name, entries: entries}
	s.newRunner = s.build
	return s, nil
}

func (s *Scheduler) Name() string { return s.name }

// Prepare 创建并注册一个底层实例，供 Run 直接使用。
// 注册失败时先回收实例；重复调用返回同一准备结果，不重复注册。
func (s *Scheduler) Prepare(ctx context.Context) error {
	s.prepareOnce.Do(func() {
		if err := ctx.Err(); err != nil {
			s.prepareErr = err
			return
		}
		inner, err := s.newRunner()
		if err != nil {
			s.prepareErr = err
			return
		}
		s.mu.Lock()
		s.current = inner
		s.mu.Unlock()
		if err := s.register(ctx, inner); err != nil {
			s.prepareErr = errors.Join(err, s.shutdown(ctx, inner))
		}
	})
	return s.prepareErr
}

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

func (s *Scheduler) register(ctx context.Context, inner schedulerRunner) error {
	for _, entry := range s.entries {
		if _, err := inner.Register(ctx, entry.Spec, entry.TaskType, entry.Payload, entry.Options...); err != nil {
			return fmt.Errorf("tasks: register periodic task %q (spec %q): %w", entry.TaskType, entry.Spec, err)
		}
	}
	return nil
}

// Run 启动调度并阻塞至 ctx 取消，返回前等待底层停止。
func (s *Scheduler) Run(ctx context.Context) (err error) {
	if err := s.Prepare(ctx); err != nil {
		if ctx.Err() != nil && errx.IsPureCancellation(err) {
			return nil
		}
		return err
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("tasks: scheduler Run may only be called once")
	}
	s.started = true
	inner := s.current
	s.mu.Unlock()
	defer func() {
		err = errors.Join(err, s.shutdown(ctx, inner))
		if ctx.Err() != nil && errx.IsPureCancellation(err) {
			err = nil
		}
	}()
	if err := inner.Start(ctx); err != nil {
		return fmt.Errorf("tasks: start scheduler: %w", err)
	}
	<-ctx.Done()
	return nil
}

// 首次等待超时后继续等待同一实例，避免管理器提前释放共享 Redis。
func (s *Scheduler) shutdown(ctx context.Context, inner schedulerRunner) error {
	waitCtx := context.WithoutCancel(ctx)
	shutdownCtx, cancel := context.WithTimeout(waitCtx, s.cfg.EffectiveShutdownTimeout())
	err := inner.Shutdown(shutdownCtx)
	cancel()
	if err == nil {
		return nil
	}
	err = fmt.Errorf("tasks: scheduler shutdown incomplete: %w", err)
	if waitErr := inner.Shutdown(waitCtx); waitErr != nil {
		return errors.Join(err, fmt.Errorf("tasks: wait for scheduler shutdown: %w", waitErr))
	}
	return err
}

// Cleanup 按调用方预算等待同一实例停止；超时后仍可再次等待。
func (s *Scheduler) Cleanup(ctx context.Context) error {
	s.mu.Lock()
	inner := s.current
	s.mu.Unlock()
	if inner == nil {
		return nil
	}
	if err := inner.Shutdown(ctx); err != nil {
		return fmt.Errorf("tasks: cleanup scheduler: %w", err)
	}
	return nil
}
