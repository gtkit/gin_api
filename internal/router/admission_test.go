package router

import (
	"context"
	"net/http/httptest"
	"testing"

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
