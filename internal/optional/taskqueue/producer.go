// Package taskqueue 装配异步任务队列（asynq）的生产端与消费端。
//
// 队列是有状态的基础设施：一旦开始入队，任务就必须有人消费。因此本包默认
// 关闭，且两端**同一个开关**——只开生产端会攒下没人处理的积压，只开消费端
// 则是一个空转的 worker 占着并发配额。
//
// 消费端提供 Prepare / Run / Cleanup 三段，由装配层的 adapter 补上关停分类
// 后接入进程的 worker 管理器。因此它自动获得既有保证——关停排序（消费者在
// HTTP 之后 drain）、Prepare 失败即启动失败、未预期退出必须被上报——
// 而不是再造一套生命周期。本包因此不依赖装配层。
package taskqueue

import (
	"context"
	"errors"
	"fmt"

	"github.com/gtkit/asynqx"
	"github.com/redis/go-redis/v9"
)

// Producer 是任务入队端。
//
// 关闭态下 Enqueue 返回 ErrDisabled 而不是静默丢弃：调用方以为任务已排队、
// 实际什么都没发生，是这类能力最难查的失效形态。
type Producer struct {
	inner   *asynqx.Producer
	enabled bool
}

// ErrDisabled 表示任务队列未启用。
var ErrDisabled = errors.New("tasks: queue is disabled")

// NewProducer 按配置构造入队端。
//
// client 提供 Redis 连接。复用进程已有的客户端而不是让 asynq 自己建一套：
// 两套连接池对同一个 Redis，池上限与监控口径都会分叉。
func NewProducer(cfg Config, client redis.UniversalClient) (*Producer, error) {
	if !cfg.Enabled {
		return &Producer{}, nil
	}
	if client == nil {
		return nil, errors.New("tasks: producer requires a redis client")
	}

	inner, err := asynqx.NewProducer(
		asynqx.WithRedisInstance(client),
		// 任务超时属**投递端**：asynqx 在 Enqueue 时把它注入任务选项。
		// 配到 Worker 上不生效——配置看起来生效了，实际投递仍用库默认值。
		asynqx.WithDefaultTaskTimeout(cfg.EffectiveTaskTimeout()),
		asynqx.WithShutdownTimeout(cfg.EffectiveShutdownTimeout()),
	)
	if err != nil {
		return nil, fmt.Errorf("tasks: build producer: %w", err)
	}
	return &Producer{inner: inner, enabled: true}, nil
}

// Enabled 报告队列是否启用。
func (p *Producer) Enabled() bool { return p != nil && p.enabled }

// Enqueue 入队一个任务。
//
// 默认带上任务超时：不设的话一个卡住的处理器会永久占用一个并发位，
// 积压随之增长而没有任何一处报错。
func (p *Producer) Enqueue(
	ctx context.Context, taskType string, payload any, opts ...asynqx.TaskOption,
) error {
	if !p.Enabled() {
		return fmt.Errorf("%w: cannot enqueue %q", ErrDisabled, taskType)
	}
	if _, err := p.inner.Enqueue(ctx, taskType, payload, opts...); err != nil {
		return fmt.Errorf("tasks: enqueue %q: %w", taskType, err)
	}
	return nil
}

// Close 释放入队端。幂等。等价于 Shutdown(context.Background())。
func (p *Producer) Close() error {
	return p.Shutdown(context.Background())
}

// Shutdown 在 ctx 约束内释放入队端。幂等。
//
// ctx 感知是关停契约的一部分：Close 走 context.Background()，在途投递的等待
// 因此切不断——第二个信号到达时进程仍要在这里干等。关停序列传入的是
// "只被强制信号取消"的 ctx，取消只解除本次等待，底层释放仍由 asynqx 后台完成。
func (p *Producer) Shutdown(ctx context.Context) error {
	if p == nil || p.inner == nil {
		return nil
	}
	return p.inner.Shutdown(ctx)
}
