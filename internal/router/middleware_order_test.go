package router

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// wantMiddlewareOrder 是全局中间件的**精确**顺序契约。
//
// 顺序不是实现细节：
//   - Recovery 不在最外层 → 它之前注册的中间件 panic 不被兜住，进程直接崩；
//   - RequestID 不够靠前 → 它之前产生的日志与错误响应都没有关联 ID；
//   - Logger/Metrics 排在 CORS/MaxBytes 之后 → 403/413 的响应没有访问记录
//     也不进指标，而那正是最需要留痕与统计的两类；
//   - SecurityHeaders 排在任何写 body 的中间件之后 → header 设置**静默失效**
//     （body 一开始写响应头就已发出），线上看不到任何报错；
//   - MaxBytesLimit 不在最后 → body 日志的读取不受同一上限约束。
//
// 因此这里用精确序列断言，而不是"包含某几个"。
var wantMiddlewareOrder = []string{
	"GinRecovery",
	"RequestID",
	"GinLogger",
	"Metrics",
	"GinRecovery",
	"SecurityHeaders",
	"ErrorHandler",
	"Cors",
	"MaxBytesLimit",
}

// TestGlobalMiddlewareOrderIsExact 锁住全局中间件顺序。
func TestGlobalMiddlewareOrderIsExact(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine, err := New("test", Options{
		AllowedOrigins: []string{"https://example.com"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got := middlewareNames(engine)
	if !reflect.DeepEqual(got, wantMiddlewareOrder) {
		t.Fatalf("middleware chain = %v, want exactly %v", got, wantMiddlewareOrder)
	}
}

// TestCorsIsOptional 钉住未配置来源时不挂 CORS。
//
// 挂一个空白名单的 CORS 会让每个跨源请求都被拒绝，而现象是"接口在浏览器里
// 不通、用 curl 却正常"——排查方向很容易被引向业务代码。
func TestCorsIsOptional(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine, err := New("test", Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, name := range middlewareNames(engine) {
		if name == "Cors" {
			t.Fatal("CORS was mounted without any allowed origin: every cross-origin " +
				"request would be rejected")
		}
	}
}

// TestSystemProbesAreOutsideTheAPIVersion 钉住探针不带版本前缀。
//
// 探针是运维接口。放进 /api/v1 的话，业务版本升级会连带改探针地址，
// 而编排系统的健康检查配置未必同步——那会让整批实例被判为不健康。
func TestSystemProbesAreOutsideTheAPIVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine, err := New("test", Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	paths := map[string]struct{}{}
	for _, r := range engine.Routes() {
		paths[r.Path] = struct{}{}
	}
	for _, want := range []string{"/health", "/ready"} {
		if _, ok := paths[want]; !ok {
			t.Errorf("probe %q was not registered", want)
		}
	}
}

// TestModulesAreMountedUnderTheAPIVersion 钉住业务路由带版本前缀。
func TestModulesAreMountedUnderTheAPIVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine, err := New("test", Options{
		RegisterModules: func(v1 *gin.RouterGroup) {
			v1.GET("/thing", func(c *gin.Context) { c.Status(http.StatusOK) })
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/thing", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("module route under /api/v1 returned %d", rec.Code)
	}
}

// TestTrustedProxiesMustBeExplicit 钉住默认不信任任何代理。
//
// Gin 不设置时的默认是**信任所有代理**，那会让任何客户端都能用转发头
// 伪造自己的 IP——而限流与审计都按 IP 走。
func TestTrustedProxiesMustBeExplicit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotIP string
	engine, err := New("test", Options{
		RegisterModules: func(v1 *gin.RouterGroup) {
			v1.GET("/ip", func(c *gin.Context) {
				gotIP = c.ClientIP()
				c.Status(http.StatusOK)
			})
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ip", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.99")
	engine.ServeHTTP(httptest.NewRecorder(), req)

	if gotIP == "203.0.113.99" {
		t.Fatal("a forged X-Forwarded-For was trusted with no proxy configured: " +
			"any client could then impersonate any IP for rate limiting and auditing")
	}
}

// middlewareNames 取出 engine 上全局中间件的函数名。
func middlewareNames(engine *gin.Engine) []string {
	handlers := engine.Handlers
	names := make([]string, 0, len(handlers))
	for _, h := range handlers {
		// 形如 ".../middleware.GinLogger.func1"，取中间那段。
		full := runtime.FuncForPC(reflect.ValueOf(h).Pointer()).Name()
		parts := strings.Split(full, ".")
		for i, p := range parts {
			if strings.HasPrefix(p, "func") && i > 0 {
				names = append(names, parts[i-1])
				break
			}
		}
	}
	return names
}
