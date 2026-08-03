package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestInitMiddlewareEnforcesMaxBody 验证请求体大小上限确实被注册进全局中间件链：
// 超过上限的请求经完整链路后返回 413，而不是被 handler 正常处理。
func TestInitMiddlewareEnforcesMaxBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	if err := InitMiddleware(engine, nil, AccessLogOptions{}, 16); err != nil { // 16 字节上限
		t.Fatalf("InitMiddleware: %v", err)
	}

	engine.POST("/echo", func(c *gin.Context) {
		_, _ = io.ReadAll(c.Request.Body)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(strings.Repeat("x", 1024)))
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d (max body limit not enforced in chain)", w.Code, http.StatusRequestEntityTooLarge)
	}
}

// TestInitMiddlewareAllowsWithinLimit 验证未超限请求正常放行。
func TestInitMiddlewareAllowsWithinLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	if err := InitMiddleware(engine, nil, AccessLogOptions{}, 1024); err != nil {
		t.Fatalf("InitMiddleware: %v", err)
	}

	engine.POST("/echo", func(c *gin.Context) {
		_, _ = io.ReadAll(c.Request.Body)
		c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader("small body"))
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for within-limit request", w.Code)
	}
}
