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
	policy ratelimit.Policy,
	resources *data.Resources,
	redisBackend **ratelimit.RedisBackend,
	keyPrefix string,
) (ratelimit.Limiter, error) {
	if policy.Store == ratelimit.StoreLocal {
		return ratelimit.NewLocalBackend(policy)
	}

	if *redisBackend == nil {
		client := resources.Rdb(0)
		if client == nil {
			return nil, fmt.Errorf(
				"rate limit policy %q uses the redis store but no redis client is available: "+
					"either configure redis or switch the policy to the local store", policy.ID)
		}
		backend, err := ratelimit.NewRedisBackend(ctx, client,
			ratelimit.WithKeyPrefix(keyPrefix),
			ratelimit.WithObserver(rateLimitObserver{}))
		if err != nil {
			return nil, fmt.Errorf("build redis rate limit backend for %q: %w", policy.ID, err)
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
