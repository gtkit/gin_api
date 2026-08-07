package telemetry

import (
	"context"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
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
	_ = provider.Shutdown(context.Background())
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Shutdown 耗时 %s，超出预算：它会把关停一起拖住", elapsed)
	}

	// 重复 Shutdown 不得出事：关停路径上多处收尾很常见。
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("第二次 Shutdown: %v", err)
	}
}

// TestShutdownSurvivesCancelledContext 钉住"关停已被强制"不等于"遥测必然丢光"。
//
// 关停末期传进来的 ctx 很可能已被取消。Shutdown 用独立预算，让已采集的数据
// 仍有机会送出，同时超时保证它不会反过来拖住关停。
func TestShutdownSurvivesCancelledContext(t *testing.T) {
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
	_ = provider.Shutdown(ctx)
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
