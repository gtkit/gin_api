package middleware

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/gtkit/gin-api/internal/pkg/metrics"
)

// newMetricsEngine 装配一个带指标中间件的引擎。
func newMetricsEngine(t *testing.T) (*gin.Engine, *sdkmetric.ManualReader) {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	recorder, err := metrics.New(provider.Meter("test"))
	if err != nil {
		t.Fatalf("metrics.New: %v", err)
	}

	engine := gin.New()
	engine.Use(Metrics(recorder))
	engine.GET("/api/v1/user/:id", func(c *gin.Context) { c.Status(http.StatusOK) })
	return engine, reader
}

// routeLabels 采集一次并返回请求指标上出现过的全部 http.route 取值。
func routeLabels(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	labels := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "http.server.requests" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("unexpected data shape %T", m.Data)
			}
			for _, dp := range sum.DataPoints {
				route, ok := dp.Attributes.Value("http.route")
				if !ok {
					t.Fatal("a request data point carries no http.route label")
				}
				labels[route.AsString()] += dp.Value
			}
		}
	}
	return labels
}

// TestScanningTrafficCollapsesIntoOneSeries 是本中间件最重要的一条。
//
// 未匹配路径完全由请求方控制。若中间件用 `c.Request.URL.Path` 作标签，
// 一次目录爆破就能凭空造出成千上万条时间序列，把监控后端压垮——
// 而故障出现在监控侧，恰恰是最需要它的时候不可用。
func TestScanningTrafficCollapsesIntoOneSeries(t *testing.T) {
	t.Parallel()

	engine, reader := newMetricsEngine(t)

	// 模拟一次目录扫描：100 个各不相同的不存在路径。
	for i := range 100 {
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(
			http.MethodGet, fmt.Sprintf("/nonexistent/path-%d", i), nil))
	}

	labels := routeLabels(t, reader)
	if len(labels) != 1 {
		t.Fatalf("100 scanning requests produced %d route labels (%v), want 1: "+
			"per-path labels are how a monitoring backend gets taken down", len(labels), labels)
	}
	if got := labels[metrics.UnmatchedRoute]; got != 100 {
		t.Fatalf("unmatched requests = %d under label %q, want 100 (labels: %v)",
			got, metrics.UnmatchedRoute, labels)
	}
}

// TestPathParametersShareOneSeries 钉住路径参数不产生新序列。
//
// 用实际路径作标签的话，序列数会随业务数据量增长——每一个 id 一条。
func TestPathParametersShareOneSeries(t *testing.T) {
	t.Parallel()

	engine, reader := newMetricsEngine(t)

	for i := range 50 {
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(
			http.MethodGet, fmt.Sprintf("/api/v1/user/%d", i), nil))
	}

	labels := routeLabels(t, reader)
	if len(labels) != 1 {
		t.Fatalf("50 requests with distinct path parameters produced %d route labels (%v), "+
			"want 1", len(labels), labels)
	}
	if got := labels["/api/v1/user/:id"]; got != 50 {
		t.Fatalf("requests under the route template = %d, want 50 (labels: %v)", got, labels)
	}
}

// TestNilRecorderPassesThrough 钉住未接指标时请求照常处理。
func TestNilRecorderPassesThrough(t *testing.T) {
	t.Parallel()

	engine := gin.New()
	engine.Use(Metrics(nil))
	engine.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ping", nil))

	if rec.Code != http.StatusOK || rec.Body.String() != "pong" {
		t.Fatalf("request handling broke without a recorder: %d %q", rec.Code, rec.Body.String())
	}
}
