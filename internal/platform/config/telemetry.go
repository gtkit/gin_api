package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// TelemetryConfig 是 OpenTelemetry 导出的进程级配置。
//
// 默认关闭。关闭时进程沿用 OTel 的全局 no-op provider——所有记录点仍在，
// 只是每次记录都是空操作。这一点很重要：埋点不该随开关增减，否则打开导出
// 时才发现某条路径压根没埋。
type TelemetryConfig struct {
	// Enabled 是总开关。
	Enabled bool `mapstructure:"enabled"`
	// Endpoint 是 OTLP/HTTP 收集端地址（如 otel-collector:4318）。
	Endpoint string `mapstructure:"endpoint"`
	// Insecure 为真时用明文 HTTP。仅限收集端在同一内网/边车时使用。
	Insecure bool `mapstructure:"insecure"`
	// Headers 是导出请求附带的头（如租户或鉴权令牌）。
	Headers map[string]string `mapstructure:"headers"`
	// Metrics / Traces 分别控制两类信号是否导出。
	Metrics bool `mapstructure:"metrics"`
	Traces  bool `mapstructure:"traces"`
	// SampleRatio 是 trace 采样率 (0,1]。
	SampleRatio float64 `mapstructure:"sample_ratio"`
	// ExportInterval 是指标导出周期。
	ExportInterval time.Duration `mapstructure:"export_interval"`
	// ShutdownTimeout 是关停时 flush 的预算。
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
}

// 未配置时的兜底值。
//
// 导出周期取 30s：更短会让收集端与网络承担与信息量不相称的开销，
// 指标本来就是聚合值，30s 的分辨率对告警与容量判断都够用。
//
// 关停 flush 预算取 5s：它挤占的是优雅关停的总预算。遥测数据丢一点
// 可以接受，因为关停而超时的连接不可以——这是明确的优先级。
const (
	telemetryDefaultExportInterval  = 30 * time.Second
	telemetryDefaultShutdownTimeout = 5 * time.Second
	telemetryDefaultSampleRatio     = 0.1
)

// maxTelemetryShutdownTimeout 是 flush 预算的上限。
const maxTelemetryShutdownTimeout = 30 * time.Second

// EffectiveExportInterval 返回生效的指标导出周期。
func (c TelemetryConfig) EffectiveExportInterval() time.Duration {
	if c.ExportInterval > 0 {
		return c.ExportInterval
	}
	return telemetryDefaultExportInterval
}

// EffectiveShutdownTimeout 返回生效的 flush 预算。
func (c TelemetryConfig) EffectiveShutdownTimeout() time.Duration {
	if c.ShutdownTimeout > 0 {
		return c.ShutdownTimeout
	}
	return telemetryDefaultShutdownTimeout
}

// EffectiveSampleRatio 返回生效的 trace 采样率。
func (c TelemetryConfig) EffectiveSampleRatio() float64 {
	if c.SampleRatio > 0 {
		return c.SampleRatio
	}
	return telemetryDefaultSampleRatio
}

// validateTelemetryConfig 校验遥测配置。
func validateTelemetryConfig(cfg TelemetryConfig) error {
	if !cfg.Enabled {
		return nil
	}

	var errs []error

	if strings.TrimSpace(cfg.Endpoint) == "" {
		errs = append(errs, errors.New(
			"telemetry.endpoint is required when telemetry is enabled: without it the exporter "+
				"would keep failing in the background while the process looks healthy"))
	}
	if !cfg.Metrics && !cfg.Traces {
		errs = append(errs, errors.New(
			"telemetry is enabled but both telemetry.metrics and telemetry.traces are false: "+
				"that builds an exporter that exports nothing"))
	}
	if cfg.SampleRatio < 0 || cfg.SampleRatio > 1 {
		errs = append(errs, fmt.Errorf(
			"telemetry.sample_ratio %v is outside (0, 1]", cfg.SampleRatio))
	}
	if cfg.EffectiveShutdownTimeout() > maxTelemetryShutdownTimeout {
		errs = append(errs, fmt.Errorf(
			"telemetry.shutdown_timeout %s exceeds the %s cap: it is taken out of the graceful "+
				"shutdown budget, and losing some telemetry beats holding connections open",
			cfg.EffectiveShutdownTimeout(), maxTelemetryShutdownTimeout))
	}

	return errors.Join(errs...)
}
