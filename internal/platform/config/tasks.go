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
	// 调度器首次等待也使用此预算；超时会上报故障，仍跟踪同一实例直到真实退出。
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
	// RedisDB 是队列使用的 Redis 逻辑库号。
	//
	// 与业务缓存分库：asynq 会扫描并删除自己的键，与业务键共处一库时，
	// 一次误配的前缀或一次运维操作就会互相波及。
	RedisDB int `mapstructure:"redis_db"`
	// Scheduler 是周期任务调度（cron）的配置。
	Scheduler SchedulerConfig `mapstructure:"scheduler"`
}

// SchedulerConfig 是周期任务调度的配置。
//
// 只承载开关与租约参数。**触发时刻、任务类型与幂等保证在代码里声明**
// （见 internal/pkg/tasks 的 PeriodicTask）：把 cron spec 放配置里，
// 就可能出现"配置声明了一个任务，代码里没有对应处理器"的失配——
// 它的表现是任务按时入队、一直重试到进死信队列，而配置文件看起来完全正常。
type SchedulerConfig struct {
	// Enabled 是调度器开关。关闭时不构造调度器、不建租约、不注册 worker。
	//
	// 它独立于 tasks.enabled：调度器投递的任务需要有人消费，因此启用它
	// 要求队列本身也启用（见 validateTasksConfig）。反过来只开队列不开调度器
	// 是正常配置——大多数任务由业务代码触发，不需要 cron。
	Enabled bool `mapstructure:"enabled"`
	// LeaseName 是选主租约的名字，同一部署内的所有副本必须一致。
	//
	// 不一致的后果是每个副本各自持有一把"自己的锁"并同时调度，
	// 而每一把锁看起来都工作正常。
	LeaseName string `mapstructure:"lease_name"`
	// LeaseTTL 是租约时长。续租周期取其 1/3。
	//
	// 取值权衡：太短会在 Redis 抖动时频繁易主，每次易主都要重建调度器；
	// 太长会让 leader 崩溃后的空窗期变长——那段时间里没有任何实例在调度。
	LeaseTTL time.Duration `mapstructure:"lease_ttl"`
	// RetryInterval 是未抢到租约时的重试间隔。
	RetryInterval time.Duration `mapstructure:"retry_interval"`
	// UniqueTTL 是投递去重窗口，作用于声明了幂等保证的任务。
	// 周期任务只接受 GuaranteeIdempotent，当前消息链路不提供 fencing。
	//
	// 这是**第二道防线**，不是正确性保证：底层的唯一性锁在任务成功完成时
	// 就释放，不是维持整个 TTL。一个很快跑完的任务，配合实例间的时钟偏移，
	// 仍可能被投递两次——这正是每个周期任务都必须声明重复执行保证的原因。
	UniqueTTL time.Duration `mapstructure:"unique_ttl"`
}

// 调度器未配置时的兜底值。
//
// 租约取 2m、重试取 10s：与 internal/pkg/singleton 既有单实例任务的量级一致。
// 去重窗口取 1m：必须 ≥ 1s（底层要求），且应小于最密的触发间隔——
// 大于触发间隔会把两次**本应各自执行**的触发去重成一次。
const (
	schedulerDefaultLeaseTTL      = 2 * time.Minute
	schedulerDefaultRetryInterval = 10 * time.Second
	schedulerDefaultUniqueTTL     = time.Minute
	schedulerMinUniqueTTL         = time.Second
	// minSchedulerLeaseTTL 是租约时长的下限。
	//
	// 续租周期是租期的 1/3，因此 5s 的租期意味着每 1.67s 一次续租往返。
	// 低于这个量级时，一次正常的网络抖动就会被判成失去租约——而每次误判
	// 都会停掉调度器、释放租约、重新抢锁、重建调度器，代价远大于它防住的东西。
	minSchedulerLeaseTTL = 5 * time.Second
)

// EffectiveLeaseName 返回生效的租约名。
func (c SchedulerConfig) EffectiveLeaseName() string {
	if name := strings.TrimSpace(c.LeaseName); name != "" {
		return name
	}
	return "task-scheduler"
}

// EffectiveLeaseTTL 返回生效的租约时长。
func (c SchedulerConfig) EffectiveLeaseTTL() time.Duration {
	if c.LeaseTTL > 0 {
		return c.LeaseTTL
	}
	return schedulerDefaultLeaseTTL
}

// EffectiveRetryInterval 返回生效的抢锁重试间隔。
func (c SchedulerConfig) EffectiveRetryInterval() time.Duration {
	if c.RetryInterval > 0 {
		return c.RetryInterval
	}
	return schedulerDefaultRetryInterval
}

// EffectiveUniqueTTL 返回生效的去重窗口。
func (c SchedulerConfig) EffectiveUniqueTTL() time.Duration {
	if c.UniqueTTL > 0 {
		return c.UniqueTTL
	}
	return schedulerDefaultUniqueTTL
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

	errs = append(errs, validateSchedulerConfig(cfg.Scheduler)...)

	return errors.Join(errs...)
}

// validateSchedulerConfig 校验周期任务调度配置。
//
// 只在队列已启用的前提下调用（调用点在 validateTasksConfig 的 Enabled 分支内）：
// 调度器投递的任务由既有消费者处理，队列关着时它无处可投。
func validateSchedulerConfig(cfg SchedulerConfig) []error {
	if !cfg.Enabled {
		return nil
	}

	var errs []error

	// 负值一律拒绝，不静默回退：回退让"配了一个非法值"与"没配"变成同一种结果，
	// 而使用者以为自己配的那个值正在生效。
	if cfg.LeaseTTL < 0 || cfg.RetryInterval < 0 || cfg.UniqueTTL < 0 {
		errs = append(errs, errors.New(
			"tasks.scheduler: negative lease_ttl / retry_interval / unique_ttl are not allowed "+
				"(they silently fall back to defaults, so the configured value never takes effect)"))
	}

	// 去重窗口有下限：底层要求唯一性 TTL ≥ 1s，小于它的取值会让投递直接失败，
	// 而失败发生在触发时刻——没有人在那个时刻看着日志。
	if cfg.UniqueTTL > 0 && cfg.UniqueTTL < schedulerMinUniqueTTL {
		errs = append(errs, fmt.Errorf(
			"tasks.scheduler.unique_ttl %s is below the %s minimum required by the queue backend",
			cfg.UniqueTTL, schedulerMinUniqueTTL))
	}

	// 续租周期取租期的 1/3，租期过短会让续租过于频繁而毫无收益；
	// 更重要的是它必须显著大于一次 Redis 往返，否则正常延迟就会被误判成失去租约，
	// 表现为调度器反复易主、每次易主都重建一遍。
	if ttl := cfg.EffectiveLeaseTTL(); ttl < minSchedulerLeaseTTL {
		errs = append(errs, fmt.Errorf(
			"tasks.scheduler.lease_ttl %s is below the %s minimum: a lease this short is renewed "+
				"so often that ordinary redis latency reads as lease loss, and every false loss "+
				"rebuilds the scheduler", ttl, minSchedulerLeaseTTL))
	}

	return errs
}
