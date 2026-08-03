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

// newTestEngine 走与生产完全相同的装配路径：系统探针现在是 RouteSpec 的一部分，
// 不再由旁路注册产生，因此测试也必须经 registrar 拿到 engine。
func newTestEngine(checker HealthChecker) *gin.Engine {
	return newTestEngineWith(RouterOptions{HealthChecker: checker})
}

func newTestEngineWith(opts RouterOptions) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine, err := InitRouterWithVersionedModules("test", VersionedModules{}, opts)
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
			engine := newTestEngineWith(RouterOptions{
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
	engine := newTestEngineWith(RouterOptions{
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
			engine := newTestEngineWith(RouterOptions{
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
