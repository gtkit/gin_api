package outbound

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

	defaults := ClientConfig{
		Timeout:          2 * time.Second,
		MaxResponseBytes: 1024,
	}

	tests := []struct {
		name                 string
		client               ClientConfig
		wantTimeout          time.Duration
		wantMaxResponseBytes int64
		wantMaxConns         int
	}{
		{
			name:                 "条目自己的值优先",
			client:               ClientConfig{Timeout: 7 * time.Second, MaxResponseBytes: 2048, MaxConnsPerHost: 8},
			wantTimeout:          7 * time.Second,
			wantMaxResponseBytes: 2048,
			wantMaxConns:         8,
		},
		{
			name:                 "未设则继承 defaults",
			client:               ClientConfig{},
			wantTimeout:          2 * time.Second,
			wantMaxResponseBytes: 1024,
			wantMaxConns:         outboundDefaultMaxConnsPerHost,
		},
		{
			name:                 "defaults 也未设则用内置兜底",
			client:               ClientConfig{},
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

	got := ClientConfig{}.Resolved(ClientConfig{})
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

// TestValidateConfig 钉住配置校验的三类拒绝与"未启用不校验"。
func TestValidateConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name: "未声明客户端即不启用",
			cfg:  Config{},
		},
		{
			name: "合法配置",
			cfg: Config{
				Clients: map[string]ClientConfig{
					"billing": {BaseURL: "https://billing.example.test"},
				},
			},
		},
		{
			name: "缺 base_url",
			cfg: Config{
				Clients: map[string]ClientConfig{"billing": {Timeout: time.Second}},
			},
			wantErr: "base_url is required",
		},
		{
			name: "超时超过上限",
			cfg: Config{
				Clients: map[string]ClientConfig{
					"billing": {BaseURL: "https://billing.example.test", Timeout: time.Hour},
				},
			},
			wantErr: "exceeds the",
		},
		{
			name: "空闲连接数超过总连接数",
			cfg: Config{
				Clients: map[string]ClientConfig{
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
			cfg: Config{
				Clients: map[string]ClientConfig{"  ": {BaseURL: "https://x.example.test"}},
			},
			wantErr: "must not be empty",
		},
		{
			name: "熔断阈值为负",
			cfg:  Config{Breaker: BreakerConfig{ConsecutiveFailures: -1}},
			// 未声明客户端也要拦：写错的阈值会在第一个下游接入时才爆。
			wantErr: "consecutive_failures -1 must not be negative",
		},
		{
			name:    "熔断开启态超上限",
			cfg:     Config{Breaker: BreakerConfig{OpenTimeout: time.Hour}},
			wantErr: "open_timeout",
		},
		{
			name: "熔断零值合法（取库默认）",
			cfg:  Config{Breaker: BreakerConfig{}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestNegativeConfigValuesAreRejected 钉住负值不静默回退（P2 回归）。
//
// 回退让"配了一个非法值"与"没配"变成同一种结果，而使用者以为自己配的
// 那个值正在生效——排查时没有任何一处会提示真相。
func TestNegativeConfigValuesAreRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "outbound client 负超时",
			mutate: func(c *Config) {
				c.Clients = map[string]ClientConfig{
					"billing": {BaseURL: "https://billing.example.test", Timeout: -time.Second},
				}
			},
			wantErr: "negative values are not allowed",
		},
		{
			name:    "outbound defaults 负响应体上限",
			mutate:  func(c *Config) { c.Defaults.MaxResponseBytes = -1 },
			wantErr: "outbound.defaults",
		},
		{
			name: "base_url 非绝对 URL",
			mutate: func(c *Config) {
				c.Clients = map[string]ClientConfig{
					"billing": {BaseURL: "billing.example.test/api"},
				}
			},
			wantErr: "not an absolute http(s) URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &Config{}
			tt.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}
