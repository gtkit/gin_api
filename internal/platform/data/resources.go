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
// It deliberately has no Close method; the lifecycle plugin owns closure.
type DatabaseHandle interface {
	DB() *gorm.DB
	SQLDB() *sql.DB
	PingContext(context.Context) error
}

// RedisHandle is the narrow Redis capability consumed by Resources.
// It deliberately has no Close method; the lifecycle plugin owns closure.
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

// Requirements 声明本次装配对**可选**资源的必需性。
//
// MySQL 不设开关：本脚手架的每条业务路径都落库，"MySQL 可选"没有对应的运行形态，
// 给它一个开关只会让人误以为存在无库运行模式。
//
// Redis 的必需性由**已启用的能力集合**推导，而不是由一个独立的开关配置——
// 独立开关必然与真实依赖漂移：有人打开了依赖 Redis 的能力却忘了把开关也打开，
// 于是 Redis 挂掉时进程照常报告 ready，而那个能力已经不工作了。
type Requirements struct {
	// Redis 为 true 时，Redis 连接失败即启动失败；
	// 为 false 时连接失败只让该能力进入永久降级，进程继续启动。
	Redis bool
}

// Resources is a non-owning, immutable view over initialized plugin capabilities.
// The registry owns resource lifetimes; closing this view never closes clients.
type Resources struct {
	mysqlView       DatabaseHandle
	redis           RedisHandle
	redisConfigured bool
	redisState      *AvailabilityTracker
}

// ResourcesFromClients creates a non-owning view over plugin-managed clients.
//
// The view is used by existing bootstrap and repository assembly code so the
// refactor does not widen business dependencies. MySQL and Redis remain owned
// by their respective lifecycle plugins and are not closed by this value.
func ResourcesFromClients(
	mysql DatabaseHandle,
	redis RedisHandle,
	redisConfigured bool,
) (*Resources, error) {
	if mysql == nil || mysql.DB() == nil {
		return nil, errors.New("resources: mysql client is required")
	}
	var redisState *AvailabilityTracker
	if redis == nil || len(redis.PoolStats()) == 0 {
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
// 永不为 nil（启动失败时是永久降级的跟踪器），因此消费方不需要判空。
func (r *Resources) RedisAvailability() *AvailabilityTracker {
	if r == nil {
		return nil
	}
	return r.redisState
}

// PingRedis 执行一次 Redis 健康检查并把结果计入可用性状态。
//
// Redis 从未建立时直接返回错误，且不影响永久降级状态。
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

// Close is retained for existing callers. Resource ownership stays in plugins.
func (*Resources) Close() error { return nil }
