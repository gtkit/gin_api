package config

import (
	"strings"
	"testing"
	"time"
)

// TestOutboundResolvedInheritsAndFallsBack 钉住三级取值：条目 → defaults → 内置兜底。
//
// 关键是**兜底必须存在**：一个漏配超时的客户端如果就这么以零值上线，
// 等于没有超时——一个卡住的下游就能把本进程拖垮。
func TestOutboundResolvedInheritsAndFallsBack(t *testing.T) {
	t.Parallel()

	defaults := OutboundClientConfig{
		Timeout:          2 * time.Second,
		MaxResponseBytes: 1024,
	}

	tests := []struct {
		name                 string
		client               OutboundClientConfig
		wantTimeout          time.Duration
		wantMaxResponseBytes int64
		wantMaxConns         int
	}{
		{
			name:                 "条目自己的值优先",
			client:               OutboundClientConfig{Timeout: 7 * time.Second, MaxResponseBytes: 2048, MaxConnsPerHost: 8},
			wantTimeout:          7 * time.Second,
			wantMaxResponseBytes: 2048,
			wantMaxConns:         8,
		},
		{
			name:                 "未设则继承 defaults",
			client:               OutboundClientConfig{},
			wantTimeout:          2 * time.Second,
			wantMaxResponseBytes: 1024,
			wantMaxConns:         outboundDefaultMaxConnsPerHost,
		},
		{
			name:                 "defaults 也未设则用内置兜底",
			client:               OutboundClientConfig{},
			wantTimeout:          2 * time.Second,
			wantMaxResponseBytes: 1024,
			wantMaxConns:         outboundDefaultMaxConnsPerHost,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.client.Resolved(defaults)
			if got.Timeout != tt.wantTimeout {
				t.Fatalf("Timeout = %s, want %s", got.Timeout, tt.wantTimeout)
			}
			if got.MaxResponseBytes != tt.wantMaxResponseBytes {
				t.Fatalf("MaxResponseBytes = %d, want %d", got.MaxResponseBytes, tt.wantMaxResponseBytes)
			}
			if got.MaxConnsPerHost != tt.wantMaxConns {
				t.Fatalf("MaxConnsPerHost = %d, want %d", got.MaxConnsPerHost, tt.wantMaxConns)
			}
		})
	}
}

// TestOutboundEmptyDefaultsStillGetBuiltIns 钉住 defaults 完全为空时仍有兜底。
func TestOutboundEmptyDefaultsStillGetBuiltIns(t *testing.T) {
	t.Parallel()

	got := OutboundClientConfig{}.Resolved(OutboundClientConfig{})
	if got.Timeout != outboundDefaultTimeout {
		t.Fatalf("Timeout = %s, want %s", got.Timeout, outboundDefaultTimeout)
	}
	if got.MaxResponseBytes != outboundDefaultMaxResponseBytes {
		t.Fatalf("MaxResponseBytes = %d, want %d", got.MaxResponseBytes, outboundDefaultMaxResponseBytes)
	}
	if got.MaxIdleConnsPerHost != outboundDefaultMaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConnsPerHost = %d, want %d", got.MaxIdleConnsPerHost, outboundDefaultMaxIdleConnsPerHost)
	}
}

// TestValidateOutboundConfig 钉住配置校验的三类拒绝与"未启用不校验"。
func TestValidateOutboundConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     OutboundConfig
		wantErr string
	}{
		{
			name: "未声明客户端即不启用",
			cfg:  OutboundConfig{},
		},
		{
			name: "合法配置",
			cfg: OutboundConfig{
				Clients: map[string]OutboundClientConfig{
					"billing": {BaseURL: "https://billing.example.test"},
				},
			},
		},
		{
			name: "缺 base_url",
			cfg: OutboundConfig{
				Clients: map[string]OutboundClientConfig{"billing": {Timeout: time.Second}},
			},
			wantErr: "base_url is required",
		},
		{
			name: "超时超过上限",
			cfg: OutboundConfig{
				Clients: map[string]OutboundClientConfig{
					"billing": {BaseURL: "https://billing.example.test", Timeout: time.Hour},
				},
			},
			wantErr: "exceeds the",
		},
		{
			name: "空闲连接数超过总连接数",
			cfg: OutboundConfig{
				Clients: map[string]OutboundClientConfig{
					"billing": {
						BaseURL:             "https://billing.example.test",
						MaxConnsPerHost:     2,
						MaxIdleConnsPerHost: 10,
					},
				},
			},
			wantErr: "exceeds max_conns_per_host",
		},
		{
			name: "空名字",
			cfg: OutboundConfig{
				Clients: map[string]OutboundClientConfig{"  ": {BaseURL: "https://x.example.test"}},
			},
			wantErr: "must not be empty",
		},
		{
			name: "熔断阈值为负",
			cfg:  OutboundConfig{Breaker: OutboundBreakerConfig{ConsecutiveFailures: -1}},
			// 未声明客户端也要拦：写错的阈值会在第一个下游接入时才爆。
			wantErr: "consecutive_failures -1 must not be negative",
		},
		{
			name:    "熔断开启态超上限",
			cfg:     OutboundConfig{Breaker: OutboundBreakerConfig{OpenTimeout: time.Hour}},
			wantErr: "open_timeout",
		},
		{
			name: "熔断零值合法（取库默认）",
			cfg:  OutboundConfig{Breaker: OutboundBreakerConfig{}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateOutboundConfig(tt.cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateOutboundConfig() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateOutboundConfig() = nil, want an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}
