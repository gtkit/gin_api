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
			instance, records := newNativeTestLogger(t, "info")
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
			instance.Sync()
			logs := records()
			if len(logs) != 1 {
				t.Fatalf("recovery records = %d, want 1", len(logs))
			}
			stack, _ := logs[0]["stacktrace"].(string)
			if (stack != "") != tt.includeStack || strings.Contains(stack, "boom") {
				t.Fatalf("unexpected stack for includeStack=%v: %q", tt.includeStack, stack)
			}
		})
	}
}

func TestRecoveryNativeRequestContext(t *testing.T) {
	for _, tc := range []struct {
		name   string
		code   int32
		level  string
		status int
		panic  func(*gin.Context)
	}{
		{"panic", recoveryLogCodePanicRecovered, "ERROR", http.StatusInternalServerError, func(*gin.Context) { panic("private panic") }},
		{"started", recoveryLogCodeResponseStarted, "ERROR", http.StatusAccepted, func(c *gin.Context) {
			c.String(http.StatusAccepted, "partial")
			panic("private panic")
		}},
		{"disconnected", recoveryLogCodeClientClosedConn, "WARN", http.StatusOK, func(*gin.Context) { panic(fmt.Errorf("private panic: %w", http.ErrAbortHandler)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			instance, records := newNativeTestLogger(t, "info")
			gen, err := NewRequestIDGenerator()
			if err != nil {
				t.Fatal(err)
			}
			engine := gin.New()
			engine.Use(GinRecovery(false), RequestID(gen))
			entered := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
			release := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
			done := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
			engine.GET("/panic/:id", func(c *gin.Context) {
				slot := 0
				if c.Query("slot") == "second" {
					slot = 1
				}
				close(entered[slot])
				<-release[slot]
				tc.panic(c)
			})
			responses := [2]*httptest.ResponseRecorder{httptest.NewRecorder(), httptest.NewRecorder()}
			for i, slot := range []string{"first", "second"} {
				request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/panic/private-path?auth=private-query&slot="+slot, nil)
				request.Header.Set(RequestIDHeader, "forged-request-id")
				request.Header.Set("X-Arbitrary", "private-header")
				go func() {
					engine.ServeHTTP(responses[i], request)
					close(done[i])
				}()
			}
			// Both request contexts exist before either panic is handled. Finish
			// the second request first to expose a shared or last-request ID.
			<-entered[0]
			<-entered[1]
			close(release[1])
			<-done[1]
			close(release[0])
			<-done[0]
			var ids []string
			for _, response := range responses {
				id := response.Header().Get(RequestIDHeader)
				if id == "" || id == "forged-request-id" || response.Code != tc.status {
					t.Fatalf("id=%q status=%d", id, response.Code)
				}
				ids = append(ids, id)
			}
			instance.Sync()
			logs := records()
			if len(logs) != 2 || ids[0] == ids[1] {
				t.Fatalf("logs=%v ids=%v", logs, ids)
			}
			for i, record := range logs {
				if record["request_id"] != ids[1-i] || record["level"] != tc.level || record["recovery_code"] != float64(tc.code) {
					t.Fatalf("unexpected recovery record: %v", record)
				}
				if record["method"] != http.MethodGet || record["route"] != "/panic/:id" || record["response_started"] != (tc.name == "started") {
					t.Fatalf("unexpected recovery fields: %v", record)
				}
				for _, marker := range []string{"private-path", "private-query", "private-header", "private panic", "forged-request-id"} {
					if strings.Contains(fmt.Sprint(record), marker) || strings.Contains(responses[1-i].Body.String(), marker) {
						t.Errorf("private marker reached logs or response: %q", marker)
					}
				}
			}
		})
	}
}

func TestRecoveryWithoutRequestContext(t *testing.T) {
	instance, records := newNativeTestLogger(t, "info")
	engine := gin.New()
	engine.Use(GinRecovery(false))
	engine.GET("/panic", func(c *gin.Context) {
		c.Request = nil
		panic(http.ErrAbortHandler)
	})
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/panic", nil))
	instance.Sync()
	logs := records()
	if len(logs) != 1 || logs[0]["recovery_code"] != float64(recoveryLogCodeClientClosedConn) {
		t.Fatalf("missing recovery record: %v", logs)
	}
	if id := logs[0]["request_id"]; id != nil && id != "" {
		t.Fatalf("requestless panic received fabricated id: %v", id)
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
