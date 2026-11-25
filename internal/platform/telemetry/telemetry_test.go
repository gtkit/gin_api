package telemetry

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/middleware"
	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/internal/platform/config"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestDisabledLeavesGlobalsUntouched 钉住关闭态与"没有本包"完全等价。
//
// 这是两态里更容易出错的那一态：只要构造期动了全局 provider，关闭配置就不再
// 是"零成本"，而是一个没人注意到的行为变更。
func TestDisabledLeavesGlobalsUntouched(t *testing.T) {
	// 不能 t.Parallel()：断言的是进程级全局 provider。
	beforeMeter := otel.GetMeterProvider()
	beforeTracer := otel.GetTracerProvider()
	beforeProp := otel.GetTextMapPropagator()

	provider, err := New(context.Background(), config.TelemetryConfig{Enabled: false}, "svc")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if provider.Enabled() {
		t.Fatal("关闭态报告为启用")
	}

	if otel.GetMeterProvider() != beforeMeter {
		t.Fatal("关闭态改动了全局 MeterProvider")
	}
	if otel.GetTracerProvider() != beforeTracer {
		t.Fatal("关闭态改动了全局 TracerProvider")
	}
	if otel.GetTextMapPropagator() != beforeProp {
		t.Fatal("关闭态改动了全局 Propagator")
	}

	// 关闭态的 Shutdown 必须无害且不消耗预算。
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("关闭态 Shutdown: %v", err)
	}
}

// TestNilProviderIsSafe 钉住 nil 可用：装配失败路径上调用方可能拿到 nil。
func TestNilProviderIsSafe(t *testing.T) {
	t.Parallel()

	var provider *Provider
	if provider.Enabled() {
		t.Fatal("nil Provider 报告为启用")
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("nil Shutdown: %v", err)
	}
}

// TestEnabledInstallsProvidersAndFlushes 钉住开启态**真的**换掉了全局 provider，
// 并且关停能在预算内收尾。
//
// 用一个不存在的收集端：导出必然失败，但装配与关停的行为不该依赖收集端可达——
// 收集端挂掉时进程仍要能正常启动和退出，这正是要验的。
func TestEnabledInstallsProvidersAndFlushes(t *testing.T) {
	// 不能 t.Parallel()：改的是进程级全局 provider。
	restore := snapshotGlobals()
	defer restore()

	cfg := config.TelemetryConfig{
		Enabled:         true,
		Endpoint:        "127.0.0.1:1",
		Insecure:        true,
		Metrics:         true,
		Traces:          true,
		SampleRatio:     1,
		ExportInterval:  time.Hour,
		ShutdownTimeout: 300 * time.Millisecond,
	}

	provider, err := New(context.Background(), cfg, "svc")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !provider.Enabled() {
		t.Fatal("开启态报告为未启用")
	}

	if _, ok := otel.GetMeterProvider().(*sdkmetric.MeterProvider); !ok {
		t.Fatalf("全局 MeterProvider 是 %T，want *sdkmetric.MeterProvider", otel.GetMeterProvider())
	}
	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); !ok {
		t.Fatalf("全局 TracerProvider 是 %T，want *sdktrace.TracerProvider", otel.GetTracerProvider())
	}
	// 跨进程传播必须装上，否则本服务的 span 与上游对不上，整条链是断的。
	if fields := otel.GetTextMapPropagator().Fields(); len(fields) == 0 {
		t.Fatal("没有安装任何传播器：调用链会在本服务断掉")
	}

	// 关停必须在预算内返回，即使收集端不可达。
	start := time.Now()
	if err := provider.Shutdown(context.Background()); err != nil {
		dialErr, ok := errors.AsType[*net.OpError](err)
		if !errors.Is(err, context.DeadlineExceeded) && (!ok || dialErr.Op != "dial") {
			t.Fatalf("unexpected exporter shutdown failure: %v", err)
		}
		t.Logf("unreachable test collector reported within shutdown budget: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Shutdown 耗时 %s，超出预算：它会把关停一起拖住", elapsed)
	}

	// 重复 Shutdown 不得出事：关停路径上多处收尾很常见。
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("第二次 Shutdown: %v", err)
	}
}

// TestShutdownHandlesCancelledContextWithinBudget 检查取消原因不会被误判或拖住关停。
// Provider沿用调用方的取消；独立关停预算由上层生命周期提供。
// 本用例没有已导出的span证据，不能据耗时断言宣称数据已排空。
func TestShutdownHandlesCancelledContextWithinBudget(t *testing.T) {
	restore := snapshotGlobals()
	defer restore()

	provider, err := New(context.Background(), config.TelemetryConfig{
		Enabled:         true,
		Endpoint:        "127.0.0.1:1",
		Insecure:        true,
		Traces:          true,
		SampleRatio:     1,
		ShutdownTimeout: 200 * time.Millisecond,
	}, "svc")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	if err := provider.Shutdown(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected canceled shutdown failure: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("已取消 ctx 下 Shutdown 耗时 %s", elapsed)
	}
}

// snapshotGlobals 记下全局 provider 并返回还原函数。
//
// 不还原的话，一个开启态测试会把 SDK provider 留给同包后续测试，
// 让它们的断言依赖执行顺序。
func snapshotGlobals() func() {
	meter := otel.GetMeterProvider()
	tracer := otel.GetTracerProvider()
	prop := otel.GetTextMapPropagator()
	return func() {
		otel.SetMeterProvider(meter)
		otel.SetTracerProvider(tracer)
		otel.SetTextMapPropagator(prop)
	}
}

func TestHTTPDurationViewKeepsOneEndToEndObservation(t *testing.T) {
	for _, tt := range []struct {
		name           string
		measure, trace bool
	}{
		{name: "disabled"},
		{name: "metrics only", measure: true},
		{name: "traces only", trace: true},
		{name: "metrics and traces", measure: true, trace: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reader := sdkmetric.NewManualReader()
			var meterProvider metric.MeterProvider = metricnoop.NewMeterProvider()
			if tt.measure {
				provider := newMeterProvider(sdkmetric.WithReader(reader))
				t.Cleanup(func() {
					if err := provider.Shutdown(context.Background()); err != nil {
						t.Error(err)
					}
				})
				meterProvider = provider
			}
			spans := tracetest.NewSpanRecorder()
			tracer := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
			t.Cleanup(func() {
				if err := tracer.Shutdown(context.Background()); err != nil {
					t.Error(err)
				}
			})
			recorder, err := metrics.New(meterProvider.Meter("application-test"))
			if err != nil {
				t.Fatal(err)
			}
			engine := gin.New()
			engine.Use(middleware.Metrics(recorder))
			if tt.trace {
				engine.Use(otelgin.Middleware("test", otelgin.WithMeterProvider(meterProvider), otelgin.WithTracerProvider(tracer)))
			}
			engine.Use(middleware.GinRecovery(false))
			engine.POST("/success", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
			engine.POST("/reject", func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) })
			engine.POST("/panic", func(*gin.Context) { panic("test") })
			engine.POST("/stream", func(c *gin.Context) {
				c.String(http.StatusAccepted, "begin")
				c.Writer.Flush()
				_, _ = c.Writer.WriteString("end")
			})
			cases := map[string]int{"/success": 200, "/reject": 401, "/panic": 500, "/stream": 202}
			for path, status := range cases {
				w := httptest.NewRecorder()
				engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader("body")))
				if w.Code != status {
					t.Fatalf("%s: status %d, want %d", path, w.Code, status)
				}
			}
			if tt.trace && len(spans.Ended()) != len(cases) || !tt.trace && len(spans.Ended()) != 0 {
				t.Fatalf("ended spans = %d", len(spans.Ended()))
			}
			if !tt.measure {
				return
			}
			var data metricdata.ResourceMetrics
			if err := reader.Collect(t.Context(), &data); err != nil {
				t.Fatal(err)
			}
			var count uint64
			bodyMetrics := map[string]bool{}
			for _, scope := range data.ScopeMetrics {
				for _, m := range scope.Metrics {
					switch m.Name {
					case "http.server.request.duration":
						if scope.Scope.Name != "application-test" || m.Unit != "s" {
							t.Fatalf("duration from unexpected scope or unit: %s %s", scope.Scope.Name, m.Unit)
						}
						for _, point := range m.Data.(metricdata.Histogram[float64]).DataPoints {
							count += point.Count
						}
					case "http.server.request.body.size", "http.server.response.body.size":
						if scope.Scope.Name == otelgin.ScopeName {
							bodyMetrics[m.Name] = true
						}
					}
				}
			}
			if count != uint64(len(cases)) {
				t.Fatalf("duration sample count = %d, want %d", count, len(cases))
			}
			if tt.trace && len(bodyMetrics) != 2 || !tt.trace && len(bodyMetrics) != 0 {
				t.Fatalf("otelgin byte instruments = %v", bodyMetrics)
			}
		})
	}
}
