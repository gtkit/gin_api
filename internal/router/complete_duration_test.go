package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/logger/v2"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/zap"
)

func TestHTTPDurationIncludesAccessLog(t *testing.T) {
	const logDelay = 50 * time.Millisecond
	access, err := logger.New(logger.WithPath(filepath.Join(t.TempDir(), "access")), logger.WithFile(true), logger.WithConsole(false), logger.WithReplaceGlobals(false),
		logger.WithContextFields(func(context.Context) []zap.Field { time.Sleep(logDelay); return nil }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(access.Sync)
	// Start the real file rotator outside the clock bubble; its lifetime belongs
	// to the surrounding test, not to the simulated request.
	access.Info("warmup")
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	recorder, err := metrics.New(provider.Meter("complete-duration"))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := newRouterForTest(t, "test", Options{AccessLogger: access, Metrics: recorder})
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		response := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			defer close(done)
			engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
		}()
		synctest.Wait()
		var inProgress metricdata.ResourceMetrics
		if err := reader.Collect(t.Context(), &inProgress); err != nil {
			t.Fatal(err)
		}
		var active int64
		for _, scope := range inProgress.ScopeMetrics {
			for _, metric := range scope.Metrics {
				if metric.Name != "http.server.active_requests" {
					continue
				}
				data, ok := metric.Data.(metricdata.Sum[int64])
				if !ok {
					t.Fatalf("activity type %T", metric.Data)
				}
				for _, point := range data.DataPoints {
					active += point.Value
				}
			}
		}
		if active != 1 {
			t.Errorf("activity while access log waits = %d, want 1", active)
		}
		synctest.Sleep(logDelay)
		<-done
		var result metricdata.ResourceMetrics
		if err := reader.Collect(t.Context(), &result); err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, scope := range result.ScopeMetrics {
			for _, metric := range scope.Metrics {
				if metric.Name != "http.server.request.duration" {
					continue
				}
				data, ok := metric.Data.(metricdata.Histogram[float64])
				if !ok {
					t.Fatalf("duration type %T", metric.Data)
				}
				for _, point := range data.DataPoints {
					found = true
					if point.Count != 1 || point.Sum < logDelay.Seconds() {
						t.Errorf("duration=%f count=%d, want >=%s", point.Sum, point.Count, logDelay)
					}
				}
			}
		}
		if !found {
			t.Fatal("duration missing")
		}
	})
}
