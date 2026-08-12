// Package telemetry 装配 OpenTelemetry 的导出侧。
//
// 埋点与导出是两件事，本包只管后者。项目里的记录点（metrics.Recorder、
// otelgin 中间件）无条件存在，关闭导出时它们走 OTel 的全局 no-op provider，
// 每次记录都是空操作。
//
// 这样分是有原因的：把埋点也做成条件的，打开导出那天才会发现某条关键路径
// 压根没埋——而那一天通常正是出事要查的时候。
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// Provider 持有已装配的导出侧，关停时按预算 flush。
//
// 关闭态返回的是一个零值 Provider：Shutdown 无害，Enabled 为 false。
// 调用方因此不必判空，也不必写"如果启用了才关"这种分支。
type Provider struct {
	meter   *sdkmetric.MeterProvider
	tracer  *sdktrace.TracerProvider
	timeout time.Duration
	enabled bool
}

// New 按配置装配导出侧，并把 provider 设为全局。
//
// 关闭时不构造任何东西，也不改动全局 provider——进程与"没有本包"完全等价。
func New(ctx context.Context, cfg config.TelemetryConfig, serviceName string) (*Provider, error) {
	if !cfg.Enabled {
		return &Provider{}, nil
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(buildVersion()),
	))
	if err != nil {
		return nil, fmt.Errorf("telemetry: build resource: %w", err)
	}

	provider := &Provider{enabled: true, timeout: cfg.EffectiveShutdownTimeout()}

	// 中途失败必须回收已建的那半：exporter 持有连接与后台 goroutine，
	// 直接返回错误会把它们留到进程退出。
	if cfg.Metrics {
		if err := provider.initMetrics(ctx, cfg, res); err != nil {
			return nil, errors.Join(err, provider.Shutdown(ctx))
		}
	}
	if cfg.Traces {
		if err := provider.initTraces(ctx, cfg, res); err != nil {
			return nil, errors.Join(err, provider.Shutdown(ctx))
		}
	}

	// 跨进程传播必须设置，否则本服务产生的 span 与上游的对不上——
	// 单个服务内部看起来正常，整条调用链却是断的。
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	return provider, nil
}

func (p *Provider) initMetrics(ctx context.Context, cfg config.TelemetryConfig, res *resource.Resource) error {
	exporter, err := otlpmetrichttp.New(ctx, metricExporterOptions(cfg)...)
	if err != nil {
		return fmt.Errorf("telemetry: build metric exporter: %w", err)
	}

	p.meter = sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(
			exporter, sdkmetric.WithInterval(cfg.EffectiveExportInterval()),
		)),
	)
	otel.SetMeterProvider(p.meter)
	return nil
}

func (p *Provider) initTraces(ctx context.Context, cfg config.TelemetryConfig, res *resource.Resource) error {
	exporter, err := otlptracehttp.New(ctx, traceExporterOptions(cfg)...)
	if err != nil {
		return fmt.Errorf("telemetry: build trace exporter: %w", err)
	}

	p.tracer = sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		// Batcher 而不是 SimpleSpanProcessor：后者每个 span 同步导出一次，
		// 把收集端的时延直接加到每个请求上。
		sdktrace.WithBatcher(exporter),
		// ParentBased 包住比例采样：上游已经决定采样的链路必须整条留下，
		// 否则调用链在本服务这里断掉，而那恰恰是最需要看的那种链路。
		sdktrace.WithSampler(sdktrace.ParentBased(
			sdktrace.TraceIDRatioBased(cfg.EffectiveSampleRatio()),
		)),
	)
	otel.SetTracerProvider(p.tracer)
	return nil
}

func metricExporterOptions(cfg config.TelemetryConfig) []otlpmetrichttp.Option {
	opts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpoint(cfg.Endpoint)}
	if cfg.Insecure {
		opts = append(opts, otlpmetrichttp.WithInsecure())
	}
	if len(cfg.Headers) > 0 {
		opts = append(opts, otlpmetrichttp.WithHeaders(cfg.Headers))
	}
	return opts
}

func traceExporterOptions(cfg config.TelemetryConfig) []otlptracehttp.Option {
	opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(cfg.Endpoint)}
	if cfg.Insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	if len(cfg.Headers) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(cfg.Headers))
	}
	return opts
}

// Enabled 报告导出是否启用。
func (p *Provider) Enabled() bool { return p != nil && p.enabled }

// Shutdown 在预算内 flush 并关闭 provider。
//
// flush 有自己的预算上界，同时**尊重调用方 ctx 的取消**：优雅关停路径上
// 调用方给的是一个不随信号 ctx 取消的 base（数据会在预算内送出去）；
// 强制关停时那个 base 被取消，flush 如实中止——"第二个信号取消各阶段
// 剩余等待"的契约在这里同样成立。此前这里用 WithoutCancel 丢弃取消信号，
// 强制路径就得为一个不可达的 collector 白等满整个预算。
func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil || (p.meter == nil && p.tracer == nil) {
		return nil
	}

	flushCtx, cancel := context.WithTimeout(ctx, p.flushBudget())
	defer cancel()

	var errs []error
	if p.tracer != nil {
		// 先关 tracer：它的 batcher 里可能还压着 span，而 span 的价值
		// 高于一个周期的指标（指标下个周期还会再来，span 丢了就没了）。
		errs = append(errs, p.tracer.Shutdown(flushCtx))
		p.tracer = nil
	}
	if p.meter != nil {
		errs = append(errs, p.meter.Shutdown(flushCtx))
		p.meter = nil
	}
	return errors.Join(errs...)
}

func (p *Provider) flushBudget() time.Duration {
	if p.timeout > 0 {
		return p.timeout
	}
	return 5 * time.Second
}

// buildVersion 从构建信息取版本，用于 trace/metric 的 service.version 属性。
//
// 不引入一条 -ldflags 注入的版本变量：那要求每条构建路径都记得带上它，
// 漏一条就是一个没人发现的空版本。构建信息由 go 工具链自动填入
// （模块版本，或本地构建时的 (devel)），任何构建方式都不会缺。
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" {
		return "unknown"
	}
	return info.Main.Version
}
