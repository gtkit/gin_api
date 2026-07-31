package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestInputValidationRejectsInvalidQueryWithUnifiedEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(InputValidation())
	engine.GET("/search", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/search?q=1+UNION+SELECT", nil)
	req.Header.Set("User-Agent", "test-client")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "\"code\":10010002") {
		t.Fatalf("expected params error code, got %s", body)
	}
	if !strings.Contains(body, "无效的查询参数") {
		t.Fatalf("expected query validation message, got %s", body)
	}
}

func TestInputValidationRejectsInvalidHeadersWithUnifiedEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(InputValidation())
	engine.GET("/search", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/search", nil)
	req.Header.Set("User-Agent", "test-client")
	req.Header.Set("Referer", "://bad referer")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "\"code\":10010002") {
		t.Fatalf("expected params error code, got %s", body)
	}
	if !strings.Contains(body, "无效的请求头") {
		t.Fatalf("expected header validation message, got %s", body)
	}
}
