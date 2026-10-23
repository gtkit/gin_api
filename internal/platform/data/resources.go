package dao

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// DatabaseHandle is the narrow database capability consumed by Resources.
// It deliberately has no Close method; the concrete owner controls closure.
type DatabaseHandle interface {
	DB() *gorm.DB
	SQLDB() *sql.DB
	PingContext(context.Context) error
}

// RedisHandle is the narrow Redis capability consumed by Resources.
// It deliberately has no Close method; the concrete owner controls closure.
type RedisHandle interface {
	GetClient(int) (*redis.Client, bool)
	PoolStats() map[int]*redis.PoolStats
	HealthCheck(context.Context) error
}

// RedisClientProvider supplies borrowed clients without transferring closure.
type RedisClientProvider interface {
	Client(int) *redis.Client
	DefaultClient() *redis.Client
	Configured() bool
}

// RedisProvider combines the client and observation capabilities used at startup.
type RedisProvider interface {
	RedisClientProvider
	RedisHandle
}

// Resources is a borrowed, immutable view over initialized clients.
// Runtime retains their owners; this view has no Close method.
type Resources struct {
	mysqlView       DatabaseHandle
	redis           RedisHandle
	redisConfigured bool
	redisState      *AvailabilityTracker
}

// ResourcesFromClients borrows the initialized MySQL and Redis clients.
// It owns availability tracking, but never closes the borrowed clients.
func ResourcesFromClients(
	mysql DatabaseHandle,
	redis RedisHandle,
	redisConfigured bool,
) (*Resources, error) {
	if mysql == nil || mysql.DB() == nil {
		return nil, errors.New("resources: mysql client is required")
	}
	var redisState *AvailabilityTracker
	_, recoverable := redis.(interface{ ConfigureTracing() error })
	if redis != nil && len(redis.PoolStats()) == 0 && redisConfigured && recoverable {
		tracker, err := newRecoverableDegradedTracker("redis", DefaultAvailabilityPolicy(), redis.HealthCheck)
		if err != nil {
			return nil, fmt.Errorf("resources: build recovering redis tracker: %w", err)
		}
		redisState = tracker
	} else if redis == nil || len(redis.PoolStats()) == 0 {
		redis = nil
		redisState = NewPermanentlyDegradedTracker("redis")
	} else {
		tracker, err := NewAvailabilityTracker("redis", DefaultAvailabilityPolicy(), redis.HealthCheck)
		if err != nil {
			return nil, fmt.Errorf("resources: build redis availability tracker: %w", err)
		}
		redisState = tracker
	}

	return &Resources{
		mysqlView:       mysql,
		redis:           redis,
		redisConfigured: redisConfigured,
		redisState:      redisState,
	}, nil
}

// Mdb 返回 GORM 句柄。
func (r *Resources) Mdb() *gorm.DB {
	if r == nil {
		return nil
	}
	if r.mysqlView == nil {
		return nil
	}
	return r.mysqlView.DB()
}

// Rdb 返回指定 DB 的 go-redis 客户端；该 DB 未初始化或 Redis 不可用时返回 nil。
//
// 返回 nil 而不是报错，与调用点的既有形态一致；可用性判断请用 RedisAvailability。
func (r *Resources) Rdb(db int) *redis.Client {
	if r == nil || r.redis == nil {
		return nil
	}
	client, ok := r.redis.GetClient(db)
	if !ok {
		return nil
	}
	return client
}

// RedisConfigured 报告配置里是否给了 Redis 地址。
//
// 与"客户端是否可用"是两件事，消费方必须分开处置（resource-lifecycle spec）：
//
//	未配置        → 声明依赖 Redis 的能力属**配置矛盾**，应当启动失败；
//	已配置但不可达 → 属运行期故障，可降级的能力应当降级而不是阻断启动。
//
// 两者合并成"客户端为空"会让所有降级路径在它们最该生效的场景里不可达。
func (r *Resources) RedisConfigured() bool {
	if r == nil {
		return false
	}
	return r.redisConfigured
}

// RedisAvailability 返回 Redis 的可用性跟踪器。
//
// 已构造资源视图中永不为 nil；可恢复 Owner 从 Degraded 开始接受真实观测。
func (r *Resources) RedisAvailability() *AvailabilityTracker {
	if r == nil {
		return nil
	}
	return r.redisState
}

// PingRedis 执行一次 Redis 健康检查并把结果计入可用性状态。
//
// 可恢复 Owner 的真实结果推进状态；真正缺失的句柄保持永久降级。
func (r *Resources) PingRedis(ctx context.Context) error {
	if r == nil || r.redis == nil {
		return errors.New("redis is not available in this process")
	}
	err := r.redis.HealthCheck(ctx)
	r.redisState.Observe(err)
	return err
}

// PingDB 执行一次数据库探活。
func (r *Resources) PingDB(ctx context.Context) error {
	if r == nil || r.mysqlView == nil {
		return errors.New("mysql connection is not available")
	}
	return r.mysqlView.PingContext(ctx)
}
