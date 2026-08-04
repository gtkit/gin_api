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
	engine.Use(Cors(allowedOrigins, nil))
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
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Fatal("expected Allow-Methods header on preflight")
	}
}
