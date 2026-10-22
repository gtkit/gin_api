package ratelimit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// ttlSlack 是键 TTL 相对"补满一桶所需时间"的余量。
	//
	// 没有余量的话，一个刚好在补满边界到达的请求会读到已过期的键，
	// 拿到一个满桶——限额被悄悄放大。
	ttlSlack = 60 * time.Second
)

// scriptRunner 是本包需要的 Redis 能力，便于测试替换。
type scriptRunner interface {
	ScriptLoad(ctx context.Context, script string) *redis.StringCmd
	EvalSha(ctx context.Context, sha1 string, keys []string, args ...any) *redis.Cmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
}

// RedisBackend 是跨副本共享的令牌桶，所有引用它的策略共享同一份健康状态。
//
// 共享是有意的：按策略各建一套状态机会让同一次 Redis 故障触发 N 份独立探测，
// 对一个已经出问题的依赖形成探测风暴。FailureMode 仍由各策略分别决定。
type RedisBackend struct {
	// observer 接收运行期事件；未注入时是空实现。
	observer Observer
	client   scriptRunner
	// sha 是脚本缓存标识，**必须原子读写**。
	//
	// 它在每个请求上被读（EvalSha），并在 NOSCRIPT 时被重载写入——而 NOSCRIPT
	// 会在主从切换、故障恢复与 SCRIPT FLUSH 之后同时发生在多个并发请求上。
	// 用普通字段时 `go test -race` 明确报 read/write 与 write/write 竞争；
	// Go 内存模型下那是未定义行为，实际可能读到撕裂的 string header。
	sha    atomic.Pointer[string]
	health *HealthState
	// keyPrefix 隔离不同服务的限流键。
	keyPrefix                string
	probeCleanupError        atomic.Pointer[error]
	probeCleanupErrorHandler func(error)
}

// NewRedisBackend 构造并**实测**限流能力。
//
// 只 PING 是不够的：PING 通过只说明连接活着，不说明当前 ACL 允许 EVALSHA、
// 不说明脚本能加载、更不说明返回值契约与代码期望一致。资源层 Ping 成功
// 但能力探测失败的实例绝不能被判为 ready——它在第一个真实请求上才会暴露。
//
// 探测在启动 ctx 内完成，用带服务前缀的随机 probe key 执行一次真实调用后删除。
func NewRedisBackend(
	ctx context.Context, client scriptRunner, opts ...Option,
) (*RedisBackend, error) {
	if client == nil {
		return nil, errors.New("redis rate limit backend requires a client")
	}
	resolved := newOptions(opts)

	b := &RedisBackend{
		client:                   client,
		health:                   NewHealthState(DefaultHealthPolicy(), resolved.observer),
		keyPrefix:                resolved.keyPrefix,
		observer:                 resolved.observer,
		probeCleanupErrorHandler: resolved.probeCleanupErrorHandler,
	}

	// degradedStart 只吸收**后端不可用**，不吸收调用方的取消：
	// 启动被信号中止时构造必须中止，而不是留下一个降级的半成品。
	sha, err := client.ScriptLoad(ctx, tokenBucketScript).Result()
	if err != nil {
		if !resolved.degradedStart || ctx.Err() != nil {
			return nil, fmt.Errorf("load rate limit script: %w", err)
		}
		// 空 sha 是自愈的：首次判定的 EVALSHA 报 NOSCRIPT，evaluate
		// 的重载路径会装载脚本——与主从切换后缓存丢失同一条恢复通道。
		empty := ""
		b.sha.Store(&empty)
		b.health.forceDegraded()
		return b, nil
	}
	b.sha.Store(&sha)

	if err := b.probe(ctx); err != nil {
		if !resolved.degradedStart || ctx.Err() != nil {
			return nil, fmt.Errorf("verify rate limit capability: %w", err)
		}
		b.health.forceDegraded()
		return b, nil
	}

	return b, nil
}

// probe 用一次真实的限流调用验证端到端能力（rate-limiting spec）。
//
// 三条约束缺一不可，它们各自对应一个曾经的缺陷：
//
//	**策略标识每次进程唯一**：标识决定键基数注册表的键。固定标识会让注册表
//	在重启之间共享，于是第二次探测撞上自己上次留下的成员被拒——探针给真实
//	判定路径加的准入反过来打在它自己身上。
//
//	**断言判定为放行**：只看 err 不看 Decision 时，任何"能连通但被拒"的
//	结果都被报告成能力正常，而探针存在的全部理由就是验证能力。
//
//	**清理桶键与注册表键**：只删桶键会留下注册表成员，正是上面那条共享的来源。
func (b *RedisBackend) probe(ctx context.Context) error {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("generate probe key: %w", err)
	}
	unique := hex.EncodeToString(suffix)
	// 不在此处拼前缀：evaluate 会统一加。两处各拼一次会得到双重前缀。
	key := "probe:" + unique

	probePolicy := Policy{
		// 策略标识随进程唯一：它同时决定注册表键，固定值会跨重启共享。
		ID: "probe:" + unique, Rate: 1, Burst: 1,
		Scope: ScopeGlobal, Store: StoreRedis,
		FailureMode: FailureClosed,
		// 留并发余量：同一进程内的多次探测（重试、多后端）不应互相挤掉。
		MaxKeys: probeMaxKeys,
	}

	decision, err := b.evaluate(ctx, probePolicy, key)
	if err != nil {
		return err
	}
	// 能连通不等于能用：被限速或被键基数拒绝都说明这条路径当下不可用。
	if decision.Outcome != OutcomeAllowed {
		return fmt.Errorf("probe was not allowed (outcome %s): the backend answered but "+
			"cannot serve rate limit decisions", decision.Outcome)
	}

	// 两键都由创建它们的Lua设置TTL。DEL ACL缺失不等于限流能力失败；
	// 保存首个清理错误并通知拥有者，避免恢复探测重复刷日志。
	if err := b.client.Del(ctx, b.qualifiedKey(key), b.registryKey(probePolicy)).Err(); err != nil {
		cleanupErr := fmt.Errorf("clean rate limit probe artifacts (TTL expiry remains active): %w", err)
		if b.probeCleanupError.CompareAndSwap(nil, new(cleanupErr)) && b.probeCleanupErrorHandler != nil {
			b.probeCleanupErrorHandler(cleanupErr)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return errors.Join(ctxErr, cleanupErr)
		}
	}
	// A completed capability check must not turn caller cancellation during
	// cleanup into successful construction (including degraded-start mode).
	return ctx.Err()
}

// ProbeCleanupError 返回本后端首次探针清理失败；无失败时为nil。
// 状态保留到后端销毁，不表示限流能力失效；探针键仍按Lua的TTL回收。
func (b *RedisBackend) ProbeCleanupError() error {
	if err := b.probeCleanupError.Load(); err != nil {
		return *err
	}
	return nil
}

// Health 返回共享的健康状态。
func (b *RedisBackend) Health() *HealthState { return b.health }

// Check 判定一次请求。
//
// 处于 Degraded 时**不再打 Redis**：故障期间继续每请求发起调用，
// 只会给一个已经出问题的依赖持续加压，并让每个请求都白等一次超时。
func (b *RedisBackend) check(
	ctx context.Context, policy Policy, key string,
) (Decision, error) {
	if !b.health.AllowRequest(ctx) {
		return Decision{Outcome: OutcomeBackendUnavailable, Limit: policy.Burst}, nil
	}

	decision, err := b.evaluate(ctx, policy, key)
	b.health.Observe(err)
	if err != nil {
		return Decision{Outcome: OutcomeBackendUnavailable, Limit: policy.Burst}, err
	}
	return decision, nil
}

// evaluate 执行一次脚本调用，处理 NOSCRIPT 重载。
func (b *RedisBackend) evaluate(
	ctx context.Context, policy Policy, key string,
) (Decision, error) {
	// **前缀在这里统一拼接**，因为这是所有路径唯一的收口点。
	//
	// 曾经由各调用方自己拼：探针拼了，真实判定没拼——于是 WithKeyPrefix 对
	// 生产流量完全无效，而它的文档明确承诺隔离。多个服务共用同一后端时，
	// 同名策略共享同一个桶，各自的限额都不是配置里写的那个值。
	// 收口到一处之后，新增调用路径不会再漏。
	key = b.qualifiedKey(key)
	registryKey := b.registryKey(policy)

	args := []any{
		policy.FixedPointRate(),
		policy.FixedPointBurst(),
		bucketTTL(policy).Milliseconds(),
		policy.MaxKeys,
	}

	raw, err := b.client.EvalSha(ctx, *b.sha.Load(), []string{key, registryKey}, args...).Result()
	if err != nil && isNoScript(err) {
		// 主从切换或 SCRIPT FLUSH 之后脚本缓存会丢。**重载并重试一次**。
		// 缺这一条，一次故障切换会让整片限流持续报错，而根因只是缓存没了。
		sha, loadErr := b.client.ScriptLoad(ctx, tokenBucketScript).Result()
		if loadErr != nil {
			return Decision{}, fmt.Errorf("reload rate limit script after NOSCRIPT: %w", loadErr)
		}
		// 并发重载时多个 goroutine 会各自 Store 一次。这没有问题——
		// 它们加载的是同一段脚本，因此得到的 sha 必然相同；重复存入等价。
		b.sha.Store(&sha)
		raw, err = b.client.EvalSha(ctx, sha, []string{key, registryKey}, args...).Result()
	}
	if err != nil {
		return Decision{}, fmt.Errorf("evaluate rate limit script: %w", err)
	}

	return parseScriptResult(raw, policy)
}

// isNoScript 识别脚本缓存缺失。
//
// go-redis 不为它提供专门的错误类型，只能按前缀判定；Redis 的错误回复
// 以 `NOSCRIPT` 开头是稳定契约。
func isNoScript(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "NOSCRIPT")
}

// parseScriptResult 把 Lua 返回值翻译成 Decision。
func parseScriptResult(raw any, policy Policy) (Decision, error) {
	values, ok := raw.([]any)
	if !ok || len(values) != 3 {
		return Decision{}, fmt.Errorf("unexpected rate limit script result shape: %T", raw)
	}

	status, err := toInt64(values[0])
	if err != nil {
		return Decision{}, fmt.Errorf("parse script status: %w", err)
	}
	remainingMicro, err := toInt64(values[1])
	if err != nil {
		return Decision{}, fmt.Errorf("parse script remaining: %w", err)
	}
	retryAfterMS, err := toInt64(values[2])
	if err != nil {
		return Decision{}, fmt.Errorf("parse script retry-after: %w", err)
	}

	decision := Decision{
		Limit:     policy.Burst,
		Remaining: int(remainingMicro / microTokensPerToken),
	}
	if decision.Remaining < 0 {
		decision.Remaining = 0
	}

	switch status {
	case statusAllowed:
		decision.Outcome = OutcomeAllowed
	case statusRejected:
		decision.Outcome = OutcomeRateLimited
		decision.RetryAfter = time.Duration(retryAfterMS) * time.Millisecond
	case statusKeyBudgetDenied:
		// 与 local 后端同语义：键基数达上限拒绝新建，不是限速——
		// 混在一起会让键轮换攻击长期被当成正常限速噪音。
		decision.Outcome = OutcomeKeyBudgetExhausted
	default:
		return Decision{}, fmt.Errorf("unknown rate limit script status %d", status)
	}

	return decision, nil
}

// toInt64 把 Lua 的整数返回值转成 int64。
func toInt64(v any) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	case float64:
		return int64(n), nil
	default:
		return 0, fmt.Errorf("value is %T, want an integer", v)
	}
}

// maxBucketTTL 是键存活时长的上限。
//
// 桶再大也不该让键活过一天：更长的 TTL 只是让一个早已无人访问的主体
// 继续占内存，而它的配额早就补满了。
const maxBucketTTL = 24 * time.Hour

// registryKey 是策略的键基数注册表键。
//
// 按策略隔离：键基数是策略级配额（Policy.MaxKeys），与桶键同前缀，
// 多服务共用同一后端时互不干扰。
func (b *RedisBackend) registryKey(policy Policy) string {
	return b.qualifiedKey("registry:" + policy.ID)
}

// probeMaxKeys 是探测策略的键基数上限。
//
// 大于 1：探针的注册表虽已随策略标识唯一化，但同一进程内仍可能有多次探测
// （构造重试、多后端实例），上限为 1 会让它们互相挤掉。
const probeMaxKeys = 8

// bucketTTL 返回键的存活时长：补满一桶所需时间 + 余量。
//
// **必须夹在上限内**。此前直接 `time.Duration(秒数) * time.Second` 会溢出：
// 实测 Rate=0.000001、Burst=10000 通过了策略校验，却算出
// -2346317h46m53s——负 TTL 传给 Redis 会让键立即过期或被拒绝，限流静默失效。
//
// 策略校验只查了 Lua 的精确整数范围，而这是**另一个**溢出边界
// （time.Duration 是纳秒计的 int64）。两个边界必须各查各的。
func bucketTTL(policy Policy) time.Duration {
	if policy.Rate <= 0 {
		return ttlSlack
	}

	seconds := math.Ceil(float64(policy.Burst) / policy.Rate)
	// 先在 float64 域比较再转换：转换后再比较就已经溢出了。
	//
	// **含余量之后再夹上限**，不能反过来：先夹再加余量会让结果比上限多出一个
	// 余量（实测 Rate=1、Burst=86399 得到 24h0m59s，而声称的上限是 24h）。
	// 一个自己就违反声称值的上限，无法作为容量估算的依据。
	if seconds >= maxBucketTTL.Seconds() {
		return maxBucketTTL
	}
	if ttl := time.Duration(seconds)*time.Second + ttlSlack; ttl < maxBucketTTL {
		return ttl
	}
	return maxBucketTTL
}

// qualifiedKey 给键加上服务前缀。
//
// 未配置前缀时原样返回：多数部署只有一个服务用这个 Redis，强制加前缀
// 只会让键变长而不带来隔离价值。
func (b *RedisBackend) qualifiedKey(key string) string {
	if b.keyPrefix == "" {
		return key
	}
	return b.keyPrefix + ":" + key
}

// FallbackBackend 把 Redis backend 与本地影子桶组合成 local_fallback 模式。
//
// 影子桶平时**不消耗 token**，因此切换瞬间是满桶——这正是"只能称算法模型一致、
// 不能称状态连续等价"的原因：切换会多放行至多一个 Burst。文档必须写明这一点，
// 不能让人以为降级是无缝的。
type FallbackBackend struct {
	redis  *RedisBackend
	shadow *LocalBackend
	policy Policy

	// warnOnce 保证"已降级"只提示一次，避免故障期刷屏。
	warnOnce sync.Once
}

// NewFallbackBackend 组合 Redis 与同参数的本地影子桶。
func NewFallbackBackend(redisBackend *RedisBackend, policy Policy) (*FallbackBackend, error) {
	if redisBackend == nil {
		return nil, errors.New("fallback backend requires a redis backend")
	}
	shadow, err := NewLocalBackend(policy)
	if err != nil {
		return nil, fmt.Errorf("build shadow limiter for %q: %w", policy.ID, err)
	}
	return &FallbackBackend{redis: redisBackend, shadow: shadow, policy: policy}, nil
}

// Policy 返回该 backend 的策略。
func (b *FallbackBackend) Policy() Policy { return b.policy }

// Check 优先走 Redis，不可用时切到本地影子桶。
func (b *FallbackBackend) Check(ctx context.Context, key string) (Decision, error) {
	decision, err := b.redis.check(ctx, b.policy, key)
	if err == nil && decision.Outcome != OutcomeBackendUnavailable {
		return decision, nil
	}

	b.warnOnce.Do(func() {
		b.redis.observer.FellBackToLocal(b.policy.ID)
	})

	local, localErr := b.shadow.Check(ctx, key)
	local.Degraded = true
	if localErr != nil {
		return local, errors.Join(err, fmt.Errorf("local rate limit fallback: %w", localErr))
	}
	return local, nil
}

// Close 释放影子桶。
func (b *FallbackBackend) Close() {
	if b == nil {
		return
	}
	b.shadow.Close()
}

// RedisLimiter 把共享的 RedisBackend 绑定到一条具体策略，实现 Limiter。
//
// backend 共享而 limiter 按策略独立：健康状态必须共享（否则同一次故障会
// 触发 N 份探测），而 FailureMode 与限额参数必须各自独立。
type RedisLimiter struct {
	backend *RedisBackend
	policy  Policy
}

// NewRedisLimiter 绑定策略。
func NewRedisLimiter(backend *RedisBackend, policy Policy) (*RedisLimiter, error) {
	if backend == nil {
		return nil, errors.New("redis limiter requires a backend")
	}
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("build redis limiter: %w", err)
	}
	return &RedisLimiter{backend: backend, policy: policy}, nil
}

// Policy 返回该 limiter 的策略。
func (l *RedisLimiter) Policy() Policy { return l.policy }

// Check 判定一次请求；后端故障时按 FailureMode 给出最终判定。
func (l *RedisLimiter) Check(ctx context.Context, key string) (Decision, error) {
	decision, err := l.backend.check(ctx, l.policy, key)
	if err == nil && decision.Outcome != OutcomeBackendUnavailable {
		return decision, nil
	}
	return resolveFailure(l.policy, l.policy.Burst), err
}

// Close 无需释放资源：backend 由装配层统一持有。
func (l *RedisLimiter) Close() {}
