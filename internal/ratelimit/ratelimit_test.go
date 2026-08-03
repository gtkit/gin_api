package ratelimit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/router/routespec"
)

func testPolicy(mutate func(*routespec.RateLimitPolicy)) routespec.RateLimitPolicy {
	p := routespec.RateLimitPolicy{
		ID: "test.policy", Rate: 10, Burst: 5,
		Scope: routespec.ScopeIP, Store: routespec.StoreLocal,
		FailureMode: routespec.FailureClosed, MaxKeys: 100,
	}
	if mutate != nil {
		mutate(&p)
	}
	return p
}

// TestPolicyValidation 覆盖策略校验的全部规则。
//
// 限流配置错误的表现是"限额不是你以为的那个值"——它在生产上只有被打穿
// 或被误杀时才会发现，因此全部判定必须在启动期完成。
func TestPolicyValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		mutate   func(*routespec.RateLimitPolicy)
		wantErr  bool
		wantHint string
	}{
		{name: "valid policy", mutate: nil},
		{
			name:   "fractional rate is valid",
			mutate: func(p *routespec.RateLimitPolicy) { p.Rate = 0.5 },
		},
		{
			name:    "id charset enforced",
			mutate:  func(p *routespec.RateLimitPolicy) { p.ID = "Bad ID/with slash" },
			wantErr: true, wantHint: "policy id",
		},
		{
			name:    "empty id rejected",
			mutate:  func(p *routespec.RateLimitPolicy) { p.ID = "" },
			wantErr: true, wantHint: "policy id",
		},
		{
			name:    "zero rate rejected",
			mutate:  func(p *routespec.RateLimitPolicy) { p.Rate = 0 },
			wantErr: true, wantHint: "positive finite rate",
		},
		{
			name:    "zero burst rejected",
			mutate:  func(p *routespec.RateLimitPolicy) { p.Burst = 0 },
			wantErr: true, wantHint: "positive burst",
		},
		{
			name:    "zero max keys rejected",
			mutate:  func(p *routespec.RateLimitPolicy) { p.MaxKeys = 0 },
			wantErr: true, wantHint: "max keys",
		},
		{
			name:    "unspecified scope rejected",
			mutate:  func(p *routespec.RateLimitPolicy) { p.Scope = routespec.ScopeUnspecified },
			wantErr: true, wantHint: "must declare a scope",
		},
		{
			name:    "unspecified store rejected",
			mutate:  func(p *routespec.RateLimitPolicy) { p.Store = routespec.StoreUnspecified },
			wantErr: true, wantHint: "must declare a store",
		},
		{
			name:    "unspecified failure mode rejected",
			mutate:  func(p *routespec.RateLimitPolicy) { p.FailureMode = routespec.FailureUnspecified },
			wantErr: true, wantHint: "must declare a failure mode",
		},
		{
			// local store 降级到 local 没有意义——降级目标就是它自己。
			name: "local store with local fallback rejected",
			mutate: func(p *routespec.RateLimitPolicy) {
				p.Store = routespec.StoreLocal
				p.FailureMode = routespec.FailureLocalFallback
			},
			wantErr: true, wantHint: "nothing to fall back from",
		},
		{
			// 越过精确整数范围之后限流会**悄悄失准**，没有任何报错。
			name:    "burst overflowing the exact integer range rejected",
			mutate:  func(p *routespec.RateLimitPolicy) { p.Burst = 1 << 40 },
			wantErr: true, wantHint: "exact-integer range",
		},
		{
			name:    "rate below the smallest representable value rejected",
			mutate:  func(p *routespec.RateLimitPolicy) { p.Rate = 1e-9 },
			wantErr: true, wantHint: "smallest representable rate",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := testPolicy(tc.mutate).Validate()
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate() = nil, want error")
			}
			if !strings.Contains(err.Error(), tc.wantHint) {
				t.Fatalf("error %q does not mention %q", err, tc.wantHint)
			}
		})
	}
}

// TestLocalBackendEnforcesBurstAndRate 验证本地桶的基本行为。
func TestLocalBackendEnforcesBurstAndRate(t *testing.T) {
	t.Parallel()

	// Rate 极低，保证测试期间几乎不补充：断言的是 Burst 边界本身。
	backend, err := NewLocalBackend(testPolicy(func(p *routespec.RateLimitPolicy) {
		p.Rate, p.Burst = 0.01, 3
	}))
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	defer backend.Close()

	ctx := context.Background()
	for i := range 3 {
		d, err := backend.Check(ctx, "k")
		if err != nil {
			t.Fatalf("Check %d: %v", i, err)
		}
		if !d.Allowed() {
			t.Fatalf("request %d was rejected within the burst of 3", i)
		}
	}

	d, err := backend.Check(ctx, "k")
	if err != nil {
		t.Fatalf("Check beyond burst: %v", err)
	}
	if d.Allowed() {
		t.Fatal("a request beyond the burst was allowed")
	}
	if d.Outcome != OutcomeRateLimited {
		t.Fatalf("outcome = %s, want rate_limited", d.Outcome)
	}
}

// TestLocalBackendRejectsNewKeysAtBound 钉住"达上限拒绝新建，不淘汰活跃桶"。
//
// LRU 淘汰是限流绕过：攻击者轮换超过 MaxKeys 个主体就能反复把旧桶挤掉，
// 旧主体回来时拿到一个满桶。因此达上限时必须拒绝新键，并且这个拒绝
// 要能与普通限速区分开——它说明主体数量异常，而不是某个主体太快。
func TestLocalBackendRejectsNewKeysAtBound(t *testing.T) {
	t.Parallel()

	backend, err := NewLocalBackend(testPolicy(func(p *routespec.RateLimitPolicy) {
		p.MaxKeys = 2
	}))
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	defer backend.Close()

	ctx := context.Background()
	for _, key := range []string{"a", "b"} {
		if d, _ := backend.Check(ctx, key); !d.Allowed() {
			t.Fatalf("key %q was rejected while under the key bound", key)
		}
	}

	d, _ := backend.Check(ctx, "c")
	if d.Allowed() {
		t.Fatal("a new key was accepted beyond the key bound")
	}
	if d.Outcome != OutcomeKeyBudgetExhausted {
		t.Fatalf("outcome = %s, want key_budget_exhausted (it must be distinguishable from plain rate limiting)",
			d.Outcome)
	}

	// 已存在的桶必须仍然可用——被挤掉的话就等于 LRU 淘汰。
	if _, err := backend.Check(ctx, "a"); err != nil {
		t.Fatalf("existing key failed after the bound was reached: %v", err)
	}
}

// TestRetryAfterSecondsCeilsWithFloor 钉住 Retry-After 的取整规则。
//
// 截断（int(d.Seconds())）会在不足 1 秒时输出 `Retry-After: 0`——
// 那是非法头，且语义等于"立刻重试"，恰好是限流最不希望客户端做的事。
func TestRetryAfterSecondsCeilsWithFloor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		after time.Duration
		want  int
	}{
		{name: "zero floors to one", after: 0, want: 1},
		{name: "negative floors to one", after: -time.Second, want: 1},
		{name: "sub-second floors to one", after: 100 * time.Millisecond, want: 1},
		{name: "exactly one second", after: time.Second, want: 1},
		{name: "just over a second ceils to two", after: 1001 * time.Millisecond, want: 2},
		{name: "fractional ceils up", after: 2500 * time.Millisecond, want: 3},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := (Decision{RetryAfter: tc.after}).RetryAfterSeconds(); got != tc.want {
				t.Fatalf("RetryAfterSeconds(%s) = %d, want %d", tc.after, got, tc.want)
			}
		})
	}
}

// TestKeyBuilderComposition 验证键的四段组成与主体 HMAC。
func TestKeyBuilderComposition(t *testing.T) {
	t.Parallel()

	const secret = "rate-limit-test-secret-not-a-real-credential"
	builder, err := NewKeyBuilder(secret, "svc")
	if err != nil {
		t.Fatalf("NewKeyBuilder: %v", err)
	}

	policy := testPolicy(nil)
	ipSubject := Subject{Scope: routespec.ScopeIP, Value: "203.0.113.7"}

	key := builder.Build(policy, "GET", "/api/v1/users/:id", ipSubject)

	for _, want := range []string{"svc", policy.ID, "GET", "/api/v1/users/:id", "ip"} {
		if !strings.Contains(key, want) {
			t.Fatalf("key %q does not contain %q", key, want)
		}
	}
	// 主体原文绝不能出现在键里：Redis 的读权限不该等于"拿到所有被限流的 IP"。
	if strings.Contains(key, ipSubject.Value) {
		t.Fatalf("key %q leaks the raw subject identifier", key)
	}

	// 方法与路由模式必须参与组键，否则不同代价的操作会共用一个桶。
	if key == builder.Build(policy, "POST", "/api/v1/users/:id", ipSubject) {
		t.Fatal("GET and POST produced the same key")
	}
	if key == builder.Build(policy, "GET", "/api/v1/orders/:id", ipSubject) {
		t.Fatal("two different route patterns produced the same key")
	}
	// 不同策略必须有独立的桶。
	other := testPolicy(func(p *routespec.RateLimitPolicy) { p.ID = "other.policy" })
	if key == builder.Build(other, "GET", "/api/v1/users/:id", ipSubject) {
		t.Fatal("two different policies produced the same key")
	}
}

// TestKeyBuilderSecretIsMandatoryAndLongEnough 钉住密钥长度下限。
//
// 短密钥让 HMAC 可暴力破解，键空间重新变得可枚举——那等于回到普通哈希。
func TestKeyBuilderSecretIsMandatoryAndLongEnough(t *testing.T) {
	t.Parallel()

	if _, err := NewKeyBuilder("", "svc"); err == nil {
		t.Fatal("NewKeyBuilder accepted an empty secret")
	}
	if _, err := NewKeyBuilder(strings.Repeat("a", minSecretLength-1), "svc"); err == nil {
		t.Fatal("NewKeyBuilder accepted a too-short secret")
	}
	if _, err := NewKeyBuilder(strings.Repeat("a", minSecretLength), ""); err == nil {
		t.Fatal("NewKeyBuilder accepted an empty prefix")
	}
}

// TestKeyBuilderUsesFixedFallbackRouteKeys 验证 404/405 用固定常量而非原始路径。
//
// 原始路径是客户端完全可控的高基数输入：用它组键会让键空间无限膨胀，
// 而扫描器恰好是最会制造大量不同路径的那类客户端。
func TestKeyBuilderUsesFixedFallbackRouteKeys(t *testing.T) {
	t.Parallel()

	builder, err := NewKeyBuilder(strings.Repeat("k", minSecretLength), "svc")
	if err != nil {
		t.Fatalf("NewKeyBuilder: %v", err)
	}

	policy := testPolicy(nil)
	subject := Subject{Scope: routespec.ScopeIP, Value: "203.0.113.7"}

	// FullPath() 在未命中路由时为空。
	key := builder.Build(policy, "GET", "", subject)
	if !strings.Contains(key, notFoundRouteKey) {
		t.Fatalf("key %q does not use the fixed not-found route token", key)
	}

	if got := FallbackRouteKey(true); got != methodNotAllowedRouteKey {
		t.Fatalf("FallbackRouteKey(true) = %q", got)
	}
	if got := FallbackRouteKey(false); got != notFoundRouteKey {
		t.Fatalf("FallbackRouteKey(false) = %q", got)
	}
}

// --- 健康状态机 ---

func newTestHealth(t *testing.T, policy HealthPolicy) (*HealthState, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)}
	s := NewHealthState(policy)
	s.now = clock.Now
	return s, clock
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// TestHealthDegradesOnlyAtThreshold 验证单次抖动不降级。
func TestHealthDegradesOnlyAtThreshold(t *testing.T) {
	t.Parallel()

	policy := HealthPolicy{FailureThreshold: 3, SuccessThreshold: 2, Cooldown: time.Second, ProbeInterval: time.Second}
	s, _ := newTestHealth(t, policy)

	boom := errors.New("dial tcp: connection refused")
	for i := 1; i < policy.FailureThreshold; i++ {
		s.Observe(boom)
		if s.Phase() != PhaseHealthy {
			t.Fatalf("phase after %d failures = %s, want healthy", i, s.Phase())
		}
	}
	s.Observe(boom)
	if s.Phase() != PhaseDegraded {
		t.Fatalf("phase after %d failures = %s, want degraded", policy.FailureThreshold, s.Phase())
	}
}

// TestHealthDegradedStopsHittingBackend 钉住"降级期间不再每请求打后端"。
//
// 继续打只会给一个已经出问题的依赖持续加压，并让每个请求都白等一次超时。
// 冷却期内只允许**一个**探测请求通过。
func TestHealthDegradedStopsHittingBackend(t *testing.T) {
	t.Parallel()

	policy := HealthPolicy{FailureThreshold: 1, SuccessThreshold: 1, Cooldown: 30 * time.Second, ProbeInterval: 5 * time.Second}
	s, clock := newTestHealth(t, policy)

	s.Observe(errors.New("boom"))
	if s.Phase() != PhaseDegraded {
		t.Fatalf("phase = %s, want degraded", s.Phase())
	}

	ctx := context.Background()
	for i := range 10 {
		if s.AllowRequest(ctx) {
			t.Fatalf("request %d was sent to the backend during the cooldown window", i)
		}
	}

	// 冷却到期后只放行一个探测。
	clock.advance(policy.Cooldown)
	if !s.AllowRequest(ctx) {
		t.Fatal("no probe was allowed after the cooldown elapsed")
	}
	if s.AllowRequest(ctx) {
		t.Fatal("a second concurrent probe was allowed; probing must have a single owner")
	}
}

// TestHealthRecoveryIsConservative 验证恢复比降级保守。
//
// 误判恢复的代价是把流量重新导向一个还没好的依赖，因此需要连续成功
// 且间隔足够；单次成功不足以回到 Healthy。
func TestHealthRecoveryIsConservative(t *testing.T) {
	t.Parallel()

	policy := HealthPolicy{FailureThreshold: 1, SuccessThreshold: 2, Cooldown: 10 * time.Second, ProbeInterval: 5 * time.Second}
	s, clock := newTestHealth(t, policy)
	ctx := context.Background()

	s.Observe(errors.New("boom"))
	clock.advance(policy.Cooldown)

	if !s.AllowRequest(ctx) {
		t.Fatal("probe was not allowed after cooldown")
	}
	s.Observe(nil)
	if s.Phase() == PhaseHealthy {
		t.Fatalf("phase = healthy after a single successful probe; recovery must require %d",
			policy.SuccessThreshold)
	}
}

// TestHealthProbeFailureRestartsCooldown 验证探测失败退回并重新冷却。
func TestHealthProbeFailureRestartsCooldown(t *testing.T) {
	t.Parallel()

	policy := HealthPolicy{FailureThreshold: 1, SuccessThreshold: 1, Cooldown: 10 * time.Second, ProbeInterval: time.Second}
	s, clock := newTestHealth(t, policy)
	ctx := context.Background()

	s.Observe(errors.New("boom"))
	clock.advance(policy.Cooldown)

	if !s.AllowRequest(ctx) {
		t.Fatal("probe was not allowed after cooldown")
	}
	s.Observe(errors.New("still down"))

	if s.Phase() != PhaseDegraded {
		t.Fatalf("phase after a failed probe = %s, want degraded", s.Phase())
	}
	// 冷却重新开始：立刻再探必须被拒绝。
	if s.AllowRequest(ctx) {
		t.Fatal("a probe was allowed immediately after a failed probe; the cooldown must restart")
	}
}

// TestHealthProbeOwnershipIsSingle 在并发下验证探测的单一所有权。
func TestHealthProbeOwnershipIsSingle(t *testing.T) {
	t.Parallel()

	policy := HealthPolicy{FailureThreshold: 1, SuccessThreshold: 1, Cooldown: 0, ProbeInterval: time.Hour}
	s, _ := newTestHealth(t, policy)
	s.Observe(errors.New("boom"))

	ctx := context.Background()
	const concurrent = 16
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		allowed int
	)
	wg.Add(concurrent)
	for range concurrent {
		go func() {
			defer wg.Done()
			if s.AllowRequest(ctx) {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if allowed != 1 {
		t.Fatalf("allowed probes = %d, want exactly 1", allowed)
	}
}

// TestRegistryRejectsDuplicatesAndFreezes 验证注册表的两条不变式。
func TestRegistryRejectsDuplicatesAndFreezes(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()

	first, err := NewLocalBackend(testPolicy(nil))
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	if err := reg.Register(first); err != nil {
		t.Fatalf("Register: %v", err)
	}

	second, err := NewLocalBackend(testPolicy(nil))
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	if err := reg.Register(second); err == nil {
		t.Fatal("registry accepted a duplicate policy id; the effective one would depend on registration order")
	}

	reg.Freeze()
	third, err := NewLocalBackend(testPolicy(func(p *routespec.RateLimitPolicy) { p.ID = "third.policy" }))
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	if err := reg.Register(third); err == nil {
		t.Fatal("registry accepted a registration after being frozen")
	}

	_ = reg.Close()
}

// TestRegistryValidateReferences 钉住两类装配错误。
//
// 引用未登记的策略若不拦住，那条路由会**完全没有限流**——而配置里
// 明明写了一个 ID，看起来一切正常。user scope 挂在公开路由上同理：
// 拿不到身份，整条路由退化成一个所有匿名请求共享的桶。
func TestRegistryValidateReferences(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	userPolicy := testPolicy(func(p *routespec.RateLimitPolicy) {
		p.ID = "user.policy"
		p.Scope = routespec.ScopeUser
	})
	limiter, err := NewLocalBackend(userPolicy)
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	if err := reg.Register(limiter); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cases := []struct {
		name     string
		route    routespec.CompiledRoute
		wantErr  bool
		wantHint string
	}{
		{
			name: "no policy reference is fine",
			route: routespec.CompiledRoute{
				FullPath: "/api/v1/open",
				Spec:     routespec.RouteSpec{Method: "GET"},
			},
		},
		{
			name: "unknown policy rejected",
			route: routespec.CompiledRoute{
				FullPath: "/api/v1/thing",
				Spec: routespec.RouteSpec{
					Method: "GET", RateLimitPolicyID: "missing.policy",
					AccessPolicy: routespec.AccessPolicy{Mode: routespec.AccessAuthenticated},
				},
			},
			wantErr: true, wantHint: "not registered",
		},
		{
			name: "user scope on a public route rejected",
			route: routespec.CompiledRoute{
				FullPath: "/api/v1/public",
				Spec: routespec.RouteSpec{
					Method: "GET", RateLimitPolicyID: "user.policy",
					AccessPolicy: routespec.AccessPolicy{Mode: routespec.AccessPublic},
				},
			},
			wantErr: true, wantHint: "does not require authentication",
		},
		{
			name: "user scope on an authenticated route accepted",
			route: routespec.CompiledRoute{
				FullPath: "/api/v1/me",
				Spec: routespec.RouteSpec{
					Method: "GET", RateLimitPolicyID: "user.policy",
					AccessPolicy: routespec.AccessPolicy{Mode: routespec.AccessAuthenticated},
				},
			},
		},
		{
			// OptionalAuthenticated 允许匿名通过，因此不保证有身份。
			name: "user scope on an optional-auth route rejected",
			route: routespec.CompiledRoute{
				FullPath: "/api/v1/maybe",
				Spec: routespec.RouteSpec{
					Method: "GET", RateLimitPolicyID: "user.policy",
					AccessPolicy: routespec.AccessPolicy{Mode: routespec.AccessOptionalAuthenticated},
				},
			},
			wantErr: true, wantHint: "does not require authentication",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := reg.ValidateReferences([]routespec.CompiledRoute{tc.route})
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("ValidateReferences() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("ValidateReferences() = nil, want error")
			}
			if !strings.Contains(err.Error(), tc.wantHint) {
				t.Fatalf("error %q does not mention %q", err, tc.wantHint)
			}
		})
	}
}

// TestFixedPointConversion 验证定点换算与 Lua 精确整数边界。
func TestFixedPointConversion(t *testing.T) {
	t.Parallel()

	p := testPolicy(func(p *routespec.RateLimitPolicy) { p.Rate, p.Burst = 2.5, 10 })

	if got := p.FixedPointRate(); got != 2_500_000 {
		t.Fatalf("FixedPointRate() = %d, want 2500000", got)
	}
	if got := p.FixedPointBurst(); got != 10_000_000 {
		t.Fatalf("FixedPointBurst() = %d, want 10000000", got)
	}
}
