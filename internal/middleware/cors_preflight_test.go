package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// corsEngine 挂一条 CORS 中间件，并注册真实路由。
//
// 不再注入"路径 → 方法"表：预检的可用方法现在取自 gin 已写好的 Allow 头，
// 而那要求 engine 真的注册过路由且开启 HandleMethodNotAllowed。用假表会让
// 测试绕开真实机制——本次的缺陷（带参路由被误判为路径不存在）恰恰是假表
// 掩盖不了、真路由才暴露得出来的那一类。
func corsEngine(t *testing.T, origins []string) *gin.Engine {
	t.Helper()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	// 与生产装配同款：gin 据此在 405 路径上写 Allow。
	engine.HandleMethodNotAllowed = true
	engine.Use(Cors(origins))
	engine.Handle(http.MethodGet, "/api/v1/thing", func(c *gin.Context) { c.Status(http.StatusOK) })
	return engine
}

// TestCorsAlwaysDeclaresVaryOrigin 钉住 Vary 无条件出现。
//
// 响应内容依 Origin 而变，而共享缓存按 URL 缓存。不声明 Vary 时，CDN 会把
// A 源拿到的响应原样发给 B 源——那既是跨源数据泄漏，也会让 B 源莫名被拒。
//
// 每条分支都要覆盖：中间件里每个分支都会 return，逐条补写必然漏掉其中一条。
func TestCorsAlwaysDeclaresVaryOrigin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		origin string
	}{
		{name: "no origin at all", method: http.MethodGet},
		{name: "an allowed origin", method: http.MethodGet, origin: "https://example.com"},
		{name: "a rejected origin", method: http.MethodGet, origin: "https://evil.test"},
		{name: "a rejected preflight", method: http.MethodOptions, origin: "https://evil.test"},
		{name: "an allowed preflight", method: http.MethodOptions, origin: "https://example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(tt.method, "/api/v1/thing", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.method == http.MethodOptions {
				req.Header.Set("Access-Control-Request-Method", "GET")
			}

			rec := httptest.NewRecorder()
			corsEngine(t, []string{"https://example.com"}).ServeHTTP(rec, req)

			vary := rec.Header().Values("Vary")
			var hasOrigin bool
			for _, v := range vary {
				if strings.Contains(v, "Origin") {
					hasOrigin = true
				}
			}
			if !hasOrigin {
				t.Fatalf("Vary = %v, want it to include Origin (status %d)", vary, rec.Code)
			}
		})
	}
}

// TestPreflightValidatesTheTargetRoute 钉住预检不再无条件 204。
//
// 无条件 204 让预检成功而实际请求失败：浏览器据此放行，随后拿到 404 或 405，
// 而开发者看到的现象是"CORS 明明通过了却还是失败"——排查方向被引向错误的地方。
func TestPreflightValidatesTheTargetRoute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		path           string
		requestMethod  string
		requestHeaders string
		wantStatus     int
	}{
		{
			name: "an allowed method on an existing route",
			path: "/api/v1/thing", requestMethod: "GET", wantStatus: http.StatusNoContent,
		},
		{
			// 路径不存在时不该假称它可用。
			name: "an unknown path",
			path: "/api/v1/nope", requestMethod: "GET", wantStatus: http.StatusNotFound,
		},
		{
			// 方法不被支持时预检必须失败，并给出 Allow。
			name: "a method the route does not support",
			path: "/api/v1/thing", requestMethod: "DELETE", wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name: "an allowed request header",
			path: "/api/v1/thing", requestMethod: "GET",
			requestHeaders: "Content-Type, App-Token", wantStatus: http.StatusNoContent,
		},
		{
			// 放行一个服务端不接受的头，只会把失败推迟到实际请求。
			name: "a request header outside the allow list",
			path: "/api/v1/thing", requestMethod: "GET",
			requestHeaders: "X-Not-Allowed", wantStatus: http.StatusForbidden,
		},
		{
			// 没有声明目标方法的 OPTIONS 不是预检，无从校验。
			name: "an options request that is not a preflight",
			path: "/api/v1/thing", wantStatus: http.StatusNoContent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodOptions, tt.path, nil)
			req.Header.Set("Origin", "https://example.com")
			if tt.requestMethod != "" {
				req.Header.Set("Access-Control-Request-Method", tt.requestMethod)
			}
			if tt.requestHeaders != "" {
				req.Header.Set("Access-Control-Request-Headers", tt.requestHeaders)
			}

			rec := httptest.NewRecorder()
			corsEngine(t, []string{"https://example.com"}).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusMethodNotAllowed && rec.Header().Get("Allow") == "" {
				t.Fatal("a 405 preflight must carry Allow so the caller knows what is supported")
			}
		})
	}
}

// TestPreflightAllowHeadersMatchesWhatIsAccepted 钉住声明与校验同源。
//
// 两处各写一份的话，预检放行的头与响应声明的头会分叉，而分叉的方向通常是
// 预检更宽——于是浏览器放行了一个服务端并不接受的头。
func TestPreflightAllowHeadersMatchesWhatIsAccepted(t *testing.T) {
	t.Parallel()

	for header := range strings.SplitSeq(allowHeadersValue, ",") {
		name := strings.TrimSpace(header)
		if name == "" {
			continue
		}

		req := httptest.NewRequest(http.MethodOptions, "/api/v1/thing", nil)
		req.Header.Set("Origin", "https://example.com")
		req.Header.Set("Access-Control-Request-Method", "GET")
		req.Header.Set("Access-Control-Request-Headers", name)

		rec := httptest.NewRecorder()
		corsEngine(t, []string{"https://example.com"}).ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Fatalf("header %q is advertised in Access-Control-Allow-Headers but the preflight "+
				"rejects it (status %d)", name, rec.Code)
		}
	}
}
