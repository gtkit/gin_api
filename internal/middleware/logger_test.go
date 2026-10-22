package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func newLoggerEngine(t *testing.T, opts AccessLogOptions) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(GinLogger(opts))
	// echo handler：读取请求体并原样写回，用于验证 body 透传未被中间件破坏。
	echo := func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			t.Errorf("read echo body: %v", err)
			return
		}
		c.Data(http.StatusOK, "application/json", body)
	}
	engine.POST("/echo", echo)
	engine.PUT("/echo", echo)
	engine.PATCH("/echo", echo)
	engine.DELETE("/echo", echo)
	engine.GET("/echo", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	engine.GET("/asset/app.js", func(c *gin.Context) { c.String(http.StatusOK, "static") })
	return engine
}

func TestGinLoggerPassesThroughBody(t *testing.T) {
	tests := []struct {
		name string
		opts AccessLogOptions
	}{
		{name: "default off", opts: AccessLogOptions{}},
		{name: "request body on", opts: AccessLogOptions{LogRequestBody: true}},
		{name: "response body on", opts: AccessLogOptions{LogResponseBody: true}},
		{name: "both on", opts: AccessLogOptions{LogRequestBody: true, LogResponseBody: true}},
	}

	const payload = `{"password":"secretpw","name":"bob"}`

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := newLoggerEngine(t, tt.opts)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/echo?token=abc123", strings.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			engine.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			// 无论 body 日志开关如何，下游 handler 必须收到完整原始请求体。
			if got := w.Body.String(); got != payload {
				t.Fatalf("echoed body = %q, want %q (body was corrupted by logger)", got, payload)
			}
		})
	}
}

func TestGinLoggerWriteMethods(t *testing.T) {
	engine := newLoggerEngine(t, AccessLogOptions{LogRequestBody: true, LogResponseBody: true})

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			const payload = `{"pwd":"x","v":1}`
			w := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/echo", strings.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			engine.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("%s status = %d, want 200", method, w.Code)
			}
			if got := w.Body.String(); got != payload {
				t.Fatalf("%s echoed body = %q, want %q", method, got, payload)
			}
		})
	}
}

func TestGinLoggerGetAndStaticSkip(t *testing.T) {
	engine := newLoggerEngine(t, AccessLogOptions{LogRequestBody: true, LogResponseBody: true})

	t.Run("GET is handled without body wrapping", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/echo", nil)
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK || w.Body.String() != "ok" {
			t.Fatalf("GET /echo = %d %q, want 200 \"ok\"", w.Code, w.Body.String())
		}
	})

	t.Run("static asset short-circuits", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/asset/app.js", nil)
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK || w.Body.String() != "static" {
			t.Fatalf("GET static = %d %q, want 200 \"static\"", w.Code, w.Body.String())
		}
	})
}
