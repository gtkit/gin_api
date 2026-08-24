package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func newCorsTestEngine(allowedOrigins []string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	// 与生产装配同款：预检的可用方法取自 gin 在 405 路径上写的 Allow 头，
	// 该标志关闭时 gin 不产生 405、也不写 Allow，预检就无从判断路径是否存在。
	// 生产侧由 engine_baseline_test 的字段清单断言它为 true。
	engine.HandleMethodNotAllowed = true
	engine.Use(Cors(allowedOrigins))
	engine.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	return engine
}

func TestCors_AllowedOrigin(t *testing.T) {
	engine := newCorsTestEngine([]string{"https://app.example.com", "https://www.example.com"})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	got := rec.Header().Get("Access-Control-Allow-Origin")
	if got != "https://app.example.com" {
		t.Fatalf("Allow-Origin = %q, want %q", got, "https://app.example.com")
	}
	if rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatal("expected Allow-Credentials: true for allowlisted origin")
	}
}

func TestCors_DisallowedOrigin(t *testing.T) {
	engine := newCorsTestEngine([]string{"https://app.example.com"})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "https://evil.com")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Allow-Origin = %q, want empty for disallowed origin", got)
	}
}

func TestCors_DisallowedOrigin_Preflight(t *testing.T) {
	engine := newCorsTestEngine([]string{"https://app.example.com"})

	req := httptest.NewRequest(http.MethodOptions, "/test", nil)
	req.Header.Set("Origin", "https://evil.com")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d for disallowed preflight", rec.Code, http.StatusForbidden)
	}
}

func TestCors_Wildcard(t *testing.T) {
	engine := newCorsTestEngine([]string{"*"})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "https://anything.com")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Allow-Origin = %q, want %q", got, "*")
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("Allow-Credentials = %q, want empty for wildcard mode", got)
	}
}

func TestCors_WildcardPreflight(t *testing.T) {
	engine := newCorsTestEngine([]string{"*"})

	req := httptest.NewRequest(http.MethodOptions, "/test", nil)
	req.Header.Set("Origin", "https://anything.com")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

func TestCors_NoOriginHeader(t *testing.T) {
	engine := newCorsTestEngine([]string{"https://app.example.com"})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	// No Origin header.
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Allow-Origin = %q, want empty for no-origin request", got)
	}
}

func TestCors_CaseInsensitiveMatch(t *testing.T) {
	engine := newCorsTestEngine([]string{"https://APP.Example.COM"})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("Allow-Origin = %q, want case-insensitive match", got)
	}
}

func TestCors_PreflightAllowed(t *testing.T) {
	engine := newCorsTestEngine([]string{"https://app.example.com"})

	req := httptest.NewRequest(http.MethodOptions, "/test", nil)
	req.Header.Set("Origin", "https://app.example.com")
	// 预检的定义就是带 Access-Control-Request-Method 的 OPTIONS。
	//
	// 这里此前没有设置它——于是这个名叫 Preflight 的测试发的并不是预检请求，
	// 而它断言的 Access-Control-Allow-Methods 之所以还在，是因为旧实现把该头
	// 无条件写在每个跨源响应上。该头按规范只在预检响应里被消费，收进预检分支
	// 之后，这个测试才真正测到它声称要测的东西。
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Fatal("expected Allow-Methods header on preflight")
	}
}
