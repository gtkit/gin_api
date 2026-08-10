package config

import (
	"strings"
	"testing"
	"time"
)

// TestValidateAlertBotConfig 钉住群机器人配置校验。
//
// 重点是"启用了却发不出去"这类静默失效：告警通道自己不能成为自己的监控，
// 缺 webhook 时每条告警都失败，而只能靠"怎么一直没收到告警"来发现。
func TestValidateAlertBotConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     AlertBotConfig
		wantErr string
	}{
		{name: "关闭即不校验", cfg: AlertBotConfig{Enabled: false}},
		{
			name: "合法",
			cfg:  AlertBotConfig{Enabled: true, Platform: AlertBotFeishu, WebhookURL: "https://x/hook"},
		},
		{
			name:    "缺平台",
			cfg:     AlertBotConfig{Enabled: true, WebhookURL: "https://x/hook"},
			wantErr: "platform is required",
		},
		{
			name:    "未知平台",
			cfg:     AlertBotConfig{Enabled: true, Platform: "slack", WebhookURL: "https://x/hook"},
			wantErr: "unknown alertbot.platform",
		},
		{
			name:    "缺 webhook",
			cfg:     AlertBotConfig{Enabled: true, Platform: AlertBotWeCom},
			wantErr: "cannot alert about itself",
		},
		{
			name: "超时超上限",
			cfg: AlertBotConfig{
				Enabled: true, Platform: AlertBotDingTalk,
				WebhookURL: "https://x/hook", Timeout: time.Hour,
			},
			wantErr: "exceeds the",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateAlertBotConfig(tt.cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateAlertBotConfig() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateAlertBotConfig() = nil, want an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}
