package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/goerr"
)

func TestErrorHandlerUsesSharedEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(ErrorHandler())
	engine.GET("/boom", func(c *gin.Context) {
		c.Error(errors.New("boom"))
	})

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "\"code\":10010001") {
		t.Fatalf("expected shared response code, got %s", body)
	}
	if !strings.Contains(body, "\"msg\":\"服务器内部错误\"") {
		t.Fatalf("expected sanitized internal server message, got %s", body)
	}
	if !strings.Contains(body, "\"data\":[]") {
		t.Fatalf("expected shared response data field, got %s", body)
	}
	if strings.Contains(body, "boom") {
		t.Fatalf("error response should not leak raw detail, got %s", body)
	}
}

func TestErrorHandlerUsesGoerrWhenAvailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(ErrorHandler())
	engine.GET("/params", func(c *gin.Context) {
		c.Error(goerr.New(errors.New("name is required"), goerr.StatusParams()))
	})

	req := httptest.NewRequest(http.MethodGet, "/params", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "\"code\":10010002") {
		t.Fatalf("expected params error code, got %s", body)
	}
	// clientMessage 返回 Status 的默认文案，不暴露原始 error 细节。
	if !strings.Contains(body, "\"msg\":") {
		t.Fatalf("expected msg field in response, got %s", body)
	}
}

func TestErrorHandlerDoesNotOverwriteWrittenResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(ErrorHandler())
	engine.GET("/written", func(c *gin.Context) {
		c.Error(errors.New("boom"))
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
			"code": 99999,
			"msg":  "already written",
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/written", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "\"code\":99999") {
		t.Fatalf("expected original written response to remain, got %s", body)
	}
	if strings.Contains(body, "\"code\":10010001") {
		t.Fatalf("written response should not be overwritten, got %s", body)
	}
}
