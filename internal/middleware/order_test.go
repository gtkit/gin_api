package middleware

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
//   - AccessLogger 排在 CORS/MaxBytes 之后 → 403/413 的响应没有访问记录，
//     而那正是最需要留痕的两类；
//   - SecurityHeaders 排在任何写 body 的中间件之后 → header 设置**静默失效**
//     （body 一开始写响应头就已发出），线上看不到任何报错；
//   - MaxBytesLimit 不在最后 → body 日志的读取不受同一上限约束。
//
// 因此这里用精确序列断言，而不是"包含某几个"。
var wantMiddlewareOrder = []string{
	"GinRecovery",
	"RequestID",
	"GinLogger",
	"SecurityHeaders",
	"ErrorHandler",
	"Cors",
	"MaxBytesLimit",
}

// handlerNames 取出 engine 上全局中间件的函数名（去掉包路径与闭包后缀）。
func handlerNames(handlers gin.HandlersChain) []string {
	names := make([]string, 0, len(handlers))
	for _, h := range handlers {
		full := runtime.FuncForPC(reflect.ValueOf(h).Pointer()).Name()
		// 形如 ".../middleware.GinLogger.func1"，取中间那段。
		full = full[strings.LastIndex(full, "/")+1:]
		parts := strings.Split(full, ".")
		for i := len(parts) - 1; i >= 0; i-- {
			if !strings.HasPrefix(parts[i], "func") {
				names = append(names, parts[i])
				break
			}
		}
	}
	return names
}

// TestInitMiddlewareOrderIsExact 精确断言全局中间件顺序。
func TestInitMiddlewareOrderIsExact(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	if err := InitMiddleware(engine, []string{"https://example.com"}, AccessLogOptions{}, 1024, nil); err != nil {
		t.Fatalf("InitMiddleware: %v", err)
	}

	got := handlerNames(engine.Handlers)
	if len(got) != len(wantMiddlewareOrder) {
		t.Fatalf("middleware chain = %v, want exactly %v", got, wantMiddlewareOrder)
	}
	for i := range got {
		if got[i] != wantMiddlewareOrder[i] {
			t.Fatalf("middleware[%d] = %q, want %q (full chain %v)", i, got[i], wantMiddlewareOrder[i], got)
		}
	}
}

// TestInitMiddlewareWithoutCORS 验证未配置 allowed origins 时不挂 CORS，
// 其余顺序不变——CORS 是唯一条件注册的一项，它的缺席不该扰动别的位置。
func TestInitMiddlewareWithoutCORS(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	if err := InitMiddleware(engine, nil, AccessLogOptions{}, 1024, nil); err != nil {
		t.Fatalf("InitMiddleware: %v", err)
	}

	got := handlerNames(engine.Handlers)
	want := make([]string, 0, len(wantMiddlewareOrder)-1)
	for _, name := range wantMiddlewareOrder {
		if name != "Cors" {
			want = append(want, name)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("middleware chain = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("middleware[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestSecurityHeadersPresentOnEveryResponse 钉住安全头覆盖各类响应。
//
// 只测 200 是不够的：错误响应同样会被浏览器渲染，而 404/413 这类响应
// 恰恰更容易被忽略——它们由中间件而不是业务 handler 产生。
func TestSecurityHeadersPresentOnEveryResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	if err := InitMiddleware(engine, nil, AccessLogOptions{}, 8, nil); err != nil {
		t.Fatalf("InitMiddleware: %v", err)
	}
	engine.GET("/ok", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	engine.POST("/big", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	cases := []struct {
		name string
		req  *http.Request
	}{
		{name: "success", req: httptest.NewRequest(http.MethodGet, "/ok", nil)},
		{name: "not found", req: httptest.NewRequest(http.MethodGet, "/missing", nil)},
		{
			name: "payload too large",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, "/big", strings.NewReader(strings.Repeat("x", 64)))
				r.Header.Set("Content-Type", "application/json")
				return r
			}(),
		},
	}

	wantHeaders := map[string]string{
		"X-Content-Type-Options":  headerContentTypeOptions,
		"X-Frame-Options":         headerFrameOptions,
		"Referrer-Policy":         headerReferrerPolicy,
		"Content-Security-Policy": headerCSP,
		"Permissions-Policy":      headerPermissionsPolicy,
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, tc.req)

			for name, want := range wantHeaders {
				if got := w.Header().Get(name); got != want {
					t.Errorf("%s = %q, want %q (status %d)", name, got, want, w.Code)
				}
			}
			// 每个响应都必须带内部请求 ID，否则客户端报障时无从定位。
			if id := w.Header().Get(RequestIDHeader); id == "" {
				t.Errorf("response has no %s header (status %d)", RequestIDHeader, w.Code)
			}
		})
	}
}

// TestSecurityHeadersDoNotIncludeHSTS 反向断言：不在明文后端发 HSTS。
//
// HSTS 由 TLS 终止层负责。后端擅自发送要么无效，要么在某些拓扑下
// 把一个错误的策略钉进浏览器一年，而那是无法远程撤销的。
func TestSecurityHeadersDoNotIncludeHSTS(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(SecurityHeaders())
	engine.GET("/ok", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ok", nil))

	if got := w.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("Strict-Transport-Security = %q, want it absent from a plaintext backend", got)
	}
}
