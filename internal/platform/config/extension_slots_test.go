package config

import (
	"strings"
	"testing"
)

// TestUnwiredExtensionWarnings 钉住"填了却没接线"会被说出来。
//
// 这些配置段是刻意预留的扩展位——先把形状定下来，使用者接线时不必再改配置
// 契约。但预留不等于可用：**填了却静默无效的配置，比没有这个配置更危险**，
// 运维会以为邮件已经能发，直到某天发现一封都没出去。
func TestUnwiredExtensionWarnings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mutate   func(*Config)
		wantSlot string
	}{
		{
			name:   "an untouched config warns about nothing",
			mutate: func(*Config) {},
		},
		{
			name:     "a filled email host warns",
			mutate:   func(c *Config) { c.Email.Host = "smtp.example.com" },
			wantSlot: "email",
		},
		{
			name:     "enabling sms warns",
			mutate:   func(c *Config) { c.SMS.Enabled = true },
			wantSlot: "sms",
		},
		{
			name:     "a wechat app id warns",
			mutate:   func(c *Config) { c.Pay.Wechat.AppID = "wx-app" },
			wantSlot: "pay",
		},
		{
			name:     "an alipay app id warns through the same slot",
			mutate:   func(c *Config) { c.Pay.Alipay.AppID = "ali-app" },
			wantSlot: "pay",
		},
		{
			name:     "a baidu api key warns",
			mutate:   func(c *Config) { c.Baidu.Voice.APIKey = "key" },
			wantSlot: "baidu",
		},
		{
			// 只有空白不算填写：模板里留的占位空格不该触发提示。
			name:   "whitespace is not a filled value",
			mutate: func(c *Config) { c.Email.Host = "   " },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &Config{}
			tt.mutate(cfg)

			warnings := cfg.UnwiredExtensionWarnings()
			if tt.wantSlot == "" {
				if len(warnings) != 0 {
					t.Fatalf("warnings = %v, want none", warnings)
				}
				return
			}

			var found bool
			for _, w := range warnings {
				if strings.Contains(w, tt.wantSlot) {
					found = true
					// 提示必须给出接线方向，否则读到的人不知道下一步做什么。
					if !strings.Contains(w, "wire it up") {
						t.Errorf("warning %q does not say what to do next", w)
					}
				}
			}
			if !found {
				t.Fatalf("warnings = %v, want one mentioning %q", warnings, tt.wantSlot)
			}
		})
	}
}

// TestUnwiredExtensionWarningsHandlesNil 验证 nil 配置不 panic。
//
// 装配失败路径上调用方可能持有 nil；在告警函数里 panic 会掩盖真正的启动错误。
func TestUnwiredExtensionWarningsHandlesNil(t *testing.T) {
	t.Parallel()

	var cfg *Config
	if got := cfg.UnwiredExtensionWarnings(); got != nil {
		t.Fatalf("warnings = %v, want nil for a nil config", got)
	}
}
