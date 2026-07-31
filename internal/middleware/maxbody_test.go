package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	resp "github.com/gtkit/gin-api/internal/pkg/response"
)

// newMaxBytesTestEngine 组装与生产一致的相关中间件链（ErrorHandler → MaxBytesLimit → GinLogger）。
func newMaxBytesTestEngine(maxBytes int64, accessLog AccessLogOptions, handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(ErrorHandler())
	engine.Use(MaxBytesLimit(MaxBytesLimitConfig{MaxBytes: maxBytes, ErrorMessage: "请求体过大"}))
	engine.Use(GinLogger(accessLog))
	engine.POST("/upload", handler)
	return engine
}

func TestMaxBytesLimitRejectsDeclaredLengthBeforeHandler(t *testing.T) {
	handlerCalled := false
	engine := newMaxBytesTestEngine(4, AccessLogOptions{}, func(c *gin.Context) {
		handlerCalled = true
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("12345"))
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
	if handlerCalled {
		t.Fatal("handler executed, want rejected before handler")
	}

	body := recorder.Body.String()
	if !strings.Contains(body, "\"code\":10010019") {
		t.Fatalf("expected max body error code, got %s", body)
	}
	if !strings.Contains(body, "\"msg\":\"文件太大，最大支持 4 B\"") {
		t.Fatalf("expected max body message, got %s", body)
	}
	if !strings.Contains(body, "\"max_bytes\":4") {
		t.Fatalf("expected limit metadata in response data, got %s", body)
	}
}

func TestMaxBytesLimitReturns413ForChunkedBodyViaBind(t *testing.T) {
	engine := newMaxBytesTestEngine(4, AccessLogOptions{}, func(c *gin.Context) {
		var payload struct {
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&payload); err != nil {
			resp.BindStructErr(c, err)
			return
		}
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(`{"name":"exceeds-limit"}`))
	// 模拟 chunked 传输：无声明长度，前置预检不命中，读取时由 MaxBytesReader 兜底。
	req.ContentLength = -1
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "\"code\":10010019") {
		t.Fatalf("expected max body error code, got %s", body)
	}
}

func TestMaxBytesLimitReturns413WhenRequestBodyLoggingEnabled(t *testing.T) {
	handlerCalled := false
	engine := newMaxBytesTestEngine(4, AccessLogOptions{LogRequestBody: true}, func(c *gin.Context) {
		handlerCalled = true
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("12345678"))
	req.ContentLength = -1 // 绕过声明长度预检，让 GinLogger 的 ReadAll 撞限
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
	if handlerCalled {
		t.Fatal("handler executed, want skipped after body read failure")
	}
}

func TestMaxBytesLimitAllowsBodyWithinLimit(t *testing.T) {
	engine := newMaxBytesTestEngine(1024, AccessLogOptions{}, func(c *gin.Context) {
		var payload struct {
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&payload); err != nil {
			resp.BindStructErr(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"name": payload.Name})
	})

	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(`{"name":"ok"}`))
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

func TestMaxBytesLimitKeeps422ForInvalidJSONWithinLimit(t *testing.T) {
	engine := newMaxBytesTestEngine(1024, AccessLogOptions{}, func(c *gin.Context) {
		var payload struct {
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&payload); err != nil {
			resp.BindStructErr(c, err)
			return
		}
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(`{invalid`))
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnprocessableEntity)
	}
}
