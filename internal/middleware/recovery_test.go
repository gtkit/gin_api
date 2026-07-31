package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestGinRecoveryReturnsSharedEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		includeStack bool
	}{
		{
			name:         "without stack",
			includeStack: false,
		},
		{
			name:         "with stack",
			includeStack: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := gin.New()
			engine.Use(GinRecovery(tt.includeStack))
			engine.GET("/panic", func(c *gin.Context) {
				panic("boom")
			})

			req := httptest.NewRequest(http.MethodGet, "/panic", nil)
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
				t.Fatalf("expected shared response message, got %s", body)
			}
			if !strings.Contains(body, "\"data\":[]") {
				t.Fatalf("expected shared response data field, got %s", body)
			}
			if strings.Contains(body, "boom") {
				t.Fatalf("panic detail should not leak to client, got %s", body)
			}
		})
	}
}

func TestGinRecoveryDoesNotRewriteStartedResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(GinRecovery(false))
	engine.GET("/stream", func(c *gin.Context) {
		// 先写出部分响应，模拟流式响应或 SSE 已经开始发送的场景。
		c.String(http.StatusOK, "partial-response")
		panic("boom")
	})

	req := httptest.NewRequest(http.MethodGet, "/stream", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if body := recorder.Body.String(); body != "partial-response" {
		t.Fatalf("body = %q, want %q", body, "partial-response")
	}
	if strings.Contains(recorder.Body.String(), "10010001") {
		t.Fatalf("started response should not be rewritten, got %s", recorder.Body.String())
	}
}

func TestDumpRequestWithoutSensitiveHeadersMasksSecrets(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/panic?debug=true", strings.NewReader("ignored"))
	req.Header.Set("Authorization", "Bearer super-secret-token")
	req.Header.Set("Cookie", "session=top-secret")
	req.Header.Set("Proxy-Authorization", "Basic hidden")
	req.Header.Set("X-Trace-Id", "trace-123")

	dump, err := dumpRequestWithoutSensitiveHeaders(req)
	if err != nil {
		t.Fatalf("dumpRequestWithoutSensitiveHeaders error = %v", err)
	}

	if strings.Contains(dump, "super-secret-token") || strings.Contains(dump, "top-secret") || strings.Contains(dump, "hidden") {
		t.Fatalf("expected sensitive headers to be masked, got %s", dump)
	}
	if !strings.Contains(dump, "Authorization: *") {
		t.Fatalf("expected Authorization header to be masked, got %s", dump)
	}
	if !strings.Contains(dump, "Cookie: *") {
		t.Fatalf("expected Cookie header to be masked, got %s", dump)
	}
	if !strings.Contains(dump, "Proxy-Authorization: *") {
		t.Fatalf("expected Proxy-Authorization header to be masked, got %s", dump)
	}
	if !strings.Contains(dump, "X-Trace-Id: trace-123") {
		t.Fatalf("expected non-sensitive headers to remain, got %s", dump)
	}
}

func TestDumpRequestRedactsAppTokenAndQuery(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/sse?App-Token=secret-query-token&page=2", nil)
	req.Header.Set("App-Token", "secret-header-token")

	dump, err := dumpRequestWithoutSensitiveHeaders(req)
	if err != nil {
		t.Fatalf("dumpRequestWithoutSensitiveHeaders error = %v", err)
	}

	if strings.Contains(dump, "secret-header-token") {
		t.Fatalf("App-Token header value leaked, got %s", dump)
	}
	if strings.Contains(dump, "secret-query-token") {
		t.Fatalf("App-Token query value leaked, got %s", dump)
	}
	if !strings.Contains(dump, "App-Token: *") {
		t.Fatalf("expected App-Token header to be masked, got %s", dump)
	}
	if !strings.Contains(dump, "page=2") {
		t.Fatalf("expected benign query param to be retained, got %s", dump)
	}
}

func TestIsBrokenConnectionError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "http abort handler",
			err:  http.ErrAbortHandler,
			want: true,
		},
		{
			name: "wrapped epipe",
			err:  fmt.Errorf("write failed: %w", syscall.EPIPE),
			want: true,
		},
		{
			name: "normal error",
			err:  errors.New("boom"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBrokenConnectionError(tt.err); got != tt.want {
				t.Fatalf("isBrokenConnectionError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
