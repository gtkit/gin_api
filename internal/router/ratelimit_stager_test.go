package router

import (
	"strings"
	"testing"

	"github.com/gtkit/gin-api/pkg/ratelimit"
)

// newRateLimitTestOptions 构造带两条已注册策略（ip / user 作用域各一）的 Options。
func newRateLimitTestOptions(t *testing.T) Options {
	t.Helper()

	registry := ratelimit.NewRegistry()
	for _, p := range []ratelimit.Policy{
		{ID: "default.ip", Rate: 5, Burst: 20, Scope: ratelimit.ScopeIP,
			Store: ratelimit.StoreLocal, FailureMode: ratelimit.FailureClosed, MaxKeys: 1000},
		{ID: "default.user", Rate: 10, Burst: 50, Scope: ratelimit.ScopeUser,
			Store: ratelimit.StoreLocal, FailureMode: ratelimit.FailureClosed, MaxKeys: 1000},
	} {
		limiter, err := ratelimit.NewLocalBackend(p)
		if err != nil {
			t.Fatalf("NewLocalBackend(%s): %v", p.ID, err)
		}
		if err := registry.Register(limiter); err != nil {
			t.Fatalf("Register(%s): %v", p.ID, err)
		}
	}
	registry.Freeze()
	t.Cleanup(func() { _ = registry.Close() })

	keys, err := ratelimit.NewKeyBuilder(strings.Repeat("k", 32), "test-app")
	if err != nil {
		t.Fatalf("NewKeyBuilder: %v", err)
	}
	return Options{RateLimits: registry, RateLimitKeys: keys}
}

type stagerCall struct {
	route string
	ids   []string
}

// TestNewRateLimitStagerAssemblyResolution 钉住装配期解析契约（rate-limiting spec）：
// 路由引用的策略标识必须在装配期可解析，无法解析（含注册表缺失）即装配失败，
// 错误指明路由与标识并给出迁移指引；无任何引用时空注册表可正常启动。
func TestNewRateLimitStagerAssemblyResolution(t *testing.T) {
	tests := []struct {
		name            string
		options         func(t *testing.T) Options
		calls           []stagerCall
		wantPre         []int // 每次调用返回的认证前中间件数量
		wantPost        []int // 每次调用返回的认证后中间件数量
		wantErrContains []string
	}{
		{
			name:     "全部可解析时按作用域分组且判定通过",
			options:  newRateLimitTestOptions,
			calls:    []stagerCall{{route: "/user", ids: []string{"default.ip", "default.user"}}},
			wantPre:  []int{1},
			wantPost: []int{1},
		},
		{
			name:            "未注册标识导致装配失败并指明路由与标识",
			options:         newRateLimitTestOptions,
			calls:           []stagerCall{{route: "/user", ids: []string{"default.ip", "no.such.policy"}}},
			wantPre:         []int{1},
			wantPost:        []int{0},
			wantErrContains: []string{"/user", "no.such.policy", "default.ip", "ratelimit.policies"},
		},
		{
			name:            "存在引用但注册表缺失即装配失败",
			options:         func(*testing.T) Options { return Options{} },
			calls:           []stagerCall{{route: "/user", ids: []string{"default.ip"}}},
			wantPre:         []int{0},
			wantPost:        []int{0},
			wantErrContains: []string{"/user", "default.ip"},
		},
		{
			name:    "无任何引用时注册表缺失可正常启动",
			options: func(*testing.T) Options { return Options{} },
		},
		{
			name:    "多条路由的未解析引用全部列出",
			options: func(*testing.T) Options { return Options{} },
			calls: []stagerCall{
				{route: "/user", ids: []string{"default.ip"}},
				{route: "/auth", ids: []string{"auth.credential"}},
			},
			wantPre:         []int{0, 0},
			wantPost:        []int{0, 0},
			wantErrContains: []string{"/user", "default.ip", "/auth", "auth.credential"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stager, _, unresolved := NewRateLimitStager(tt.options(t))

			for i, call := range tt.calls {
				pre, post := stager(call.route, call.ids...)
				if len(pre) != tt.wantPre[i] || len(post) != tt.wantPost[i] {
					t.Fatalf("call %d (%s %v): got %d pre / %d post, want %d / %d",
						i, call.route, call.ids, len(pre), len(post), tt.wantPre[i], tt.wantPost[i])
				}
			}

			err := unresolved()
			if (len(tt.wantErrContains) > 0) != (err != nil) {
				t.Fatalf("unresolved() = %v, wantErr = %v", err, len(tt.wantErrContains) > 0)
			}
			for _, want := range tt.wantErrContains {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("unresolved() = %q, missing %q", err.Error(), want)
				}
			}
		})
	}
}

// TestPreIdentityRateLimiterRejectsIdentityScope 钉住 pre-identity 路由的装配契约
// （rate-limiting spec：用户作用域的策略不得用于不保证身份的路由）：
// 身份作用域的引用即使解析成功也不得静默丢弃——那会让凭据端点看着配了限流、
// 实际裸奔；必须记入装配错误并让启动失败。
func TestPreIdentityRateLimiterRejectsIdentityScope(t *testing.T) {
	tests := []struct {
		name            string
		options         func(t *testing.T) Options
		calls           []stagerCall
		wantMws         []int // 每次调用返回的中间件数量
		wantErrContains []string
	}{
		{
			name:    "来源作用域正常挂载且判定通过",
			options: newRateLimitTestOptions,
			calls:   []stagerCall{{route: "/auth", ids: []string{"default.ip"}}},
			wantMws: []int{1},
		},
		{
			name:            "身份作用域引用导致装配失败并指明原因",
			options:         newRateLimitTestOptions,
			calls:           []stagerCall{{route: "/auth", ids: []string{"default.ip", "default.user"}}},
			wantMws:         []int{1},
			wantErrContains: []string{"/auth", "default.user", "identity-scoped"},
		},
		{
			name:            "未注册标识同样进入装配错误",
			options:         newRateLimitTestOptions,
			calls:           []stagerCall{{route: "/auth", ids: []string{"no.such.policy"}}},
			wantMws:         []int{0},
			wantErrContains: []string{"/auth", "no.such.policy", "not registered"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, preIdentity, unresolved := NewRateLimitStager(tt.options(t))

			for i, call := range tt.calls {
				mws := preIdentity(call.route, call.ids...)
				if len(mws) != tt.wantMws[i] {
					t.Fatalf("call %d (%s %v): got %d middlewares, want %d",
						i, call.route, call.ids, len(mws), tt.wantMws[i])
				}
			}

			err := unresolved()
			if (len(tt.wantErrContains) > 0) != (err != nil) {
				t.Fatalf("unresolved() = %v, wantErr = %v", err, len(tt.wantErrContains) > 0)
			}
			for _, want := range tt.wantErrContains {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("unresolved() = %q, missing %q", err.Error(), want)
				}
			}
		})
	}
}
