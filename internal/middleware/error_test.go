package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	resp "github.com/gtkit/gin-api/internal/pkg/response"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/goerr"
)

func TestErrorHandlerUsesSharedEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(ErrorHandler())
	engine.GET("/boom", func(c *gin.Context) {
		c.Error(errors.New("boom")).Type = gin.ErrorTypePrivate
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
		c.Error(goerr.New(errors.New("name is required"), goerr.StatusParams())).Type = gin.ErrorTypePrivate
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
		c.Error(errors.New("boom")).Type = gin.ErrorTypePrivate
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

// TestErrorHandlerFinalizesMapErrContextErrors 钉住 handler 经 resp.MapErr
// 上抛的未映射 ctx 错误最终由本中间件定局：超时 504、断开不回包。
//
// 这是回归测试：MapErr 曾把它们直接写成 500，本中间件看到"响应已写出"
// 只能记日志，两条语义全部失效。
func TestErrorHandlerFinalizesMapErrContextErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("deadline exceeded becomes 504", func(t *testing.T) {
		engine := gin.New()
		engine.Use(ErrorHandler())
		engine.GET("/slow", func(c *gin.Context) {
			resp.MapErr(c, fmt.Errorf("load profile: %w", context.DeadlineExceeded), resp.NewErrMapper())
		})

		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/slow", nil))

		if recorder.Code != http.StatusGatewayTimeout {
			t.Fatalf("status = %d, want 504", recorder.Code)
		}
	})

	t.Run("client cancellation writes nothing", func(t *testing.T) {
		engine := gin.New()
		engine.Use(ErrorHandler())
		engine.GET("/gone", func(c *gin.Context) {
			resp.MapErr(c, fmt.Errorf("stream: %w", context.Canceled), resp.NewErrMapper())
		})

		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/gone", nil))

		if recorder.Body.Len() != 0 {
			t.Fatalf("a canceled request got a response body: %s", recorder.Body.String())
		}
	})
}
