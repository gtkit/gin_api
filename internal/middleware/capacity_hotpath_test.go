package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
	"github.com/gtkit/goerr"
)

func capacityTestEngine(t testing.TB) (*gin.Engine, func() []map[string]any, func()) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	access, read := newNativeTestLogger(t, "info")
	engine := gin.New()
	engine.Use(GinLogger(access, AccessLogOptions{}))
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	engine.GET("/capacity", Admission(1, nil), func(c *gin.Context) { close(entered); <-release; c.Status(http.StatusNoContent) })
	engine.GET("/failure", func(c *gin.Context) { resp.Err(c, goerr.New(nil, goerr.StatusServiceUnavailable())) })
	go func() {
		defer close(done)
		engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/capacity", nil))
	}()
	<-entered
	stop := sync.OnceFunc(func() { close(release); <-done })
	t.Cleanup(stop)
	return engine, read, stop
}

func TestCapacityRejectionDiagnosticIsBounded(t *testing.T) {
	engine, read, stop := capacityTestEngine(t)
	var workers sync.WaitGroup
	for range 64 {
		workers.Go(func() {
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/capacity", nil))
			var body struct {
				Code    int    `json:"code"`
				Message string `json:"msg"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Error(err)
				return
			}
			if response.Code != 503 || body.Code != 10010018 || body.Message != "服务繁忙，请稍后重试" {
				t.Errorf("capacity response: %d %+v", response.Code, body)
			}
		})
	}
	workers.Wait()
	stop()
	var capacity int
	for _, entry := range read() {
		if entry["status"] == float64(503) || entry["http_status"] == float64(503) {
			capacity++
			if entry["level"] != "WARN" || entry["stacktrace"] != nil {
				t.Errorf("capacity stack or level: %v", entry)
			}
		}
	}
	if capacity != 1 {
		t.Fatalf("capacity diagnostics = %d, want 1", capacity)
	}
	before := len(read())
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/failure", nil))
	if len(read()) != before+2 {
		t.Fatal("ordinary 503 diagnostics were suppressed")
	}
}

func BenchmarkCapacityRejectionParallel(b *testing.B) {
	engine, _, stop := capacityTestEngine(b)
	defer stop()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/capacity", nil))
			if response.Code != http.StatusServiceUnavailable {
				b.Error(response.Code)
			}
		}
	})
}

func TestCapacityLogWindowCountsConcurrentSuppression(t *testing.T) {
	t.Parallel()
	var limiter capacityLogLimiter
	var logged atomic.Int64
	var workers sync.WaitGroup
	now := time.Unix(1_000_000_000, 0)
	for range 64 {
		workers.Go(func() {
			if allowed, _ := limiter.allow(now); allowed {
				logged.Add(1)
			}
		})
	}
	workers.Wait()
	if logged.Load() != 1 {
		t.Fatalf("logged = %d, want 1", logged.Load())
	}
	if allowed, count := limiter.allow(now.Add(capacityLogInterval)); !allowed || count != 63 {
		t.Fatalf("next window = %v, suppressed = %d", allowed, count)
	}
}
