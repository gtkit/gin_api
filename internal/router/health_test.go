package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type stubChecker struct {
	dbErr    error
	redisErr error
}

func (s *stubChecker) PingDB(_ context.Context) error    { return s.dbErr }
func (s *stubChecker) PingRedis(_ context.Context) error { return s.redisErr }

func newTestEngine(checker HealthChecker) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	RegisterHealthRoutes(engine, checker)
	return engine
}

func TestHealthEndpointReturns200(t *testing.T) {
	engine := newTestEngine(&stubChecker{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("/health status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestReadyEndpointAllHealthy(t *testing.T) {
	engine := newTestEngine(&stubChecker{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("/ready status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestReadyEndpointDBDown(t *testing.T) {
	engine := newTestEngine(&stubChecker{dbErr: errors.New("connection refused")})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("/ready status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestReadyEndpointRedisDown(t *testing.T) {
	engine := newTestEngine(&stubChecker{redisErr: errors.New("timeout")})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("/ready status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestReadyEndpointNilChecker(t *testing.T) {
	engine := newTestEngine(nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	engine.ServeHTTP(w, req)

	// fail-closed：未配置 checker 应报未就绪（503），而非 fail-open 的 200。
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("/ready status = %d, want %d (nil checker must fail closed)", w.Code, http.StatusServiceUnavailable)
	}
}

func TestReadyEndpointHidesErrorDetail(t *testing.T) {
	const secretErr = "dial tcp 10.0.0.5:3306: connection refused"
	engine := newTestEngine(&stubChecker{dbErr: errors.New(secretErr)})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("/ready status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	body := w.Body.String()
	// 底层错误串绝不能出现在响应体里。
	if strings.Contains(body, secretErr) {
		t.Fatalf("/ready body leaked underlying error: %q", body)
	}
	// 应以固定标签 unavailable 呈现失败依赖。
	if !strings.Contains(body, "unavailable") {
		t.Fatalf("/ready body = %q, want fixed 'unavailable' label", body)
	}
}
