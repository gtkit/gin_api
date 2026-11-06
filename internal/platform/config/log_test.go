package config

import (
	"strings"
	"testing"
	"time"
)

func TestAccessFlushIntervalConfig(t *testing.T) {
	clearConfigEnv(t)
	for _, tc := range []struct {
		name    string
		value   string
		want    time.Duration
		wantErr bool
	}{
		{name: "omitted", want: time.Second},
		{name: "null", value: "null", want: time.Second},
		{name: "zero", value: "0s", want: time.Second},
		{name: "subsecond", value: "250ms", want: 250 * time.Millisecond},
		{name: "custom", value: "2s", want: 2 * time.Second},
		{name: "long", value: "48h", want: 48 * time.Hour},
		{name: "negative", value: "-1s", wantErr: true},
		{name: "malformed", value: "invalid", wantErr: true},
		{name: "overflow", value: "9223372036854775808ns", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := minimalConfigYAML()
			if tc.value != "" {
				input = strings.Replace(input, "log:\n", "log:\n  access_flush_interval: "+tc.value+"\n", 1)
			}
			cfg, err := loadFromReader(strings.NewReader(input))
			if tc.wantErr {
				if err == nil {
					t.Fatal("invalid access flush interval was accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Log.EffectiveAccessFlushInterval(); got != tc.want {
				t.Fatalf("access flush interval = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestAccessFlushIntervalEnvOverride(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("APP_LOG__ACCESS_FLUSH_INTERVAL", "125ms")
	input := strings.Replace(minimalConfigYAML(), "log:\n", "log:\n  access_flush_interval: 2s\n", 1)
	cfg, err := loadFromReader(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Log.EffectiveAccessFlushInterval(); got != 125*time.Millisecond {
		t.Fatalf("environment access flush interval = %s, want 125ms", got)
	}
}
