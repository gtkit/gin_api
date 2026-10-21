package dao

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	logger "github.com/gtkit/logger/v2"

	"github.com/gtkit/gin-api/internal/platform/config"

	"github.com/gtkit/ormx"
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

// Budget 是资源装配的两级时间预算。
//
// 两级缺一不可：只有单资源超时，N 个资源串行就是 N 倍的最坏启动时间，
// 编排器的启动探针会先超时；只有总预算，单个卡死的资源会把预算全部吃掉，
// 后面的资源连一次尝试机会都没有，错误信息也就指不到真正的卡点。
type Budget struct {
	// Total 覆盖全部资源的建立与校验。
	Total time.Duration
	// PerResource 是单个资源的上限；实际取 min(PerResource, 总预算剩余)。
	PerResource time.Duration
}

const (
	// DefaultBudgetTotal 是资源装配的默认总预算。
	DefaultBudgetTotal = 30 * time.Second
	// DefaultBudgetPerResource 是单个资源的默认上限。
	DefaultBudgetPerResource = 10 * time.Second
)

// DefaultBudget 返回默认预算。
func DefaultBudget() Budget {
	return Budget{Total: DefaultBudgetTotal, PerResource: DefaultBudgetPerResource}
}

// normalize 用默认值填补未设置的预算项，并校验自洽。
func (b Budget) normalize() (Budget, error) {
	if b.Total <= 0 {
		b.Total = DefaultBudgetTotal
	}
	if b.PerResource <= 0 {
		b.PerResource = DefaultBudgetPerResource
	}
	if b.PerResource > b.Total {
		return Budget{}, fmt.Errorf(
			"resource budget: per-resource %s exceeds total %s", b.PerResource, b.Total)
	}
	return b, nil
}

// namedCloser 把资源名与其关闭动作绑在一起，让关闭错误能指名道姓。
type namedCloser struct {
	name  string
	close func() error
}

// Resources 持有进程级基础设施连接。
//
// 字段全部私有：连接句柄一旦可以被外部直接取走并各自持有，
// "谁负责关闭"就不再有唯一答案，而这正是资源泄漏的来源。
// 外部只能经方法拿到用于执行查询的句柄，关闭权只属于本类型。
type Resources struct {
	// mysql is retained for the legacy self-owning NewResources path.
	mysql     *ormx.Client
	mysqlView DatabaseHandle
	redis     RedisHandle
	// pluginOwned 表示连接由独立插件负责关闭；Resources 只提供 typed view。
	// 旧 NewResources 路径保持 false 并继续由 Resources 自己关闭。
	pluginOwned bool

	// redisConfigured 记录配置里是否给了 Redis 地址（见 RedisConfigured）。
	redisConfigured bool

	// redisState 永不为 nil：optional Redis 启动失败时是一个永久降级的跟踪器。
	// 用状态对象而不是 nil 表达"没有这个能力"，消费方就不必在每个点判空
	// （漏掉任何一处都是一次 panic）。
	redisState *AvailabilityTracker

	// closers 按**建立顺序**追加，Close 逆序执行。
	closers []namedCloser

	// closeMu + closed 保证 Close 幂等且并发安全：关停触发源不止一个
	// （信号、worker 异常、HTTP 异常），它们可能并发到达。
	// 重复 Close 底层连接池会返回错误，把一次正常关停染成失败。
	closeMu  sync.Mutex
	closed   bool
	closeErr error
}

// NewResources 建立并校验全部进程级资源。
//
// 失败时**本函数负责清理自己已经建立的部分**：已成功的资源按逆序关闭后再返回错误。
// 调用方拿到的是 nil，没有任何可关闭的句柄——把清理责任推给它等于要求它清理
// 一个它从未见过的东西。
//
// 全程不 panic：初始化失败是配置/环境问题，必须作为 error 沿装配链上抛，
// 由启动流程统一报告。panic 会绕过已建立资源的清理。
func NewResources(
	ctx context.Context,
	cfg *config.Config,
	creds config.DBCredentials,
	req Requirements,
	budget Budget,
) (*Resources, error) {
	if cfg == nil {
		return nil, errors.New("resources: config is required")
	}
	budget, err := budget.normalize()
	if err != nil {
		return nil, err
	}

	totalCtx, cancelTotal := context.WithTimeout(ctx, budget.Total)
	defer cancelTotal()

	res := &Resources{redisConfigured: strings.TrimSpace(cfg.Redis.Addr) != ""}

	// MySQL 恒为 required：失败即启动失败。
	mysqlCtx, cancelMySQL := budget.resourceContext(totalCtx)
	client, err := OpenDB(mysqlCtx, cfg.Database, creds, NewGormLogger(cfg))
	cancelMySQL()
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	res.mysql = client
	res.closers = append(res.closers, namedCloser{name: "mysql", close: client.Close})

	// Redis：required 时失败即启动失败；optional 时区分两种失败
	// （resource-lifecycle spec）：
	//
	//	**未配置**（addr 为空）→ 不建客户端，永久降级。依赖它的能力按配置
	//	矛盾处理（"声明用 redis 却没给地址"是配置错误，不是运行期故障）。
	//	**已配置但暂不可达** → optional 时保持永久降级至重启；
	//	底层构造器已回收失败连接，不能返回可自愈客户端。required 时启动失败。
	if strings.TrimSpace(cfg.Redis.Addr) == "" {
		res.redisState = NewPermanentlyDegradedTracker("redis")
		if req.Redis {
			return nil, errors.Join(
				errors.New("open redis: redis.addr is empty but an enabled capability requires redis"),
				res.Close())
		}
		return res, nil
	}

	redisCtx, cancelRedis := budget.resourceContext(totalCtx)
	rdb, redisErr := openRedis(redisCtx, cfg.Redis)
	cancelRedis()
	switch {
	case redisErr == nil:
		res.redis = rdb
		res.closers = append(res.closers, namedCloser{name: "redis", close: rdb.Close})
		tracker, trackerErr := NewAvailabilityTracker(
			"redis", DefaultAvailabilityPolicy(),
			func(probeCtx context.Context) error { return rdb.HealthCheck(probeCtx) },
		)
		if trackerErr != nil {
			// 策略非法属编程错误，但仍走清理路径而不是 panic。
			return nil, errors.Join(fmt.Errorf("build redis availability tracker: %w", trackerErr), res.Close())
		}
		res.redisState = tracker

	case req.Redis:
		// required 资源失败：先清理已建立的部分，再上抛。
		return nil, errors.Join(fmt.Errorf("open redis: %w", redisErr), res.Close())

	default:
		// optional 资源启动失败**保持降级到进程重启**：此时没有客户端可探测
		//（redisx 在构造期就回收了它，见 openRedis），也不做 client 热替换。
		//
		// 消费方靠 RedisConfigured() 区分这里与"未配置"：前者是运行期故障，
		// 可降级的能力应当降级；后者是配置矛盾，应当启动失败。
		logger.Warnf("redis is configured but unreachable at startup; capabilities that can "+
			"degrade will do so for this process lifetime (restart after redis recovers): %v", redisErr)
		res.redisState = NewPermanentlyDegradedTracker("redis")
	}

	return res, nil
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
	redisState *AvailabilityTracker,
) (*Resources, error) {
	if mysql == nil || mysql.DB() == nil {
		return nil, errors.New("resources: mysql client is required")
	}
	if redisState == nil {
		if redis == nil {
			redisState = NewPermanentlyDegradedTracker("redis")
		} else {
			tracker, err := NewAvailabilityTracker(
				"redis", DefaultAvailabilityPolicy(),
				redis.HealthCheck,
			)
			if err != nil {
				return nil, fmt.Errorf("resources: build redis availability tracker: %w", err)
			}
			redisState = tracker
		}
	}
	return &Resources{
		mysqlView:       mysql,
		redis:           redis,
		redisConfigured: redisConfigured,
		redisState:      redisState,
		pluginOwned:     true,
	}, nil
}

// resourceContext 返回单资源的执行 ctx：取 min(PerResource, 总预算剩余)。
//
// 不能只用 PerResource：总预算已经快耗尽时，再给单个资源一个完整的
// PerResource 会直接冲破总预算。
func (b Budget) resourceContext(parent context.Context) (context.Context, context.CancelFunc) {
	limit := b.PerResource
	if deadline, ok := parent.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < limit {
			limit = remaining
		}
	}
	if limit <= 0 {
		// 预算已耗尽：返回一个立即到期的 ctx，让下一个资源以"超时"失败，
		// 而不是以一个看不出原因的连接错误失败。
		return context.WithDeadline(parent, time.Now().Add(-time.Nanosecond))
	}
	return context.WithTimeout(parent, limit)
}

// Mdb 返回 GORM 句柄。
func (r *Resources) Mdb() *gorm.DB {
	if r == nil {
		return nil
	}
	if r.mysqlView != nil {
		return r.mysqlView.DB()
	}
	if r.mysql == nil {
		return nil
	}
	return r.mysql.DB()
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
	if r != nil && r.mysqlView != nil {
		return r.mysqlView.PingContext(ctx)
	}
	mdb := r.Mdb()
	if mdb == nil {
		return errors.New("mysql connection is not available")
	}
	sqlDB, err := mdb.DB()
	if err != nil {
		return fmt.Errorf("get sql.DB: %w", err)
	}
	return sqlDB.PingContext(ctx)
}

// Close 按**建立的逆序**释放全部资源，幂等。
//
// 逆序：后建立的资源可能依赖先建立的，正序关闭会让后者在依赖已消失的情况下收尾。
// 幂等：`defer res.Close()` 与显式关停路径同时存在是常态，重复调用底层连接池
// 会返回 "already closed" 之类的错误，把一次正常关停染成失败。
//
// 单个资源关闭失败不阻断其余资源——否则第一个失败就会让后面的全部泄漏。
// 全部错误聚合返回。
func (r *Resources) Close() error {
	if r == nil {
		return nil
	}
	if r.pluginOwned {
		return nil
	}

	r.closeMu.Lock()
	defer r.closeMu.Unlock()
	if r.closed {
		return r.closeErr
	}
	r.closed = true

	var errs []error
	for _, c := range slices.Backward(r.closers) {
		if c.close == nil {
			continue
		}
		if err := c.close(); err != nil {
			errs = append(errs, fmt.Errorf("close %s: %w", c.name, err))
		}
	}
	r.closeErr = errors.Join(errs...)
	return r.closeErr
}
