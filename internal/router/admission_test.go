package router

import (
	"context"
	"github.com/gtkit/gin-api/internal/middleware"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/pkg/metrics"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestAdmissionKeepsCanceledHandlerSlotAndProbesAvailable(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	rec, err := metrics.New(provider.Meter("admission-test"))
	if err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	engine := newTestEngineWith(t, Options{Metrics: rec, MaxInflightRequests: 1, HealthChecker: &stubChecker{}, RegisterModules: func(v1 *gin.RouterGroup) {
		v1.GET("/hold/:id", func(c *gin.Context) { close(entered); <-release; c.Status(204) })
	}})
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		defer close(done)
		engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, "GET", "/api/v1/hold/1", nil))
	}()
	<-entered
	cancel()
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/v1/hold/2", 503}, {"GET", "/health", 200}, {"GET", "/ready", 200},
		{"GET", "/api/v1/missing", 404}, {"POST", "/api/v1/hold/1", 405},
	} {
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.want {
			t.Errorf("%s %s: %d", tc.method, tc.path, w.Code)
		}
		if tc.want == 503 && w.Header().Get("Retry-After") != "" {
			t.Error("unexpected fixed retry delay")
		}
	}
	close(release)
	<-done
	var data metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &data); err != nil {
		t.Fatal(err)
	}
	var rejected int64
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name == "admission.rejections" {
				sum, ok := m.Data.(metricdata.Sum[int64])
				if !ok {
					t.Fatalf("unexpected data: %T", m.Data)
				}
				for _, dp := range sum.DataPoints {
					rejected += dp.Value
					v, _ := dp.Attributes.Value("http.route")
					if v.AsString() != "/api/v1/hold/:id" || dp.Attributes.Len() != 4 {
						t.Fatalf("attributes: %v", dp.Attributes)
					}
				}
			}
		}
	}
	if rejected != 1 {
		t.Fatalf("rejections=%d, want only the matched saturated request", rejected)
	}
}

func TestTransferBudgetLeavesCapacityAndIndependentDeadline(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	e := newTestEngineWith(t, Options{MaxInflightRequests: 2, RegisterFileModule: func(short, transfers *gin.RouterGroup) {
		short.GET("/meta", func(c *gin.Context) {
			d, ok := c.Request.Context().Deadline()
			if !ok || time.Until(d) > 30*time.Second {
				t.Error("short deadline missing")
			}
			c.Status(204)
		})
		transfers.GET("/blob", middleware.Timeout(time.Minute, time.Second), func(c *gin.Context) {
			d, ok := c.Request.Context().Deadline()
			if !ok || time.Until(d) < 45*time.Second {
				t.Error("transfer deadline truncated")
			}
			close(entered)
			<-release
			c.Status(204)
		})
	}})
	call := func(path string) int {
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/"+path, nil))
		return w.Code
	}
	go func() { defer close(done); call("blob") }()
	<-entered
	defer func() { close(release); <-done }()
	if got := call("blob"); got != 503 {
		t.Fatalf("saturated transfer status %d", got)
	}
	if got := call("meta"); got != 204 {
		t.Fatalf("short request status %d", got)
	}
}
