package bootstrap

import (
	"context"
	"errors"
	"fmt"

	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/gin-api/internal/ratelimit"
	"github.com/gtkit/gin-api/internal/router/routespec"

	logger "github.com/gtkit/logger/v2"
)

// rateLimitAssembly 是限流的装配产物。
type rateLimitAssembly struct {
	registry *ratelimit.Registry
	keys     *ratelimit.KeyBuilder
}

// buildRateLimits 把配置翻译成限流注册表。
//
// 策略集合为空时返回零值：母版默认不配置任何策略，此时路由也不会引用
// 任何策略 ID，registrar 的引用校验因而不会触发。
//
// 任何一条策略非法即启动失败：限流配置错误的表现是"限额不是你以为的那个值"，
// 而那在生产上只有被打穿或被误杀时才会发现。
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
	var redisBackend *ratelimit.RedisBackend

	for _, raw := range cfg.RateLimit.Policies {
		policy, convErr := toRateLimitPolicy(raw)
		if convErr != nil {
			return rateLimitAssembly{}, convErr
		}

		limiter, buildErr := buildLimiter(ctx, policy, resources, &redisBackend, cfg.App.Name)
		if buildErr != nil {
			return rateLimitAssembly{}, buildErr
		}
		if regErr := registry.Register(limiter); regErr != nil {
			return rateLimitAssembly{}, fmt.Errorf("register rate limit policy: %w", regErr)
		}
	}

	// 冻结：策略集合是启动期事实，运行期可变会让"这条路由走哪条限额"
	// 失去确定答案，也让审计无从下手。
	registry.Freeze()

	logger.Infof("rate limiting enabled with %d policies: %v", len(registry.IDs()), registry.IDs())
	return rateLimitAssembly{registry: registry, keys: keys}, nil
}

// buildLimiter 按 Store 与 FailureMode 构造执行体。
func buildLimiter(
	ctx context.Context,
	policy routespec.RateLimitPolicy,
	resources *data.Resources,
	redisBackend **ratelimit.RedisBackend,
	keyPrefix string,
) (ratelimit.Limiter, error) {
	if policy.Store == routespec.StoreLocal {
		return ratelimit.NewLocalBackend(policy)
	}

	if *redisBackend == nil {
		client := resources.Rdb(0)
		if client == nil {
			return nil, fmt.Errorf(
				"rate limit policy %q uses the redis store but no redis client is available: "+
					"either configure redis or switch the policy to the local store", policy.ID)
		}
		backend, err := ratelimit.NewRedisBackend(ctx, client, keyPrefix)
		if err != nil {
			return nil, fmt.Errorf("build redis rate limit backend for %q: %w", policy.ID, err)
		}
		*redisBackend = backend
	}

	if policy.FailureMode == routespec.FailureLocalFallback {
		return ratelimit.NewFallbackBackend(*redisBackend, policy)
	}
	return ratelimit.NewRedisLimiter(*redisBackend, policy)
}

// toRateLimitPolicy 把配置形态翻译成强类型策略。
func toRateLimitPolicy(raw config.RateLimitPolicyConfig) (routespec.RateLimitPolicy, error) {
	scope, err := parseScope(raw.Scope)
	if err != nil {
		return routespec.RateLimitPolicy{}, fmt.Errorf("rate limit policy %q: %w", raw.ID, err)
	}
	store, err := parseStore(raw.Store)
	if err != nil {
		return routespec.RateLimitPolicy{}, fmt.Errorf("rate limit policy %q: %w", raw.ID, err)
	}
	mode, err := parseFailureMode(raw.FailureMode)
	if err != nil {
		return routespec.RateLimitPolicy{}, fmt.Errorf("rate limit policy %q: %w", raw.ID, err)
	}

	policy := routespec.RateLimitPolicy{
		ID: raw.ID, Rate: raw.Rate, Burst: raw.Burst,
		Scope: scope, Store: store, FailureMode: mode, MaxKeys: raw.MaxKeys,
	}
	if err := policy.Validate(); err != nil {
		return routespec.RateLimitPolicy{}, err
	}
	return policy, nil
}

func parseScope(raw string) (routespec.RateLimitScope, error) {
	switch raw {
	case "ip":
		return routespec.ScopeIP, nil
	case "user":
		return routespec.ScopeUser, nil
	case "global":
		return routespec.ScopeGlobal, nil
	default:
		return routespec.ScopeUnspecified, fmt.Errorf("unknown scope %q", raw)
	}
}

func parseStore(raw string) (routespec.RateLimitStore, error) {
	switch raw {
	case "local":
		return routespec.StoreLocal, nil
	case "redis":
		return routespec.StoreRedis, nil
	default:
		return routespec.StoreUnspecified, fmt.Errorf("unknown store %q", raw)
	}
}

func parseFailureMode(raw string) (routespec.RateLimitFailureMode, error) {
	switch raw {
	case "fail_open":
		return routespec.FailureOpen, nil
	case "fail_closed":
		return routespec.FailureClosed, nil
	case "local_fallback":
		return routespec.FailureLocalFallback, nil
	default:
		return routespec.FailureUnspecified, fmt.Errorf("unknown failure mode %q", raw)
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
