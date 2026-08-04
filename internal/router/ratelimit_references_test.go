package router

import (
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/router/routespec"
	"github.com/gtkit/gin-api/pkg/ratelimit"
)

// rlPolicy 造一条合法策略，仅调整关心的字段。
func rlPolicy(mutate func(*ratelimit.Policy)) ratelimit.Policy {
	p := ratelimit.Policy{
		ID: "test.policy", Rate: 10, Burst: 5,
		Scope: ratelimit.ScopeIP, Store: ratelimit.StoreLocal,
		FailureMode: ratelimit.FailureClosed, MaxKeys: 100,
	}
	if mutate != nil {
		mutate(&p)
	}
	return p
}

// TestValidateRateLimitReferences 钉住两类装配错误。
//
// 引用未登记的策略若不拦住，那条路由会**完全没有限流**——而配置里
// 明明写了一个 ID，看起来一切正常。user scope 挂在公开路由上同理：
// 拿不到身份，整条路由退化成一个所有匿名请求共享的桶。
func TestValidateRateLimitReferences(t *testing.T) {
	t.Parallel()

	reg := ratelimit.NewRegistry()
	userPolicy := rlPolicy(func(p *ratelimit.Policy) {
		p.ID = "user.policy"
		p.Scope = ratelimit.ScopeUser
	})
	limiter, err := ratelimit.NewLocalBackend(userPolicy)
	if err != nil {
		t.Fatalf("ratelimit.NewLocalBackend: %v", err)
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
					Method: "GET", RateLimitPolicyIDs: []string{"missing.policy"},
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
					Method: "GET", RateLimitPolicyIDs: []string{"user.policy"},
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
					Method: "GET", RateLimitPolicyIDs: []string{"user.policy"},
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
					Method: "GET", RateLimitPolicyIDs: []string{"user.policy"},
					AccessPolicy: routespec.AccessPolicy{Mode: routespec.AccessOptionalAuthenticated},
				},
			},
			wantErr: true, wantHint: "does not require authentication",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateRateLimitReferences(reg, []routespec.CompiledRoute{tc.route})
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("validateRateLimitReferences() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("validateRateLimitReferences() = nil, want error")
			}
			if !strings.Contains(err.Error(), tc.wantHint) {
				t.Fatalf("error %q does not mention %q", err, tc.wantHint)
			}
		})
	}
}

// TestFixedPointConversion 验证定点换算与 Lua 精确整数边界。
