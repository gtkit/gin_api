package config

import (
	"strings"
	"testing"
	"time"
)

// TestAuthModeMustBeChosenExplicitly 钉住"忘记选择必须失败"。
//
// 这是本次最重要的一条：任何一种默认都会静默地给出错误的系统——
// 默认 local 会给外部身份的部署凭空加一组凭据端点，默认 external 会让
// 使用者以为认证已就绪却没有签发入口。
func TestAuthModeMustBeChosenExplicitly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mode    AuthMode
		wantErr bool
		// wantHints 是错误里必须出现的字样：只说"非法"不能让人知道该填什么。
		wantHints []string
	}{
		{
			name: "local passes", mode: AuthModeLocal,
		},
		{
			name: "external passes", mode: AuthModeExternal,
		},
		{
			name: "empty is rejected", mode: "", wantErr: true,
			wantHints: []string{"auth.mode is required", "local", "external"},
		},
		{
			name: "unknown value is rejected", mode: "oauth", wantErr: true,
			wantHints: []string{"invalid", "local|external"},
		},
		{
			// 大小写不归一化：与 env 的处理一致。"Local" 静默当作 local,
			// 会让人以为配置系统会帮忙纠错，下一个手误就未必被接住了。
			name: "case variant is rejected", mode: "Local", wantErr: true,
			wantHints: []string{"invalid"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateAuthConfig(AuthConfig{Mode: tt.mode}, RedisConfig{Addr: "127.0.0.1:6379"}, testAccessTTL)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("mode %q passed validation, want it rejected", tt.mode)
				}
				for _, hint := range tt.wantHints {
					if !strings.Contains(err.Error(), hint) {
						t.Errorf("error %q does not mention %q", err, hint)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("mode %q rejected: %v", tt.mode, err)
			}
		})
	}
}

// TestLocalAuthRequiresRevocationStore 钉住缺撤销存储时 fail-closed。
//
// "能登录但登不出"比没有认证更危险：系统看起来是完整的，使用者不会再去补，
// 直到需要回收一个泄漏的令牌时才发现无从下手。
func TestLocalAuthRequiresRevocationStore(t *testing.T) {
	t.Parallel()

	err := validateAuthConfig(AuthConfig{Mode: AuthModeLocal}, RedisConfig{}, testAccessTTL)
	if err == nil {
		t.Fatal("local auth without a revocation store passed validation: " +
			"it must fail closed, otherwise logout silently does nothing")
	}
	// 错误必须给两条出路：使用者的真实意图可能是任何一种，只说"配 redis"
	// 会让打算接外部身份的人以为自己非装 Redis 不可。
	for _, want := range []string{"redis", "external"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not offer the %q way out", err, want)
		}
	}

	// 外部身份模式不需要撤销存储：令牌由外部签发，撤销也归外部。
	if err := validateAuthConfig(AuthConfig{Mode: AuthModeExternal}, RedisConfig{}, testAccessTTL); err != nil {
		t.Fatalf("external mode should not require a revocation store: %v", err)
	}
}

// testAccessTTL 是测试用的访问令牌寿命，模拟 jwt.token_ttl_hours 的生效值。
const testAccessTTL = time.Hour

// TestRefreshMustOutliveAccess 钉住寿命关系。
//
// 刷新令牌比访问令牌先过期，等于没有刷新机制——用户仍会被踢去重新登录，
// 而配置看起来是"两个都配了"。
//
// 访问令牌寿命来自 jwt.token_ttl_hours（签发令牌的是 JWT 装配），
// 因此这里用参数传入，而不是 AuthConfig 的字段。
func TestRefreshMustOutliveAccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mode    AuthMode
		access  time.Duration
		refresh time.Duration
		wantErr bool
	}{
		{name: "defaults are consistent", mode: AuthModeLocal, access: testAccessTTL},
		{name: "refresh longer passes", mode: AuthModeLocal, access: time.Minute, refresh: time.Hour},
		{
			name: "refresh shorter is rejected", mode: AuthModeLocal,
			access: 48 * time.Hour, refresh: time.Minute, wantErr: true,
		},
		{
			name: "equal is rejected", mode: AuthModeLocal,
			access: time.Hour, refresh: time.Hour, wantErr: true,
		},
		{
			// 外部身份模式不签发这两种令牌：那里的 jwt TTL 描述的是本服务
			// **接受**的外部令牌，与刷新无关，不该因此拒绝启动。
			name: "external mode is not constrained", mode: AuthModeExternal,
			access: 48 * time.Hour, refresh: time.Minute,
		},
		{name: "negative refresh is rejected", mode: AuthModeExternal, refresh: -time.Second, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateAuthConfig(AuthConfig{
				Mode:       tt.mode,
				RefreshTTL: tt.refresh,
			}, RedisConfig{Addr: "127.0.0.1:6379"}, tt.access)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("access=%s refresh=%s: error = %v, wantErr = %v",
					tt.access, tt.refresh, err, tt.wantErr)
			}
		})
	}
}

// TestAccessTTLHasASingleSource 钉住访问令牌寿命只有一个来源。
//
// 两个来源的后果是 expires_in 与令牌真实的 exp 对不上：客户端要么在令牌
// 已经失效后才去刷新，要么提前把还能用的令牌丢掉。
func TestAccessTTLHasASingleSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		hours int
		mode  AuthMode
		want  time.Duration
	}{
		{name: "explicit value wins (local)", hours: 2, mode: AuthModeLocal, want: 2 * time.Hour},
		{name: "explicit value wins (external)", hours: 2, mode: AuthModeExternal, want: 2 * time.Hour},
		// 分模式兜底（local-authentication spec）：本地模式有刷新令牌承担续期，
		// 访问令牌默认短寿命；外部模式描述"接受的外部令牌"，兜底保持宽松。
		{name: "local zero falls back to short ttl", hours: 0, mode: AuthModeLocal, want: defaultLocalTokenTTL},
		{name: "local negative falls back", hours: -1, mode: AuthModeLocal, want: defaultLocalTokenTTL},
		{name: "external zero keeps legacy ttl", hours: 0, mode: AuthModeExternal, want: defaultExternalTokenTTLHours * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := (JWTConfig{TokenTTLHours: tt.hours}).EffectiveTokenTTL(tt.mode); got != tt.want {
				t.Fatalf("EffectiveTokenTTL(%s) = %s, want %s", tt.mode, got, tt.want)
			}
		})
	}
}

// TestBcryptCostFloor 钉住代价下限。
//
// bcrypt 包自身允许低至 4——那个下限是给测试用的，配到生产上等于没有哈希。
func TestBcryptCostFloor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cost    int
		wantErr bool
		// wantEffective 是留空时应当生效的值。
		wantEffective int
	}{
		{name: "zero takes the default", cost: 0, wantEffective: defaultBcryptCost},
		{name: "at the floor passes", cost: minBcryptCost, wantEffective: minBcryptCost},
		{name: "below the floor is rejected", cost: minBcryptCost - 1, wantErr: true},
		{name: "bcrypt test-only cost is rejected", cost: 4, wantErr: true},
		{name: "above the maximum is rejected", cost: maxBcryptCost + 1, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := AuthConfig{Mode: AuthModeExternal, BcryptCost: tt.cost}
			err := validateAuthConfig(cfg, RedisConfig{}, testAccessTTL)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("cost %d: error = %v, wantErr = %v", tt.cost, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got := cfg.EffectiveBcryptCost(); got != tt.wantEffective {
				t.Errorf("effective cost = %d, want %d", got, tt.wantEffective)
			}
		})
	}
}
