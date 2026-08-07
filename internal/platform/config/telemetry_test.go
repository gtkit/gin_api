package config

import (
	"strings"
	"testing"
	"time"
)

// TestValidateTelemetryConfig 钉住遥测配置校验。
//
// 重点是"启用了却导不出任何东西"这类静默失效：endpoint 缺失或两类信号都关，
// 都会让进程看起来接了遥测而实际上什么也没有——比明确的关闭更坏。
func TestValidateTelemetryConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     TelemetryConfig
		wantErr string
	}{
		{name: "关闭即不校验", cfg: TelemetryConfig{Enabled: false}},
		{
			name: "合法",
			cfg:  TelemetryConfig{Enabled: true, Endpoint: "collector:4318", Metrics: true},
		},
		{
			name:    "缺 endpoint",
			cfg:     TelemetryConfig{Enabled: true, Metrics: true},
			wantErr: "endpoint is required",
		},
		{
			name:    "两类信号都关",
			cfg:     TelemetryConfig{Enabled: true, Endpoint: "collector:4318"},
			wantErr: "exports nothing",
		},
		{
			name: "采样率越界",
			cfg: TelemetryConfig{
				Enabled: true, Endpoint: "collector:4318", Traces: true, SampleRatio: 2,
			},
			wantErr: "sample_ratio",
		},
		{
			name: "flush 预算超上限",
			cfg: TelemetryConfig{
				Enabled: true, Endpoint: "collector:4318", Metrics: true,
				ShutdownTimeout: time.Hour,
			},
			wantErr: "exceeds the",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateTelemetryConfig(tt.cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateTelemetryConfig() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateTelemetryConfig() = nil, want an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestTelemetryEffectiveValues 钉住三个兜底值。
func TestTelemetryEffectiveValues(t *testing.T) {
	t.Parallel()

	empty := TelemetryConfig{}
	if got := empty.EffectiveExportInterval(); got != telemetryDefaultExportInterval {
		t.Fatalf("EffectiveExportInterval() = %s, want %s", got, telemetryDefaultExportInterval)
	}
	if got := empty.EffectiveShutdownTimeout(); got != telemetryDefaultShutdownTimeout {
		t.Fatalf("EffectiveShutdownTimeout() = %s, want %s", got, telemetryDefaultShutdownTimeout)
	}
	if got := empty.EffectiveSampleRatio(); got != telemetryDefaultSampleRatio {
		t.Fatalf("EffectiveSampleRatio() = %v, want %v", got, telemetryDefaultSampleRatio)
	}

	set := TelemetryConfig{ExportInterval: time.Minute, ShutdownTimeout: time.Second, SampleRatio: 0.5}
	if got := set.EffectiveExportInterval(); got != time.Minute {
		t.Fatalf("EffectiveExportInterval() = %s, want 1m", got)
	}
	if got := set.EffectiveShutdownTimeout(); got != time.Second {
		t.Fatalf("EffectiveShutdownTimeout() = %s, want 1s", got)
	}
	if got := set.EffectiveSampleRatio(); got != 0.5 {
		t.Fatalf("EffectiveSampleRatio() = %v, want 0.5", got)
	}
}
