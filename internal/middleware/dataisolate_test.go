package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/pkg/ginc"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
	"github.com/gtkit/gin-api/internal/shared/tenant"
)

// tenantProbe 装一条链路：可选的认证桩 → TenantIsolation → 读取请求上下文的探针。
//
// 探针从 c.Request.Context() 取值，而不是从 gin 的 Keys：那正是数据访问层
// 拿到的那条 context，也是本中间件唯一有意义的写入目标。
func tenantProbe(t *testing.T, resolve TenantResolver, authenticated bool) (*httptest.ResponseRecorder, *tenant.ID, *bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	var seen tenant.ID
	reached := false

	engine := gin.New()
	if authenticated {
		engine.Use(func(c *gin.Context) {
			ginc.SetPrincipal(c, sharedauth.Principal{UserID: 7, Roles: []string{"user"}})
			c.Next()
		})
	}
	engine.Use(TenantIsolation(resolve))
	engine.GET("/probe", func(c *gin.Context) {
		reached = true
		if id, ok := tenant.From(c.Request.Context()); ok {
			seen = id
		}
		c.Status(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	// 刻意带上租户头：它**不该**成为身份来源。见 TestTenantHeaderAloneYieldsNoTenant。
	req.Header.Set("X-Tenant-Id", "attacker")
	engine.ServeHTTP(rec, req)

	return rec, &seen, &reached
}

func TestTenantIsolationWritesResolvedTenantIntoRequestContext(t *testing.T) {
	t.Parallel()

	resolve := func(context.Context, sharedauth.Principal) (tenant.ID, error) {
		return "acme", nil
	}
	rec, seen, reached := tenantProbe(t, resolve, true)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !*reached {
		t.Fatal("handler was not reached")
	}
	if *seen != "acme" {
		t.Fatalf("tenant in request context = %q, want %q", *seen, "acme")
	}
}

// TestTenantHeaderAloneYieldsNoTenant 是本中间件最重要的一条判据。
//
// 租户只从已认证身份派生。若它能从 X-Tenant-ID 这类不算身份头的位置派生，
// 一个不带认证头的 GET 会被响应缓存判定为可缓存（缓存键里没有身份），
// 其响应随后被发给另一个租户的同一 URL 请求——数据库侧隔离做得再对也没用。
func TestTenantHeaderAloneYieldsNoTenant(t *testing.T) {
	t.Parallel()

	resolve := func(context.Context, sharedauth.Principal) (tenant.ID, error) {
		t.Fatal("the resolver was called for an unauthenticated request")
		return "", nil
	}
	rec, seen, reached := tenantProbe(t, resolve, false)

	// 不中止：那条路由可能压根不碰受控表；真碰了由数据访问层 fail-closed。
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (an unauthenticated route must not be aborted here)", rec.Code, http.StatusOK)
	}
	if !*reached {
		t.Fatal("handler was not reached")
	}
	if *seen != "" {
		t.Fatalf("an unauthenticated request produced tenant %q", *seen)
	}
}

// TestTenantIsolationFailsClosedOnResolverProblems 覆盖三类装配/依赖故障。
//
// 三者都必须是 503 而不是放行：放行等于隔离没装而没人知道。
// 也都不该是 403——把"归属表挂了"映射成拒绝，会让一次基础设施故障
// 表现为全体用户突然失去权限。
func TestTenantIsolationFailsClosedOnResolverProblems(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		resolve TenantResolver
	}{
		{
			name:    "未注入 resolver",
			resolve: nil,
		},
		{
			name: "resolver 报依赖故障",
			resolve: func(context.Context, sharedauth.Principal) (tenant.ID, error) {
				return "", context.DeadlineExceeded
			},
		},
		{
			name: "resolver 返回空租户",
			resolve: func(context.Context, sharedauth.Principal) (tenant.ID, error) {
				return "", nil
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec, seen, reached := tenantProbe(t, tc.resolve, true)

			if rec.Code == http.StatusOK {
				t.Fatalf("status = %d; the request was allowed through without a tenant", rec.Code)
			}
			if *reached {
				t.Fatal("the handler ran despite an unresolved tenant")
			}
			if *seen != "" {
				t.Fatalf("a tenant leaked into the context: %q", *seen)
			}
		})
	}
}

// TestResolverSeesTheAuthenticatedPrincipal 钉住 resolver 拿到的是认证结论。
//
// 它是「只从已认证身份派生」这条契约的另一半：不只是"未认证时不解析"，
// 还要"解析时看到的确实是认证产出的主体"。
func TestResolverSeesTheAuthenticatedPrincipal(t *testing.T) {
	t.Parallel()

	var got sharedauth.Principal
	resolve := func(_ context.Context, p sharedauth.Principal) (tenant.ID, error) {
		got = p
		return "acme", nil
	}
	if _, _, reached := tenantProbe(t, resolve, true); !*reached {
		t.Fatal("handler was not reached")
	}

	if got.UserID != 7 {
		t.Fatalf("resolver saw UserID %d, want 7", got.UserID)
	}
}
