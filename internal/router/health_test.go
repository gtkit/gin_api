package router

import (
	"context"
	"encoding/json"
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

// newTestEngine 走生产 router.New 的装配路径，包含全局中间件与系统探针。
func newTestEngine(t *testing.T, checker HealthChecker) *gin.Engine {
	return newTestEngineWith(t, Options{HealthChecker: checker})
}

func newTestEngineWith(t *testing.T, opts Options) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine, err := newRouterForTest(t, "test", opts)
	if err != nil {
		panic(err)
	}
	return engine
}

// stubStatus 让用例直接控制进程相位与能力状态。
type stubStatus struct {
	draining bool
	degraded []string
	failed   []string
}

func (s *stubStatus) Draining() bool                 { return s.draining }
func (s *stubStatus) DegradedCapabilities() []string { return s.degraded }
func (s *stubStatus) FailedRequirements() []string   { return s.failed }

func TestHealthEndpointReturns200(t *testing.T) {
	engine := newTestEngine(t, &stubChecker{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("/health status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestReadyEndpointAllHealthy(t *testing.T) {
	engine := newTestEngine(t, &stubChecker{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("/ready status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestReadyEndpointDBDown(t *testing.T) {
	engine := newTestEngine(t, &stubChecker{dbErr: errors.New("connection refused")})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("/ready status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

// TestReadyEndpointRedisFollowsRequiredness 钉住四态模型里最容易被写反的一条：
// 可选依赖故障是 **200 + degraded**，不是 503。
//
// 把可选依赖的故障映射成 503，等于让一台"只是少了一项能力"的实例被整体摘流；
// 反过来把必需依赖的故障映射成 200，则让一台真的不能干活的实例继续接流量。
// 判据只能来自"这个依赖对本进程是否必需"，而那与资源装配用的是同一份推导结果。
func TestReadyEndpointRedisFollowsRequiredness(t *testing.T) {
	cases := []struct {
		name          string
		redisRequired bool
		wantStatus    int
		wantBody      string
	}{
		{name: "optional redis down is degraded", redisRequired: false, wantStatus: http.StatusOK, wantBody: statusDegraded},
		{name: "required redis down is unavailable", redisRequired: true, wantStatus: http.StatusServiceUnavailable, wantBody: statusUnavailable},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := newTestEngineWith(t, Options{
				HealthChecker: &stubChecker{redisErr: errors.New("timeout")},
				RedisRequired: tc.redisRequired,
			})

			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))

			if w.Code != tc.wantStatus {
				t.Fatalf("/ready status = %d, want %d", w.Code, tc.wantStatus)
			}
			if !strings.Contains(w.Body.String(), tc.wantBody) {
				t.Fatalf("/ready body = %s, want status %q", w.Body.String(), tc.wantBody)
			}
		})
	}
}

// TestHealthStays200WhileDraining 钉住 liveness 与 readiness 的语义分离。
//
// draining 期间 /health 必须仍是 200：否则编排器会在优雅关停中途判定进程已死
// 并 kill 它，在飞请求被直接截断——优雅关停的全部工作白做。
// 同时 /ready 必须是 503，让 LB 立刻停止转发新请求。
func TestHealthStays200WhileDraining(t *testing.T) {
	engine := newTestEngineWith(t, Options{
		HealthChecker: &stubChecker{},
		ProcessStatus: &stubStatus{draining: true},
	})

	wHealth := httptest.NewRecorder()
	engine.ServeHTTP(wHealth, httptest.NewRequest(http.MethodGet, "/health", nil))
	if wHealth.Code != http.StatusOK {
		t.Fatalf("/health status while draining = %d, want 200", wHealth.Code)
	}

	wReady := httptest.NewRecorder()
	engine.ServeHTTP(wReady, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if wReady.Code != http.StatusServiceUnavailable {
		t.Fatalf("/ready status while draining = %d, want 503", wReady.Code)
	}
	if !strings.Contains(wReady.Body.String(), "draining") {
		t.Fatalf("/ready body = %s, want it to name the draining phase", wReady.Body.String())
	}
}

// TestReadyReflectsProcessCapabilities 验证 readiness 纳入进程自身状态，
// 而不只是依赖存活：required 能力失效 → 503；仅 optional 降级 → 200 degraded。
func TestReadyReflectsProcessCapabilities(t *testing.T) {
	cases := []struct {
		name       string
		status     *stubStatus
		wantStatus int
		wantBody   string
	}{
		{
			name:       "failed required capability is unavailable",
			status:     &stubStatus{failed: []string{"outbox"}},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "outbox",
		},
		{
			name:       "degraded optional capability stays servable",
			status:     &stubStatus{degraded: []string{"metrics-pusher"}},
			wantStatus: http.StatusOK,
			wantBody:   "metrics-pusher",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := newTestEngineWith(t, Options{
				HealthChecker: &stubChecker{},
				ProcessStatus: tc.status,
			})

			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))

			if w.Code != tc.wantStatus {
				t.Fatalf("/ready status = %d, want %d (body %s)", w.Code, tc.wantStatus, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.wantBody) {
				t.Fatalf("/ready body = %s, want it to name %q", w.Body.String(), tc.wantBody)
			}
		})
	}
}

func TestReadyEndpointNilChecker(t *testing.T) {
	engine := newTestEngine(t, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	engine.ServeHTTP(w, req)

	// fail-closed：未配置 checker 应报未就绪（503），而非 fail-open 的 200。
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("/ready status = %d, want %d (nil checker must fail closed)", w.Code, http.StatusServiceUnavailable)
	}
}

// TestReadyEndpointHidesErrorDetail 钉住公开文案与内部原因的隔离。
func TestReadyEndpointHidesErrorDetail(t *testing.T) {
	const depErr = "dial tcp 10.0.0.5:3306: connection refused"
	engine := newTestEngine(t, &stubChecker{dbErr: errors.New(depErr)})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("/ready status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}

	var body ReadinessResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("/ready body is not parseable: %v", err)
	}
	// 状态标签仍是固定取值，判定不依赖错误文本的措辞。
	if got := body.Checks["database"].Status; got != statusUnavailable {
		t.Fatalf("database status = %q, want %q", got, statusUnavailable)
	}
	if got := body.Checks["database"].Error; got != "dependency check failed" {
		t.Fatalf("database error = %q, want safe failure message", got)
	}
	if strings.Contains(w.Body.String(), depErr) {
		t.Fatal("readiness leaked the underlying reason")
	}
}

func TestReadyEndpointHidesRedisErrorDetail(t *testing.T) {
	const private = "10.0.3.12 password=secret-user SELECT token FROM private_table /srv/private"
	for _, required := range []bool{false, true} {
		engine := newTestEngineWith(t, Options{
			HealthChecker: &stubChecker{redisErr: errors.New(strings.Repeat(private, 20))},
			RedisRequired: required,
		})
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
		wantCode, wantStatus := http.StatusOK, statusDegraded
		if required {
			wantCode, wantStatus = http.StatusServiceUnavailable, statusUnavailable
		}
		var body ReadinessResponse
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != wantCode || body.Checks["redis"].Status != wantStatus || body.Checks["redis"].Error != "dependency check failed" {
			t.Fatalf("required=%v status=%d body=%+v", required, w.Code, body)
		}
		if strings.Contains(w.Body.String(), private) {
			t.Fatal("readiness leaked private data")
		}
	}
}

// TestReadyEndpointOmitsErrorWhenHealthy 钉住健康依赖不带 error 字段。
//
// 回空串会让消费者需要区分"没有错误"与"错误是空的"，而后者本就不该出现。
func TestReadyEndpointOmitsErrorWhenHealthy(t *testing.T) {
	engine := newTestEngine(t, &stubChecker{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("/ready status = %d, want %d", w.Code, http.StatusOK)
	}
	if strings.Contains(w.Body.String(), `"error"`) {
		t.Fatalf("/ready body carries an error field while healthy: %q", w.Body.String())
	}
}
