package router

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/pkg/ginc"
	"github.com/gtkit/gin-api/internal/ratelimit"
	"github.com/gtkit/gin-api/internal/router/routespec"

	"github.com/gin-gonic/gin"
)

// 刻意用可读的低熵字符串而不是十六进制占位串：后者会被密钥扫描器判为
// 疑似泄漏，而那种假阳性只能靠登记表压制——登记表需要长期维护与续期，
// 消除成因比登记症状便宜得多。长度仍满足 KeyBuilder 的下限要求。
const testKeySecret = "rate-limit-test-secret-not-a-real-credential"

// rateLimitModule 是一个只提供若干条 RouteSpec 的测试模块。
type rateLimitModule struct{ routes []routespec.RouteSpec }

func (m rateLimitModule) Routes() []routespec.RouteSpec { return m.routes }
func (m rateLimitModule) Name() string                  { return "ratelimit-test" }

func cooperativeTimeoutPolicy() routespec.TimeoutPolicy {
	return routespec.TimeoutPolicy{
		Mode: routespec.TimeoutCooperative, Deadline: 2 * time.Second, WriteGrace: time.Second,
	}
}

func newRateLimitRegistry(t *testing.T, policies ...routespec.RateLimitPolicy) *ratelimit.Registry {
	t.Helper()

	reg := ratelimit.NewRegistry()
	for _, p := range policies {
		limiter, err := ratelimit.NewLocalBackend(p)
		if err != nil {
			t.Fatalf("NewLocalBackend(%s): %v", p.ID, err)
		}
		if err := reg.Register(limiter); err != nil {
			t.Fatalf("Register(%s): %v", p.ID, err)
		}
	}
	reg.Freeze()
	t.Cleanup(func() { _ = reg.Close() })
	return reg
}

func newKeyBuilder(t *testing.T) *ratelimit.KeyBuilder {
	t.Helper()
	kb, err := ratelimit.NewKeyBuilder(testKeySecret, "test")
	if err != nil {
		t.Fatalf("NewKeyBuilder: %v", err)
	}
	return kb
}

// TestRouteRateLimitRejectsBeyondBurst 验证限流真的接进了 handler 链。
//
// 策略只在 RouteSpec 里声明就以为它生效，是本批次要消灭的形态之一：
// 描述符与执行必须由同一条链接起来。
func TestRouteRateLimitRejectsBeyondBurst(t *testing.T) {
	gin.SetMode(gin.TestMode)

	policy := routespec.RateLimitPolicy{
		ID: "test.ip", Rate: 0.01, Burst: 2,
		Scope: routespec.ScopeIP, Store: routespec.StoreLocal,
		FailureMode: routespec.FailureClosed, MaxKeys: 100,
	}

	engine, err := InitRouterWithVersionedModules("test", VersionedModules{
		V1: []Module{rateLimitModule{routes: []routespec.RouteSpec{{
			Method:      http.MethodGet,
			OperationID: "op6", Path: "/limited",
			Handler:           func(c *gin.Context) { c.String(http.StatusOK, "ok") },
			AccessPolicy:      routespec.AccessPolicy{Mode: routespec.AccessPublic},
			TimeoutPolicy:     cooperativeTimeoutPolicy(),
			ResponseShape:     routespec.ResponseShapeEmpty,
			RateLimitPolicyID: policy.ID,
		}}}},
	}, RouterOptions{
		RateLimits:    newRateLimitRegistry(t, policy),
		RateLimitKeys: newKeyBuilder(t),
	})
	if err != nil {
		t.Fatalf("InitRouterWithVersionedModules: %v", err)
	}

	do := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/limited", nil)
		req.RemoteAddr = "192.0.2.10:1234"
		engine.ServeHTTP(w, req)
		return w
	}

	for i := range 2 {
		if w := do(); w.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200 (within burst)", i, w.Code)
		}
	}

	w := do()
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status beyond burst = %d, want 429 (body %s)", w.Code, w.Body.String())
	}

	// Retry-After 必须是合法的正整数秒。
	retryAfter := w.Header().Get("Retry-After")
	seconds, convErr := strconv.Atoi(retryAfter)
	if convErr != nil {
		t.Fatalf("Retry-After = %q, want an integer number of seconds", retryAfter)
	}
	if seconds < 1 {
		t.Fatalf("Retry-After = %d, want at least 1: 0 is an illegal value that means 'retry now'", seconds)
	}
	if got := w.Header().Get("X-Ratelimit-Limit"); got != "2" {
		t.Fatalf("X-Ratelimit-Limit = %q, want 2", got)
	}
}

// TestRouteRateLimitSeparatesSubjects 验证不同 IP 各有独立配额。
//
// 共用一个桶的话，一个客户端就能把所有人的配额打光——那不是限流，
// 而是一个由第三方触发的全局熔断。
func TestRouteRateLimitSeparatesSubjects(t *testing.T) {
	gin.SetMode(gin.TestMode)

	policy := routespec.RateLimitPolicy{
		ID: "test.per-ip", Rate: 0.01, Burst: 1,
		Scope: routespec.ScopeIP, Store: routespec.StoreLocal,
		FailureMode: routespec.FailureClosed, MaxKeys: 100,
	}

	engine, err := InitRouterWithVersionedModules("test", VersionedModules{
		V1: []Module{rateLimitModule{routes: []routespec.RouteSpec{{
			Method:      http.MethodGet,
			OperationID: "op7", Path: "/limited",
			Handler:           func(c *gin.Context) { c.String(http.StatusOK, "ok") },
			AccessPolicy:      routespec.AccessPolicy{Mode: routespec.AccessPublic},
			TimeoutPolicy:     cooperativeTimeoutPolicy(),
			ResponseShape:     routespec.ResponseShapeEmpty,
			RateLimitPolicyID: policy.ID,
		}}}},
	}, RouterOptions{
		RateLimits:    newRateLimitRegistry(t, policy),
		RateLimitKeys: newKeyBuilder(t),
	})
	if err != nil {
		t.Fatalf("InitRouterWithVersionedModules: %v", err)
	}

	do := func(remote string) int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/limited", nil)
		req.RemoteAddr = remote
		engine.ServeHTTP(w, req)
		return w.Code
	}

	if got := do("192.0.2.10:1111"); got != http.StatusOK {
		t.Fatalf("first client status = %d, want 200", got)
	}
	if got := do("192.0.2.10:1111"); got != http.StatusTooManyRequests {
		t.Fatalf("same client second request = %d, want 429", got)
	}
	// 另一个 IP 必须仍有自己的配额。
	if got := do("198.51.100.7:2222"); got != http.StatusOK {
		t.Fatalf("a different client got %d; subjects must have independent buckets", got)
	}
}

// TestUnknownRateLimitPolicyFailsAssembly 钉住引用未登记策略即启动失败。
//
// 不拦住的话，那条路由会完全没有限流——而 RouteSpec 里明明写了一个 ID，
// 代码审查时看起来一切正常。
func TestUnknownRateLimitPolicyFailsAssembly(t *testing.T) {
	gin.SetMode(gin.TestMode)

	_, err := InitRouterWithVersionedModules("test", VersionedModules{
		V1: []Module{rateLimitModule{routes: []routespec.RouteSpec{{
			Method:      http.MethodGet,
			OperationID: "op8", Path: "/limited",
			Handler:           func(c *gin.Context) { c.Status(http.StatusOK) },
			AccessPolicy:      routespec.AccessPolicy{Mode: routespec.AccessPublic},
			TimeoutPolicy:     cooperativeTimeoutPolicy(),
			ResponseShape:     routespec.ResponseShapeEmpty,
			RateLimitPolicyID: "never.registered",
		}}}},
	}, RouterOptions{
		RateLimits:    newRateLimitRegistry(t),
		RateLimitKeys: newKeyBuilder(t),
	})
	if err == nil {
		t.Fatal("assembly succeeded with a route referencing an unregistered rate limit policy")
	}
	if !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("error %q does not explain the missing policy", err)
	}
}

// TestRateLimitPolicyWithoutRegistryFailsAssembly 验证声明了策略却没给注册表时失败。
func TestRateLimitPolicyWithoutRegistryFailsAssembly(t *testing.T) {
	gin.SetMode(gin.TestMode)

	_, err := InitRouterWithVersionedModules("test", VersionedModules{
		V1: []Module{rateLimitModule{routes: []routespec.RouteSpec{{
			Method:      http.MethodGet,
			OperationID: "op9", Path: "/limited",
			Handler:           func(c *gin.Context) { c.Status(http.StatusOK) },
			AccessPolicy:      routespec.AccessPolicy{Mode: routespec.AccessPublic},
			TimeoutPolicy:     cooperativeTimeoutPolicy(),
			ResponseShape:     routespec.ResponseShapeEmpty,
			RateLimitPolicyID: "test.ip",
		}}}},
	}, RouterOptions{})
	if err == nil {
		t.Fatal("assembly succeeded with rate limit policies declared but no registry provided")
	}
	if !strings.Contains(err.Error(), "no policy registry") {
		t.Fatalf("error %q does not explain the missing registry", err)
	}
}

// TestUserScopedPolicyOnPublicRouteFailsAssembly 钉住 scope 与访问策略的相容性。
//
// user scope 挂在公开路由上时拿不到身份，整条路由退化成一个所有匿名请求
// 共享的桶——一个用户就能打满它，而它看起来"限流是配了的"。
func TestUserScopedPolicyOnPublicRouteFailsAssembly(t *testing.T) {
	gin.SetMode(gin.TestMode)

	policy := routespec.RateLimitPolicy{
		ID: "test.user", Rate: 1, Burst: 1,
		Scope: routespec.ScopeUser, Store: routespec.StoreLocal,
		FailureMode: routespec.FailureClosed, MaxKeys: 100,
	}

	_, err := InitRouterWithVersionedModules("test", VersionedModules{
		V1: []Module{rateLimitModule{routes: []routespec.RouteSpec{{
			Method:      http.MethodGet,
			OperationID: "op10", Path: "/public",
			Handler:           func(c *gin.Context) { c.Status(http.StatusOK) },
			AccessPolicy:      routespec.AccessPolicy{Mode: routespec.AccessPublic},
			TimeoutPolicy:     cooperativeTimeoutPolicy(),
			ResponseShape:     routespec.ResponseShapeEmpty,
			RateLimitPolicyID: policy.ID,
		}}}},
	}, RouterOptions{
		RateLimits:    newRateLimitRegistry(t, policy),
		RateLimitKeys: newKeyBuilder(t),
	})
	if err == nil {
		t.Fatal("assembly succeeded with a user-scoped policy on a public route")
	}
	if !strings.Contains(err.Error(), "does not require authentication") {
		t.Fatalf("error %q does not explain the scope mismatch", err)
	}
}

// TestUserScopedRateLimitRunsAfterAuthentication 验证 user 限流排在鉴权之后。
//
// 排在之前的话拿不到 userID，中间件只能拒绝——整条路由变成恒 429。
func TestUserScopedRateLimitRunsAfterAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)

	policy := routespec.RateLimitPolicy{
		ID: "test.user-scope", Rate: 0.01, Burst: 1,
		Scope: routespec.ScopeUser, Store: routespec.StoreLocal,
		FailureMode: routespec.FailureClosed, MaxKeys: 100,
	}

	engine, err := InitRouterWithVersionedModules("test", VersionedModules{
		V1: []Module{rateLimitModule{routes: []routespec.RouteSpec{{
			Method:      http.MethodGet,
			OperationID: "op11", Path: "/me",
			Handler:           func(c *gin.Context) { c.String(http.StatusOK, "ok") },
			AccessPolicy:      routespec.AccessPolicy{Mode: routespec.AccessAuthenticated},
			TimeoutPolicy:     cooperativeTimeoutPolicy(),
			ResponseShape:     routespec.ResponseShapeEmpty,
			RateLimitPolicyID: policy.ID,
		}}}},
	}, RouterOptions{
		// 认证中间件写入固定用户，模拟鉴权成功。
		AuthMiddleware: func(c *gin.Context) {
			ginc.SetUserID(c, 4242)
			c.Next()
		},
		RateLimits:    newRateLimitRegistry(t, policy),
		RateLimitKeys: newKeyBuilder(t),
	})
	if err != nil {
		t.Fatalf("InitRouterWithVersionedModules: %v", err)
	}

	do := func() int {
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/me", nil))
		return w.Code
	}

	// 第一次必须放行——若限流排在鉴权之前，这里就拿不到身份而恒 429。
	if got := do(); got != http.StatusOK {
		t.Fatalf("first authenticated request = %d, want 200: the user-scoped limiter must run after auth", got)
	}
	if got := do(); got != http.StatusTooManyRequests {
		t.Fatalf("second request = %d, want 429", got)
	}
}
