package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// TasksConfig 是异步任务队列（asynq）的进程级配置。
//
// 默认关闭。队列是有状态的基础设施：一旦开始入队，任务就必须有人消费——
// 母版替使用者打开它，只会在某个部署里留下一堆没人处理的积压。
type TasksConfig struct {
	// Enabled 是总开关。关闭时既不建 producer 也不注册 consumer worker。
	Enabled bool `mapstructure:"enabled"`
	// Concurrency 是单副本的并发处理数。
	Concurrency int `mapstructure:"concurrency"`
	// Queues 是队列名到权重的映射；为空时只消费 default 队列。
	Queues map[string]int `mapstructure:"queues"`
	// TaskTimeout 是单个任务的默认处理超时。
	TaskTimeout time.Duration `mapstructure:"task_timeout"`
	// ShutdownTimeout 是关停时等待在处理任务完成的预算。
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
	// RedisDB 是队列使用的 Redis 逻辑库号。
	//
	// 与业务缓存分库：asynq 会扫描并删除自己的键，与业务键共处一库时，
	// 一次误配的前缀或一次运维操作就会互相波及。
	RedisDB int `mapstructure:"redis_db"`
}

// 未配置时的兜底值。
//
// 并发取 10：远小于 GOMAXPROCS × 常见倍数，因为任务处理通常打数据库——
// 并发开大只会把压力从队列转移到数据库连接池，那里更难扩。
//
// 任务超时取 5m、关停预算取 25s：后者必须小于常见的容器 SIGKILL 宽限期（30s），
// 否则等 drain 的过程会被直接杀掉，正在处理的任务既没完成也没重新入队。
const (
	tasksDefaultConcurrency     = 10
	tasksDefaultTaskTimeout     = 5 * time.Minute
	tasksDefaultShutdownTimeout = 25 * time.Second
)

// maxTasksShutdownTimeout 是关停预算上限。
const maxTasksShutdownTimeout = 5 * time.Minute

// EffectiveConcurrency 返回生效的并发数。
func (c TasksConfig) EffectiveConcurrency() int {
	if c.Concurrency > 0 {
		return c.Concurrency
	}
	return tasksDefaultConcurrency
}

// EffectiveTaskTimeout 返回生效的单任务超时。
func (c TasksConfig) EffectiveTaskTimeout() time.Duration {
	if c.TaskTimeout > 0 {
		return c.TaskTimeout
	}
	return tasksDefaultTaskTimeout
}

// EffectiveShutdownTimeout 返回生效的关停预算。
func (c TasksConfig) EffectiveShutdownTimeout() time.Duration {
	if c.ShutdownTimeout > 0 {
		return c.ShutdownTimeout
	}
	return tasksDefaultShutdownTimeout
}

// EffectiveQueues 返回生效的队列权重表。
func (c TasksConfig) EffectiveQueues() map[string]int {
	if len(c.Queues) > 0 {
		return c.Queues
	}
	return map[string]int{"default": 1}
}

// validateTasksConfig 校验任务队列配置。
func validateTasksConfig(cfg TasksConfig, redis RedisConfig) error {
	if !cfg.Enabled {
		return nil
	}

	var errs []error

	// 队列必须有 Redis：没有它，入队会在运行期持续失败，而进程看起来正常。
	if strings.TrimSpace(redis.Addr) == "" {
		errs = append(errs, errors.New(
			"tasks are enabled but redis.addr is empty: the queue has no backing store"))
	}
	// redis_db 必须真的在 redis.dbs 里：不校验的话资源层取不到该库的客户端，
	// 装配拿到 nil 后按"redis 未配置"处理——表现为"配了队列却说没有 redis"，
	// 而配置文件看起来完全正常。
	if cfg.RedisDB < 0 {
		errs = append(errs, fmt.Errorf("tasks.redis_db %d must not be negative", cfg.RedisDB))
	} else if !slices.Contains(redis.DBs, cfg.RedisDB) {
		errs = append(errs, fmt.Errorf(
			"tasks.redis_db %d is not listed in redis.dbs %v: the resource layer only opens the "+
				"declared databases, so the queue would get no client at all",
			cfg.RedisDB, redis.DBs))
	}
	// 负值一律拒绝，不静默回退：回退让"配了一个非法值"与"没配"变成同一种
	// 结果，而使用者以为自己配的那个值正在生效。
	if cfg.Concurrency < 0 || cfg.TaskTimeout < 0 || cfg.ShutdownTimeout < 0 {
		errs = append(errs, errors.New(
			"tasks: negative concurrency / task_timeout / shutdown_timeout are not allowed "+
				"(they silently fall back to defaults, so the configured value never takes effect)"))
	}
	for name, weight := range cfg.Queues {
		if strings.TrimSpace(name) == "" {
			errs = append(errs, errors.New("tasks.queues: queue name must not be empty"))
			continue
		}
		// 权重为 0 的队列永远不会被取任务：入队成功、永不处理，
		// 而没有任何一处会提示这件事。
		if weight <= 0 {
			errs = append(errs, fmt.Errorf(
				"tasks.queues[%s] weight %d must be positive: a zero-weight queue accepts tasks "+
					"and never processes them", name, weight))
		}
	}
	if cfg.EffectiveShutdownTimeout() > maxTasksShutdownTimeout {
		errs = append(errs, fmt.Errorf(
			"tasks.shutdown_timeout %s exceeds the %s cap", cfg.EffectiveShutdownTimeout(), maxTasksShutdownTimeout))
	}

	return errors.Join(errs...)
}
