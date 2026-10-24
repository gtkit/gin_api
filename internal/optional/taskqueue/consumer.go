package taskqueue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gtkit/asynqx"
	"github.com/redis/go-redis/v9"

	"github.com/gtkit/gin-api/internal/pkg/metrics"
)

// Registrar 在装配期注册任务处理器。
//
// 处理器集合是启动期事实：运行期注册会让"这个任务类型有没有人处理"
// 失去确定答案，而漏注册的表现是任务入队后一直重试到进入死信队列。
type Registrar func(worker *asynqx.Worker) error

// Consumer 是队列消费端的三段生命周期实现。
//
// 它不自己实现调度与关停排序，而是把 Prepare / Run / Cleanup 交给装配层的
// worker 管理器——那套契约已经处理了关停排序、启动失败上报，以及"长期任务
// 未预期退出必须被上报"。
type Consumer struct {
	cfg      Config
	client   redis.UniversalClient
	name     string
	handlers []Registrar
	recorder *metrics.Recorder

	worker *asynqx.Worker
	once   sync.Once
}

// NewConsumer 构造消费端。
//
// handlers 为空时返回 nil：没有处理器的消费者是一个空转的 worker，
// 注册它只会让关停多等一轮、让监控里多一个永远空闲的实例。
func NewConsumer(
	cfg Config, client redis.UniversalClient, name string, handlers ...Registrar,
) *Consumer {
	return NewObservedConsumer(cfg, client, name, nil, handlers...)
}

// NewObservedConsumer records each processing attempt under the configured
// consumer name. Task IDs, payloads and arbitrary task types are not labels.
func NewObservedConsumer(
	cfg Config, client redis.UniversalClient, name string, recorder *metrics.Recorder, handlers ...Registrar,
) *Consumer {
	if !cfg.Enabled || len(handlers) == 0 {
		return nil
	}
	return &Consumer{cfg: cfg, client: client, name: name, handlers: handlers, recorder: recorder}
}

// Name 返回低基数标识。
func (c *Consumer) Name() string { return c.name }

// Prepare 建立 worker 并注册全部处理器。
//
// 注册放在 Prepare 而不是 Run：处理器注册失败属装配错误，必须表现为启动失败，
// 而不是让实例起来之后才发现某个任务类型没人处理。
func (c *Consumer) Prepare(ctx context.Context) error {
	if c.client == nil {
		return errors.New("tasks: consumer requires a redis client")
	}

	worker, err := asynqx.NewWorker(
		asynqx.WithRedisInstance(c.client),
		asynqx.WithConcurrency(c.cfg.EffectiveConcurrency()),
		asynqx.WithQueues(c.cfg.EffectiveQueues()),
		asynqx.WithShutdownTimeout(c.cfg.EffectiveShutdownTimeout()),
		asynqx.WithMiddleware(executionMetrics(c.recorder, c.name)),
	)
	if err != nil {
		return fmt.Errorf("tasks: build worker: %w", err)
	}

	for _, register := range c.handlers {
		if err := register(worker); err != nil {
			// 已建的 worker 必须关掉：asynqx 的 NewWorker 即使从未 Start
			// 也持有底层执行器，装配失败时不关就是一次泄漏（而这条路径
			// 恰恰以启动失败告终，没有第二次机会）。
			shutdownCtx, cancel := context.WithTimeout(
				context.WithoutCancel(ctx), c.cfg.EffectiveShutdownTimeout())
			defer cancel()
			return errors.Join(
				fmt.Errorf("tasks: register handler: %w", err), worker.Shutdown(shutdownCtx))
		}
	}
	c.worker = worker
	return nil
}

// Run 阻塞消费直到 ctx 取消。
func (c *Consumer) Run(ctx context.Context) error {
	if c.worker == nil {
		return errors.New("tasks: Run called before Prepare")
	}
	if err := c.worker.Run(ctx); err != nil {
		return fmt.Errorf("tasks: worker run: %w", err)
	}
	return nil
}

// Cleanup 关闭 worker。幂等（契约要求）。
//
// 预算是自己的（drain 在处理中的任务需要时间：它们要么完成、要么被 asynq
// 重新入队，直接掐断则两者都不发生），但**不再剥离调用方的取消**：
// 传进来的 ctx 已由 worker 管理器构造成"只被强制信号取消"的形态，
// 在这里二次剥离等于把第二个信号挡在门外——而强制路径的前提正是等不下去了。
func (c *Consumer) Cleanup(ctx context.Context) error {
	if c == nil || c.worker == nil {
		return nil
	}
	var err error
	c.once.Do(func() {
		budget, cancel := context.WithTimeout(ctx, c.cfg.EffectiveShutdownTimeout()+time.Second)
		defer cancel()
		err = c.worker.Shutdown(budget)
		c.worker = nil
	})
	if err != nil {
		return fmt.Errorf("tasks: worker shutdown: %w", err)
	}
	return nil
}
