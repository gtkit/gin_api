package bootstrap

import (
	"context"
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/router/routespec"
)

func policyConfig(mutate func(*config.RateLimitPolicyConfig)) config.RateLimitPolicyConfig {
	p := config.RateLimitPolicyConfig{
		ID: "login.ip", Rate: 0.2, Burst: 5,
		Scope: "ip", Store: "local", FailureMode: "fail_closed", MaxKeys: 1000,
	}
	if mutate != nil {
		mutate(&p)
	}
	return p
}

// TestToRateLimitPolicyTranslatesEnums 验证配置里的字符串枚举被正确翻译。
//
// 配置层用字符串而不是数字是有意的：yaml 里写 `scope: 2` 的话，
// 它的含义只有读代码才知道，而写错一个数字不会有任何提示。
func TestToRateLimitPolicyTranslatesEnums(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		mutate    func(*config.RateLimitPolicyConfig)
		wantScope routespec.RateLimitScope
		wantStore routespec.RateLimitStore
		wantMode  routespec.RateLimitFailureMode
	}{
		{
			name:      "ip local fail_closed",
			mutate:    nil,
			wantScope: routespec.ScopeIP, wantStore: routespec.StoreLocal,
			wantMode: routespec.FailureClosed,
		},
		{
			name: "user redis fail_open",
			mutate: func(p *config.RateLimitPolicyConfig) {
				p.Scope, p.Store, p.FailureMode = "user", "redis", "fail_open"
			},
			wantScope: routespec.ScopeUser, wantStore: routespec.StoreRedis,
			wantMode: routespec.FailureOpen,
		},
		{
			name: "global redis local_fallback",
			mutate: func(p *config.RateLimitPolicyConfig) {
				p.Scope, p.Store, p.FailureMode = "global", "redis", "local_fallback"
			},
			wantScope: routespec.ScopeGlobal, wantStore: routespec.StoreRedis,
			wantMode: routespec.FailureLocalFallback,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := toRateLimitPolicy(policyConfig(tc.mutate))
			if err != nil {
				t.Fatalf("toRateLimitPolicy: %v", err)
			}
			if got.Scope != tc.wantScope {
				t.Errorf("Scope = %s, want %s", got.Scope, tc.wantScope)
			}
			if got.Store != tc.wantStore {
				t.Errorf("Store = %s, want %s", got.Store, tc.wantStore)
			}
			if got.FailureMode != tc.wantMode {
				t.Errorf("FailureMode = %s, want %s", got.FailureMode, tc.wantMode)
			}
		})
	}
}

// TestToRateLimitPolicyRejectsBadInput 验证非法枚举与非法取值都在翻译层被拦。
//
// 拦不住的话，一个拼错的 `scope: users` 会变成零值——而零值恰好是
// routespec 判定为非法的那个，最终报出来的是一个与 yaml 里的拼写
// 毫无关联的错误。在这里报能直接指出是哪个字段写错了。
func TestToRateLimitPolicyRejectsBadInput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		mutate   func(*config.RateLimitPolicyConfig)
		wantHint string
	}{
		{
			name:     "unknown scope",
			mutate:   func(p *config.RateLimitPolicyConfig) { p.Scope = "users" },
			wantHint: "unknown scope",
		},
		{
			name:     "unknown store",
			mutate:   func(p *config.RateLimitPolicyConfig) { p.Store = "memcached" },
			wantHint: "unknown store",
		},
		{
			name:     "unknown failure mode",
			mutate:   func(p *config.RateLimitPolicyConfig) { p.FailureMode = "retry" },
			wantHint: "unknown failure mode",
		},
		{
			name:     "invalid rate propagates from policy validation",
			mutate:   func(p *config.RateLimitPolicyConfig) { p.Rate = 0 },
			wantHint: "positive finite rate",
		},
		{
			name:     "invalid id propagates from policy validation",
			mutate:   func(p *config.RateLimitPolicyConfig) { p.ID = "Bad ID" },
			wantHint: "policy id",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := toRateLimitPolicy(policyConfig(tc.mutate))
			if err == nil {
				t.Fatal("toRateLimitPolicy accepted an invalid configuration")
			}
			if !strings.Contains(err.Error(), tc.wantHint) {
				t.Fatalf("error %q does not mention %q", err, tc.wantHint)
			}
		})
	}
}

// TestRedisRequiredForRateLimit 钉住依赖必需性的推导。
//
// 只有"分布式存储 + 失败时拒绝"才让 Redis 成为 required：那种组合下
// Redis 挂掉时策略只能拒绝请求，实例不该被判就绪。其余组合本来就有
// 降级路径，抬成 required 会让一次可降级的故障升级成实例被摘流。
func TestRedisRequiredForRateLimit(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		policies []config.RateLimitPolicyConfig
		want     bool
	}{
		{name: "no policies", policies: nil, want: false},
		{
			name:     "local store never requires redis",
			policies: []config.RateLimitPolicyConfig{policyConfig(nil)},
			want:     false,
		},
		{
			name: "redis with fail_open stays optional",
			policies: []config.RateLimitPolicyConfig{policyConfig(func(p *config.RateLimitPolicyConfig) {
				p.Store, p.FailureMode = "redis", "fail_open"
			})},
			want: false,
		},
		{
			name: "redis with local_fallback stays optional",
			policies: []config.RateLimitPolicyConfig{policyConfig(func(p *config.RateLimitPolicyConfig) {
				p.Store, p.FailureMode = "redis", "local_fallback"
			})},
			want: false,
		},
		{
			name: "redis with fail_closed requires redis",
			policies: []config.RateLimitPolicyConfig{policyConfig(func(p *config.RateLimitPolicyConfig) {
				p.Store, p.FailureMode = "redis", "fail_closed"
			})},
			want: true,
		},
		{
			name: "one strict policy among many is enough",
			policies: []config.RateLimitPolicyConfig{
				policyConfig(nil),
				policyConfig(func(p *config.RateLimitPolicyConfig) {
					p.ID, p.Store, p.FailureMode = "strict.ip", "redis", "fail_closed"
				}),
			},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{RateLimit: config.RateLimitConfig{Policies: tc.policies}}
			if got := redisRequiredForRateLimit(cfg); got != tc.want {
				t.Fatalf("redisRequiredForRateLimit = %v, want %v", got, tc.want)
			}
			// redisRequired 必须把该判据接进去，否则推导表形同虚设。
			if got := redisRequired(cfg); got != tc.want {
				t.Fatalf("redisRequired = %v, want %v (the rate limit rule must feed the requirement table)",
					got, tc.want)
			}
		})
	}

	if redisRequiredForRateLimit(nil) {
		t.Fatal("redisRequiredForRateLimit(nil) = true")
	}
}

// TestBuildRateLimitsWithoutPoliciesIsNoop 验证母版默认形态：不配策略即不启用。
//
// 此时也不要求密钥——强制配一个用不到的密钥只会让人随便填一个凑数，
// 反而降低它在真正启用时的可信度。
func TestBuildRateLimitsWithoutPoliciesIsNoop(t *testing.T) {
	t.Parallel()

	got, err := buildRateLimits(context.Background(), &config.Config{}, nil)
	if err != nil {
		t.Fatalf("buildRateLimits: %v", err)
	}
	if got.registry != nil || got.keys != nil {
		t.Fatal("buildRateLimits produced a registry although no policies are configured")
	}
	if err := closeRateLimits(got); err != nil {
		t.Fatalf("closeRateLimits on an empty assembly = %v, want nil", err)
	}
}

// TestBuildRateLimitsLocalPolicies 验证本地策略被正确装配并可关闭。
func TestBuildRateLimitsLocalPolicies(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		App: config.AppConfig{Name: "svc"},
		RateLimit: config.RateLimitConfig{
			KeyHMACSecret: strings.Repeat("s", 32),
			Policies: []config.RateLimitPolicyConfig{
				policyConfig(nil),
				policyConfig(func(p *config.RateLimitPolicyConfig) { p.ID = "search.ip" }),
			},
		},
	}

	got, err := buildRateLimits(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("buildRateLimits: %v", err)
	}
	if got.registry == nil || got.keys == nil {
		t.Fatal("buildRateLimits produced an incomplete assembly")
	}
	if ids := got.registry.IDs(); len(ids) != 2 {
		t.Fatalf("registered policies = %v, want 2", ids)
	}
	// 注册表必须已冻结：策略集合是启动期事实。
	limiter, ok := got.registry.Lookup("login.ip")
	if !ok {
		t.Fatal("login.ip was not registered")
	}
	if err := got.registry.Register(limiter); err == nil {
		t.Fatal("registry accepted a registration after assembly; it must be frozen")
	}

	if err := closeRateLimits(got); err != nil {
		t.Fatalf("closeRateLimits: %v", err)
	}
}

// TestBuildRateLimitsRejectsShortSecret 验证密钥长度在装配期被拦。
func TestBuildRateLimitsRejectsShortSecret(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		App: config.AppConfig{Name: "svc"},
		RateLimit: config.RateLimitConfig{
			KeyHMACSecret: "short",
			Policies:      []config.RateLimitPolicyConfig{policyConfig(nil)},
		},
	}

	if _, err := buildRateLimits(context.Background(), cfg, nil); err == nil {
		t.Fatal("buildRateLimits accepted a too-short key secret")
	}
}

// TestBuildRateLimitsRejectsRedisPolicyWithoutClient 验证 redis 策略缺客户端时报错。
//
// 报错必须指出出路（配 Redis 或改用 local store），否则使用者只看到
// 一个"没有 redis client"的事实，不知道该往哪改。
func TestBuildRateLimitsRejectsRedisPolicyWithoutClient(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		App: config.AppConfig{Name: "svc"},
		RateLimit: config.RateLimitConfig{
			KeyHMACSecret: strings.Repeat("s", 32),
			Policies: []config.RateLimitPolicyConfig{policyConfig(func(p *config.RateLimitPolicyConfig) {
				p.Store = "redis"
			})},
		},
	}

	_, err := buildRateLimits(context.Background(), cfg, nil)
	if err == nil {
		t.Fatal("buildRateLimits accepted a redis policy with no redis client available")
	}
	if !strings.Contains(err.Error(), "local store") {
		t.Fatalf("error %q does not point at a way forward", err)
	}
}
