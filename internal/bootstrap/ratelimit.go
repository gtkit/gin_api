package bootstrap

import (
	"context"
	"errors"
	"fmt"

	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/gin-api/pkg/ratelimit"
	"go.uber.org/zap"

	logger "github.com/gtkit/logger/v2"
)

// rateLimitAssembly 是限流的装配产物。
type rateLimitAssembly struct {
	registry *ratelimit.Registry
	keys     *ratelimit.KeyBuilder
}

// buildRateLimits 把配置里的策略翻译成执行层注册表。
//
// 策略集合为空时返回空注册表。**这不等于"关闭限流"**：业务路由硬引用策略 ID，
// 引用无法解析会在装配期失败——脚手架无从判断部署入口有没有网关，
// 而"配置写了 ID 却不生效"比启动失败坏得多。
func buildRateLimits(
	ctx context.Context, cfg *config.Config, resources *data.Resources,
) (rateLimitAssembly, error) {
	if cfg == nil || len(cfg.RateLimit.Policies) == 0 {
		return rateLimitAssembly{}, nil
	}

	keys, err := ratelimit.NewKeyBuilder(cfg.RateLimit.KeyHMACSecret, cfg.App.Name)
	if err != nil {
		return rateLimitAssembly{}, fmt.Errorf("build rate limit key builder: %w", err)
	}

	registry := ratelimit.NewRegistry()

	// Redis backend 按需**惰性**建立且全进程共享：
	// 所有引用它的策略共享一份健康状态，否则同一次 Redis 故障会触发
	// N 份独立探测，对一个已经出问题的依赖形成探测风暴。
	//
	// 启动期 Redis 不可用时的行为由**最严格的策略**决定：存在任一
	// fail_closed 的 redis 策略即构造失败（那种模式下后端不可用只能拒绝
	// 请求，启动失败更诚实）；全部是降级模式（fail_open / local_fallback）
	// 时以 Degraded 相位启动，恢复探测把后端带回来——降级模式的意义
	// 就是可用性优先，后端故障期间连滚动重启都做不了与语义自相矛盾。
	var redisBackend *ratelimit.RedisBackend
	degradedStartAllowed := !redisRequiredForRateLimit(cfg)

	// 中途失败必须关闭已建执行体：本地策略的执行体各自持有后台清理 goroutine，
	// 直接丢弃注册表引用会让它们存活到进程退出。
	for _, raw := range cfg.RateLimit.Policies {
		policy, convErr := toRateLimitPolicy(raw)
		if convErr != nil {
			return rateLimitAssembly{}, errors.Join(convErr, registry.Close())
		}

		limiter, buildErr := buildLimiter(ctx, policy, resources, &redisBackend, cfg.App.Name, degradedStartAllowed)
		if buildErr != nil {
			return rateLimitAssembly{}, errors.Join(buildErr, registry.Close())
		}
		if regErr := registry.Register(limiter); regErr != nil {
			// 注册失败的执行体不在注册表内，必须在失败现场关闭——
			// 除此之外没有任何路径能回收它的后台 goroutine。
			limiter.Close()
			return rateLimitAssembly{}, errors.Join(
				fmt.Errorf("register rate limit policy: %w", regErr), registry.Close())
		}
	}

	// 冻结：策略集合是启动期事实，运行期可变会让"这条路由走哪条限额"
	// 失去确定答案，也让审计无从下手。
	registry.Freeze()

	logger.Infof("rate limiting enabled with %d policies: %v", len(registry.IDs()), registry.IDs())
	return rateLimitAssembly{registry: registry, keys: keys}, nil
}

// buildLimiter 按 Store 与 FailureMode 构造执行体。
//
// degradedStartAllowed 由策略集合整体推导（无任何 fail_closed redis 策略）：
// backend 全进程共享，行为只能取最严格的那档。
func buildLimiter(
	ctx context.Context,
	policy ratelimit.Policy,
	resources *data.Resources,
	redisBackend **ratelimit.RedisBackend,
	keyPrefix string,
	degradedStartAllowed bool,
) (ratelimit.Limiter, error) {
	if policy.Store == ratelimit.StoreLocal {
		return ratelimit.NewLocalBackend(policy)
	}

	if *redisBackend == nil {
		// Redis **未配置**（而非不可用）对任何 redis 策略都是配置矛盾，
		// 一律构造失败，降级启动不适用。
		client := resources.Rdb(0)
		if client == nil {
			return nil, fmt.Errorf(
				"rate limit policy %q uses the redis store but no redis client is available: "+
					"either configure redis or switch the policy to the local store", policy.ID)
		}
		backendOpts := []ratelimit.Option{
			ratelimit.WithKeyPrefix(keyPrefix),
			ratelimit.WithObserver(rateLimitObserver{}),
		}
		if degradedStartAllowed {
			backendOpts = append(backendOpts, ratelimit.WithDegradedStart())
		}
		backend, err := ratelimit.NewRedisBackend(ctx, client, backendOpts...)
		if err != nil {
			return nil, fmt.Errorf("build redis rate limit backend for %q: %w", policy.ID, err)
		}
		if backend.Health().Phase() == ratelimit.PhaseDegraded {
			logger.Warnf("redis rate limit backend started degraded (redis unavailable at startup); " +
				"degraded-mode policies apply their failure modes until the recovery probe succeeds")
		}
		*redisBackend = backend
	}

	if policy.FailureMode == ratelimit.FailureLocalFallback {
		return ratelimit.NewFallbackBackend(*redisBackend, policy)
	}
	return ratelimit.NewRedisLimiter(*redisBackend, policy)
}

// toRateLimitPolicy 把配置形态翻译成强类型策略。
func toRateLimitPolicy(raw config.RateLimitPolicyConfig) (ratelimit.Policy, error) {
	scope, err := parseScope(raw.Scope)
	if err != nil {
		return ratelimit.Policy{}, fmt.Errorf("rate limit policy %q: %w", raw.ID, err)
	}
	store, err := parseStore(raw.Store)
	if err != nil {
		return ratelimit.Policy{}, fmt.Errorf("rate limit policy %q: %w", raw.ID, err)
	}
	mode, err := parseFailureMode(raw.FailureMode)
	if err != nil {
		return ratelimit.Policy{}, fmt.Errorf("rate limit policy %q: %w", raw.ID, err)
	}

	policy := ratelimit.Policy{
		ID: raw.ID, Rate: raw.Rate, Burst: raw.Burst,
		Scope: scope, Store: store, FailureMode: mode, MaxKeys: raw.MaxKeys,
	}
	if err := policy.Validate(); err != nil {
		return ratelimit.Policy{}, err
	}
	return policy, nil
}

func parseScope(raw string) (ratelimit.Scope, error) {
	switch raw {
	case "ip":
		return ratelimit.ScopeIP, nil
	case "user":
		return ratelimit.ScopeUser, nil
	case "global":
		return ratelimit.ScopeGlobal, nil
	default:
		return ratelimit.ScopeUnspecified, fmt.Errorf("unknown scope %q", raw)
	}
}

func parseStore(raw string) (ratelimit.Store, error) {
	switch raw {
	case "local":
		return ratelimit.StoreLocal, nil
	case "redis":
		return ratelimit.StoreRedis, nil
	default:
		return ratelimit.StoreUnspecified, fmt.Errorf("unknown store %q", raw)
	}
}

func parseFailureMode(raw string) (ratelimit.FailureMode, error) {
	switch raw {
	case "fail_open":
		return ratelimit.FailureOpen, nil
	case "fail_closed":
		return ratelimit.FailureClosed, nil
	case "local_fallback":
		return ratelimit.FailureLocalFallback, nil
	default:
		return ratelimit.FailureUnspecified, fmt.Errorf("unknown failure mode %q", raw)
	}
}

// redisRequiredForRateLimit 报告是否存在必须让 Redis 成为 required 的限流策略。
//
// 判据是 `Store: redis` **且** `FailureMode: fail_closed`：那种组合下
// Redis 挂掉时该策略只能拒绝请求，实例不该被判为 ready。
//
// 其余组合（fail_open / local_fallback）只要求 Redis 已配置——它们本来
// 就有降级路径，把 Redis 抬成 required 会让一次可降级的故障变成实例被摘流。
func redisRequiredForRateLimit(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	for _, p := range cfg.RateLimit.Policies {
		if p.Store == "redis" && p.FailureMode == "fail_closed" {
			return true
		}
	}
	return false
}

// closeRateLimits 关闭注册表。
func closeRateLimits(a rateLimitAssembly) error {
	if a.registry == nil {
		return nil
	}
	if err := a.registry.Close(); err != nil {
		return errors.Join(errors.New("close rate limit registry"), err)
	}
	return nil
}

// rateLimitObserver 把限流包的运行期事件接到项目日志栈。
//
// 限流包自己不写日志——它是对外发布的包，不该知道本项目用哪个日志库。
// 事件在这里落地，因此将来换日志栈只影响这一个类型。
type rateLimitObserver struct{}

// BackendPhaseChanged 记录后端健康相位迁移。
//
// 低基数事件，每次都记：它是排查"限流为什么突然放宽或收紧"的第一手线索。
func (rateLimitObserver) BackendPhaseChanged(from, to ratelimit.HealthPhase) {
	logger.Warn("rate limit backend phase changed",
		zap.String("from", from.String()), zap.String("to", to.String()))
}

// FellBackToLocal 在首次降级到本地影子桶时记录一次。
//
// 只记一次：故障期间每个请求都打一条会把日志刷满，而信息量只有第一条。
func (rateLimitObserver) FellBackToLocal(policyID string) {
	logger.Warn("rate limit fell back to the local shadow bucket; "+
		"the model is the same but the state is not carried over, so up to one burst may pass through",
		zap.String("policy", policyID))
}
