package config

import (
	"strings"
	"testing"
)

func TestOptionalRedisConfiguration(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"no consumers", func(c *Config) {}, false},
		{"local limit", func(c *Config) {
			c.RateLimit.Policies = []RateLimitPolicyConfig{{ID: "local", Store: "local", Scope: "ip", FailureMode: "fail_open", Rate: 1, Burst: 1}}
		}, false},
		{"local auth", func(c *Config) { c.Auth.Mode = AuthModeLocal }, true},
		{"redis limit fail open", func(c *Config) {
			c.RateLimit.Policies = []RateLimitPolicyConfig{{ID: "redis", Store: "redis", Scope: "ip", FailureMode: "fail_open", Rate: 1, Burst: 1}}
		}, true},
		{"redis response cache", func(c *Config) { c.Cache = CacheConfig{Enabled: true, Store: CacheStoreRedis} }, true},
		{"queue", func(c *Config) { c.Tasks.Enabled = true }, true},
		{"memory cache", func(c *Config) { c.Cache = CacheConfig{Enabled: true, Store: CacheStoreMemory, MaxBytes: 1 << 20} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfigForTest()
			cfg.Auth.Mode = AuthModeExternal
			cfg.Redis = RedisConfig{}
			cfg.RateLimit.Policies = nil
			cfg.RateLimit.KeyHMACSecret = strings.Repeat("x", 32)
			cfg.Tasks.Enabled = false
			cfg.Cache.Enabled = false
			tc.mutate(cfg)
			err := cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate = %v, wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "redis.addr") {
				t.Fatalf("expected missing Redis configuration error, got %v", err)
			}
		})
	}
}
