package config

import (
	"strings"
	"testing"
	"time"
)

func TestCapacityConfigDecoding(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		waiters      int
		valid        bool
	}{
		{"omitted", "", 2, true},
		{"null", "  bcrypt_max_waiters: null\n", 2, true},
		{"zero", "  bcrypt_max_waiters: 0\n", 0, true},
		{"positive", "  bcrypt_max_waiters: 3\n", 3, true},
		{"negative", "  bcrypt_max_waiters: -1\n", 0, false},
		{"fraction", "  bcrypt_max_waiters: 1.5\n", 0, false},
		{"boolean", "  bcrypt_max_waiters: true\n", 0, false},
		{"negative timeout", "  bcrypt_queue_timeout: -1s\n", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			yml := strings.Replace(minimalConfigYAML(), "  mode: external\n", "  mode: external\n  bcrypt_max_concurrent: 2\n"+tc.fields, 1)
			cfg, err := loadFromReader(strings.NewReader(yml))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
			if err != nil {
				return
			}
			if got := cfg.Auth.EffectiveBcryptMaxWaiters(); got != tc.waiters {
				t.Fatalf("waiters=%d", got)
			}
			if cfg.Auth.EffectiveBcryptQueueTimeout() != time.Second {
				t.Fatal("default queue timeout changed")
			}
			if cfg.App.EffectiveMaxInflightRequests() != 128 {
				t.Fatal("default inflight changed")
			}
		})
	}
	for _, limit := range []int{-1, 0, 2} {
		cfg := AppConfig{Host: "localhost", Port: "8008", Name: "test", MaxInflightRequests: limit}
		if err := validateAppConfig(cfg); (err == nil) != (limit >= 0) {
			t.Fatalf("limit=%d, error=%v", limit, err)
		}
		if limit > 0 && cfg.EffectiveMaxInflightRequests() != limit {
			t.Fatal("explicit limit lost")
		}
	}
}
