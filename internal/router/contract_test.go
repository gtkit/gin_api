package router

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/router/routespec"
)

// contractModule 贡献一组已知路由，供契约测试比对。
type contractModule struct{}

func (contractModule) Name() string { return "module.contract" }

func (contractModule) Routes() []routespec.RouteSpec {
	timeout := routespec.TimeoutPolicy{
		Mode: routespec.TimeoutCooperative, Deadline: time.Second, WriteGrace: time.Second,
	}
	public := routespec.AccessPolicy{Mode: routespec.AccessPublic}
	h := func(c *gin.Context) { c.Status(http.StatusNoContent) }

	return []routespec.RouteSpec{
		{Method: http.MethodGet, Path: "/thing", Handler: h, AccessPolicy: public, TimeoutPolicy: timeout},
		{Method: http.MethodPost, Path: "/thing", Handler: h, AccessPolicy: public, TimeoutPolicy: timeout},
		{Method: http.MethodGet, Path: "/thing/:id", Handler: h, AccessPolicy: public, TimeoutPolicy: timeout},
	}
}

// buildContractEngine 构建与生产同路径的 engine。
//
// 调用它的用例一律**不能** t.Parallel()：engine 构建会写 gin.SetMode 这个进程级
// 全局状态，与其它读取 gin mode 的用例并行即构成 data race。
func buildContractEngine(t *testing.T) *gin.Engine {
	t.Helper()
	engine, err := InitRouterWithVersionedModules("test",
		VersionedModules{V1: []Module{contractModule{}}},
		RouterOptions{HealthChecker: &stubChecker{}})
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}
	return engine
}

// TestRouteContractIsBidirectional 是唯一 registrar 的核心判据。
//
// 只做单向（spec ⊆ routes）会漏掉最危险的一类：**注册了但没有 spec 的旁路路由**——
// 它没有访问策略、没有超时预算，却真实对外服务。因此必须互为子集。
func TestRouteContractIsBidirectional(t *testing.T) {
	engine := buildContractEngine(t)

	declared := map[string]struct{}{}
	for _, r := range systemRoutes(&stubChecker{}, nil, false) {
		declared[r.Method+" "+r.Path] = struct{}{}
	}
	for _, r := range (contractModule{}).Routes() {
		declared[r.Method+" /api/v1"+r.Path] = struct{}{}
	}

	registered := map[string]struct{}{}
	for _, r := range engine.Routes() {
		registered[r.Method+" "+r.Path] = struct{}{}
	}

	for key := range declared {
		if _, ok := registered[key]; !ok {
			t.Errorf("declared in RouteSpec but not registered: %s", key)
		}
	}
	for key := range registered {
		if _, ok := declared[key]; !ok {
			t.Errorf("registered but has no RouteSpec (bypass route): %s", key)
		}
	}
}

// TestUnknownDottedPathReturns404 覆盖一个被修掉的真实缺陷：
// 旧实现在路径含 "." 时直接 return，既不写状态也不写响应体，
// 于是扫描流量可以伪装成静态资源逃避审计，客户端还拿到一个空响应。
func TestUnknownDottedPathReturns404(t *testing.T) {
	engine := buildContractEngine(t)

	paths := []string{
		"/foo.json", "/.well-known/x", "/favicon.ico",
		"/index.html", "/a.b.c", "/unknown",
	}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))

			if w.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
			}
			if w.Body.Len() == 0 {
				t.Fatal("404 must have a response body; an empty response leaks nothing but tells the client nothing either")
			}
		})
	}
}

// TestMethodNotAllowedWritesStableAllowHeader 锁住 405 的 Allow 头。
//
// 不依赖 Gin 自己写的 Allow：其顺序来自内部 method tree 的实现细节，
// 且不含由 CORS 派生的 OPTIONS。Allow 是对外契约，必须稳定且完整。
func TestMethodNotAllowedWritesStableAllowHeader(t *testing.T) {
	engine := buildContractEngine(t)

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/thing", nil))

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
	allow := w.Header().Get("Allow")
	// 固定顺序：GET, HEAD, POST, OPTIONS —— HEAD 由 GET 派生，OPTIONS 由 CORS 派生。
	if allow != "GET, HEAD, POST, OPTIONS" {
		t.Fatalf("Allow = %q, want %q", allow, "GET, HEAD, POST, OPTIONS")
	}
}

// TestMethodNotAllowedIsNotConfusedWith404 守住 404/405 的边界：
// 把未知路径报成 405 会反过来泄漏"哪些路径存在"。
func TestUnknownPathIsNotReportedAsMethodNotAllowed(t *testing.T) {
	engine := buildContractEngine(t)

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/nonexistent", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (an unknown path must be 404, not 405)", w.Code, http.StatusNotFound)
	}
	if allow := w.Header().Get("Allow"); allow != "" {
		t.Fatalf("Allow header on an unknown path leaks route existence: %q", allow)
	}
}

// TestRegistrarRefusesUnwiredAccessMode 守住 fail-closed 的最后一环：
// 声明了鉴权但没有对应实现时，绝不能当作公开路由服务。
func TestRegistrarRefusesUnwiredAccessMode(t *testing.T) {
	// 不并行：构建 engine 会调用 gin.SetMode，那是进程级全局写，
	// 与其它读取 gin mode 的用例并行会构成 data race。
	_, err := InitRouterWithVersionedModules("test",
		VersionedModules{V1: []Module{authRequiringModule{}}},
		RouterOptions{HealthChecker: &stubChecker{}}) // 刻意不提供 AuthMiddleware
	if err == nil {
		t.Fatal("a route requiring authentication must not start without an authentication middleware")
	}
	if !strings.Contains(err.Error(), "authentication") {
		t.Fatalf("error should explain the missing authentication wiring, got %v", err)
	}
}

type authRequiringModule struct{}

func (authRequiringModule) Name() string { return "module.auth" }

func (authRequiringModule) Routes() []routespec.RouteSpec {
	return []routespec.RouteSpec{{
		Method: http.MethodGet, Path: "/secret",
		Handler:       func(c *gin.Context) { c.Status(http.StatusOK) },
		AccessPolicy:  routespec.AccessPolicy{Mode: routespec.AccessAuthenticated},
		TimeoutPolicy: routespec.TimeoutPolicy{Mode: routespec.TimeoutCooperative, Deadline: time.Second, WriteGrace: time.Second},
	}}
}

// TestAuthenticatedRouteRunsAuthMiddleware 证明鉴权确实被装配进链，
// 而不只是"描述符里写了"。
func TestAuthenticatedRouteRunsAuthMiddleware(t *testing.T) {
	// 不并行：构建 engine 会调用 gin.SetMode，那是进程级全局写，
	// 与其它读取 gin mode 的用例并行会构成 data race。
	called := false
	engine, err := InitRouterWithVersionedModules("test",
		VersionedModules{V1: []Module{authRequiringModule{}}},
		RouterOptions{
			HealthChecker:  &stubChecker{},
			AuthMiddleware: func(c *gin.Context) { called = true; c.Next() },
		})
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/secret", nil))
	if !called {
		t.Fatal("authentication middleware was not invoked for an authenticated route")
	}
}

// TestSystemRoutesAreDeclaredNotBypassed 锁住系统探针也走描述符。
//
// 它们此前由 engine.GET 旁路注册，且发生在模块路由之后——那会让
// "实际路由 ⇄ RouteSpec 双向集合相等" 必然失败，探针也拿不到请求级超时预算。
func TestSystemRoutesAreDeclaredNotBypassed(t *testing.T) {
	t.Parallel()

	specs := systemRoutes(&stubChecker{}, nil, false)
	paths := make([]string, 0, len(specs))
	for _, s := range specs {
		paths = append(paths, s.Path)
		if s.AccessPolicy.Mode != routespec.AccessPublic {
			t.Fatalf("%s must be public", s.Path)
		}
		if s.TimeoutPolicy.Mode == routespec.TimeoutUnspecified {
			t.Fatalf("%s must declare a timeout policy", s.Path)
		}
		// 探针必须比业务路由更早失败，否则编排器的判定同样迟钝。
		if s.TimeoutPolicy.Deadline >= userRouteDeadlineUpperBound {
			t.Fatalf("%s deadline %s should be well below a business route budget", s.Path, s.TimeoutPolicy.Deadline)
		}
	}
	for _, want := range []string{"/health", "/ready"} {
		if !slices.Contains(paths, want) {
			t.Fatalf("system routes must include %s, got %v", want, paths)
		}
	}
}

// userRouteDeadlineUpperBound 是"业务路由预算"的参考上界，仅用于断言探针明显更短。
const userRouteDeadlineUpperBound = 5 * time.Second
