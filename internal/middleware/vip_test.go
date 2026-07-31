package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gtkit/gin-api/internal/pkg/ginc"

	"github.com/gin-gonic/gin"
)

type fakeVIPVerifier struct {
	valid bool
}

func (f fakeVIPVerifier) ValidateVIP(context.Context, int64) bool {
	return f.valid
}

func TestRequireVIPRejectsMissingUser(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(RequireVIP(fakeVIPVerifier{valid: true}))
	engine.GET("/vip", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/vip", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestRequireVIPRejectsNonVIPUser(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		ginc.SetUserID(c, 42)
		c.Next()
	})
	engine.Use(RequireVIP(fakeVIPVerifier{valid: false}))
	engine.GET("/vip", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/vip", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	// StatusVip 是业务错误（HTTP 200 + 业务错误码），不是 HTTP 401。
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

func TestRequireVIPAllowsVIPUser(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		ginc.SetUserID(c, 42)
		c.Next()
	})
	engine.Use(RequireVIP(fakeVIPVerifier{valid: true}))
	engine.GET("/vip", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/vip", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}
