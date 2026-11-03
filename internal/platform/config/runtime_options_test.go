package config

import (
	"strings"
	"testing"
	"time"
)

func TestRuntimeOptionsAcceptDeploymentChoices(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		set  func(*Config)
	}{
		{"log defaults", func(c *Config) { c.Log.Level, c.Log.Path = "", "" }},
		{"pool idle clamp", func(c *Config) { c.Database.MaxOpenConn, c.Database.MaxIdleConn = 2, 20 }},
		{"prepared cache", func(c *Config) { c.Database.PrepareStmtMaxSize, c.Database.PrepareStmtTTLSeconds = 8192, 172800 }},
		{"response cache ttl", func(c *Config) {
			c.Cache = CacheConfig{Enabled: true, Store: CacheStoreMemory, MaxBytes: 1024, TTL: time.Hour}
		}},
		{"telemetry shutdown", func(c *Config) {
			c.Telemetry = TelemetryConfig{Enabled: true, Endpoint: "collector:4318", Metrics: true, ShutdownTimeout: time.Minute}
		}},
		{"bcrypt concurrency", func(c *Config) { c.Auth.BcryptMaxConcurrent = 4096 }},
		{"proxy without forwarded headers", func(c *Config) {
			c.Proxy = ProxyConfig{TrustedProxies: []string{"10.0.0.0/8"}}
		}},
		{"disabled notifications", func(c *Config) {
			c.Email = EmailConfig{From: "noreply@example.com"}
			c.SMS = SMSConfig{SignName: "unused"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfigForTest()
			tc.set(cfg)
			if err := cfg.Validate(); err != nil {
				t.Fatalf("deployment choice was rejected: %v", err)
			}
		})
	}
}

func TestHistoricalConfigKeysAreIgnored(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("APP_DATABASE__MIN_VERSION", "unused")
	t.Setenv("APP_UNRELATED__VALUE", "unused")
	contents := strings.Replace(minimalConfigYAML(), "database:\n", "database:\n  dbtype: unused\n  min_version: unused\n", 1) + "\nretired_setting: true\n"
	if _, err := loadFromReader(strings.NewReader(contents)); err != nil {
		t.Fatalf("historical configuration blocked startup: %v", err)
	}
}
