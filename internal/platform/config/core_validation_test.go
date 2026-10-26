package config

import (
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/shared/env"
)

func TestValidateCoreConfigBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input func(*Config)
		want  []string
	}{
		{name: "valid baseline"},
		{name: "database type empty", input: func(c *Config) { c.Database.DBType = "" }, want: []string{"database.dbtype"}},
		{name: "database type blank", input: func(c *Config) { c.Database.DBType = " \t\n" }, want: []string{"database.dbtype"}},
		{name: "database type unicode blank", input: func(c *Config) { c.Database.DBType = "\u2003" }, want: []string{"database.dbtype"}},
		{name: "postgres rejected", input: func(c *Config) { c.Database.DBType = "postgres" }, want: []string{"database.dbtype"}},
		{name: "mariadb rejected", input: func(c *Config) { c.Database.DBType = "mariadb" }, want: []string{"database.dbtype"}},
		{name: "uppercase database type rejected", input: func(c *Config) { c.Database.DBType = "MYSQL" }, want: []string{"database.dbtype"}},
		{name: "padded database type rejected", input: func(c *Config) { c.Database.DBType = " mysql " }, want: []string{"database.dbtype"}},
		{name: "debug level", input: func(c *Config) { c.Log.Level = "debug" }},
		{name: "info level", input: func(c *Config) { c.Log.Level = "info" }},
		{name: "warn level", input: func(c *Config) { c.Log.Level = "warn" }},
		{name: "error level", input: func(c *Config) { c.Log.Level = "error" }},
		{name: "dpanic level", input: func(c *Config) { c.Log.Level = "dpanic" }},
		{name: "panic level", input: func(c *Config) { c.Log.Level = "panic" }},
		{name: "fatal level", input: func(c *Config) { c.Log.Level = "fatal" }},
		{name: "padded log level preserved", input: func(c *Config) { c.Log.Level = " \tinfo\n" }},
		{name: "log level empty", input: func(c *Config) { c.Log.Level = "" }, want: []string{"log.level"}},
		{name: "log level spaces", input: func(c *Config) { c.Log.Level = "   " }, want: []string{"log.level"}},
		{name: "log level tabs", input: func(c *Config) { c.Log.Level = "\t\n" }, want: []string{"log.level"}},
		{name: "log level unicode blank", input: func(c *Config) { c.Log.Level = "\u2003" }, want: []string{"log.level"}},
		{name: "uppercase log level rejected", input: func(c *Config) { c.Log.Level = "INFO" }, want: []string{"log.level"}},
		{name: "warning is not warn", input: func(c *Config) { c.Log.Level = "warning" }, want: []string{"log.level"}},
		{name: "unknown log level", input: func(c *Config) { c.Log.Level = "verbose" }, want: []string{"log.level"}},
		{name: "log path empty", input: func(c *Config) { c.Log.Path = "" }, want: []string{"log.path"}},
		{name: "log path spaces", input: func(c *Config) { c.Log.Path = "   " }, want: []string{"log.path"}},
		{name: "log path tabs", input: func(c *Config) { c.Log.Path = "\t\n" }, want: []string{"log.path"}},
		{name: "log path unicode blank", input: func(c *Config) { c.Log.Path = "\u2003" }, want: []string{"log.path"}},
		{name: "padded log path preserved", input: func(c *Config) { c.Log.Path = " ./logs/app_client " }},
		{name: "zero lifetime retains default", input: func(c *Config) { c.Database.MaxLifeSeconds = 0 }},
		{name: "positive lifetime", input: func(c *Config) { c.Database.MaxLifeSeconds = 1 }},
		{name: "negative lifetime diagnosed once", input: func(c *Config) { c.Database.MaxLifeSeconds = -1 }, want: []string{"database.maxlifeseconds"}},
		{name: "minimum lifetime diagnosed once", input: func(c *Config) { c.Database.MaxLifeSeconds = -int(^uint(0)>>1) - 1 }, want: []string{"database.maxlifeseconds"}},
		{name: "default open limit still permits idle clamp", input: func(c *Config) { c.Database.MaxIdleConn = 100 }},
		{name: "default idle still permits small open limit", input: func(c *Config) { c.Database.MaxOpenConn = 2 }},
		{name: "explicit conflicting pool limits still rejected", input: func(c *Config) {
			c.Database.MaxOpenConn, c.Database.MaxIdleConn = 2, 3
		}, want: []string{"database.maxidleconn"}},
		{name: "redis db zero alone allowed", input: func(c *Config) { c.Redis.DBs = []int{0} }},
		{name: "redis has no fixed consumer", input: func(c *Config) { c.Redis.DBs = []int{3} }},
		{name: "aggregate all affected fields once", input: func(c *Config) {
			c.Database.DBType = "postgres"
			c.Database.MaxLifeSeconds = -1
			c.Log.Level = "verbose"
			c.Log.Path = "\t"
		}, want: []string{"database.dbtype", "database.maxlifeseconds", "log.level", "log.path"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfigForTest()
			if tt.input != nil {
				tt.input(cfg)
			}
			before := []string{cfg.Database.DBType, cfg.Log.Level, cfg.Log.Path}
			err := cfg.Validate()
			got := coreValidationFields(err)
			slices.Sort(got)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("Validate fields = %v, want %v; error: %v", got, tt.want, err)
			}
			after := []string{cfg.Database.DBType, cfg.Log.Level, cfg.Log.Path}
			if !slices.Equal(after, before) {
				t.Fatalf("Validate changed configuration: got %q, want %q", after, before)
			}
		})
	}
}

// coreValidationFields 只比较诊断的字段标识及次数，不绑定完整错误文案。
func coreValidationFields(err error) []string {
	if err == nil {
		return nil
	}
	if group, ok := errors.AsType[interface {
		error
		Unwrap() []error
	}](err); ok {
		var fields []string
		for _, cause := range group.Unwrap() {
			fields = append(fields, coreValidationFields(cause)...)
		}
		return fields
	}
	return []string{strings.Fields(err.Error())[0]}
}

func TestValidateDatabasePrepareStmtBounds(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		size int
		ttl  int
		want []string
	}{
		{name: "omitted"},
		{name: "minimum", size: 1, ttl: 1},
		{name: "maximum", size: 4096, ttl: 86400},
		{name: "negative size", size: -1, want: []string{"database.prepare_stmt_max_size"}},
		{name: "size above maximum", size: 4097, want: []string{"database.prepare_stmt_max_size"}},
		{name: "largest size", size: math.MaxInt, want: []string{"database.prepare_stmt_max_size"}},
		{name: "smallest size", size: math.MinInt, want: []string{"database.prepare_stmt_max_size"}},
		{name: "negative ttl", ttl: -1, want: []string{"database.prepare_stmt_ttl_seconds"}},
		{name: "ttl above maximum", ttl: 86401, want: []string{"database.prepare_stmt_ttl_seconds"}},
		{name: "largest ttl", ttl: math.MaxInt, want: []string{"database.prepare_stmt_ttl_seconds"}},
		{name: "smallest ttl", ttl: math.MinInt, want: []string{"database.prepare_stmt_ttl_seconds"}},
		{name: "both invalid", size: -1, ttl: 86401, want: []string{"database.prepare_stmt_max_size", "database.prepare_stmt_ttl_seconds"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfigForTest()
			cfg.Database.PrepareStmtMaxSize = tt.size
			cfg.Database.PrepareStmtTTLSeconds = tt.ttl
			before := cfg.Database
			err := cfg.Validate()
			got := coreValidationFields(err)
			slices.Sort(got)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("Validate fields = %v, want %v; error: %v", got, tt.want, err)
			}
			if cfg.Database != before {
				t.Fatal("validation changed database configuration")
			}
		})
	}
}

func TestLoadPrepareStmtCacheSettings(t *testing.T) {
	for _, tt := range []struct {
		name      string
		yaml      string
		env       map[string]string
		wantSize  int
		wantTTL   int
		wantField string
	}{
		{name: "omitted retains defaults", wantSize: 128, wantTTL: 3600},
		{name: "explicit zero retains defaults", yaml: "  prepare_stmt_max_size: 0\n  prepare_stmt_ttl_seconds: 0\n", wantSize: 128, wantTTL: 3600},
		{name: "minimum", yaml: "  prepare_stmt_max_size: 1\n  prepare_stmt_ttl_seconds: 1\n", wantSize: 1, wantTTL: 1},
		{name: "maximum", yaml: "  prepare_stmt_max_size: 4096\n  prepare_stmt_ttl_seconds: 86400\n", wantSize: 4096, wantTTL: 86400},
		{name: "environment overrides yaml", yaml: "  prepare_stmt_max_size: 256\n  prepare_stmt_ttl_seconds: 90\n",
			env: map[string]string{"APP_DATABASE__PREPARE_STMT_MAX_SIZE": "64", "APP_DATABASE__PREPARE_STMT_TTL_SECONDS": "120"}, wantSize: 64, wantTTL: 120},
		{name: "environment zero selects defaults", yaml: "  prepare_stmt_max_size: 256\n  prepare_stmt_ttl_seconds: 90\n",
			env: map[string]string{"APP_DATABASE__PREPARE_STMT_MAX_SIZE": "0", "APP_DATABASE__PREPARE_STMT_TTL_SECONDS": "0"}, wantSize: 128, wantTTL: 3600},
		{name: "yaml size rejected", yaml: "  prepare_stmt_max_size: -1\n", wantField: "database.prepare_stmt_max_size"},
		{name: "environment ttl rejected", env: map[string]string{"APP_DATABASE__PREPARE_STMT_TTL_SECONDS": "86401"}, wantField: "database.prepare_stmt_ttl_seconds"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			for name, value := range tt.env {
				t.Setenv(name, value)
			}
			yaml := strings.Replace(minimalConfigYAML(), "database:\n", "database:\n"+tt.yaml, 1)
			cfg, err := loadFromReader(strings.NewReader(yaml))
			if tt.wantField != "" {
				if cfg != nil || !slices.Equal(coreValidationFields(err), []string{tt.wantField}) {
					t.Fatalf("invalid cache configuration was not rejected for %s: %v", tt.wantField, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("load cache configuration: %v", err)
			}
			if got := cfg.Database.EffectivePrepareStmtMaxSize(); got != tt.wantSize {
				t.Errorf("cache size = %d, want %d", got, tt.wantSize)
			}
			if got := cfg.Database.EffectivePrepareStmtTTLSeconds(); got != tt.wantTTL {
				t.Errorf("cache ttl = %d, want %d", got, tt.wantTTL)
			}
		})
	}
}

func TestLogEnvironmentAndSlowThreshold(t *testing.T) {
	t.Parallel()
	for _, environment := range []env.Env{env.Dev, env.Test, env.Prod} {
		for _, level := range []string{"debug", "info", "warn", "error", "dpanic", "panic", "fatal", " debug "} {
			cfg := LogConfig{Level: level, Path: "./logs/app"}
			err := validateLogConfig(environment, cfg, "")
			rejected := environment == env.Prod && strings.TrimSpace(level) == "debug"
			if (err != nil) != rejected {
				t.Errorf("%s/%q err=%v", environment, level, err)
			}
		}
	}
	for _, tc := range []struct {
		value   *int64
		want    time.Duration
		invalid bool
	}{
		{nil, 200 * time.Millisecond, false}, {new(int64(0)), 0, false}, {new(int64(17)), 17 * time.Millisecond, false},
		{new(int64(-1)), 0, true}, {new(int64(math.MaxInt64)), 0, true},
	} {
		cfg := LogConfig{Level: "info", Path: "./logs/app", SQLSlowThresholdMS: tc.value}
		err := validateLogConfig(env.Dev, cfg, "")
		if (err != nil) != tc.invalid {
			t.Fatalf("threshold=%v err=%v", tc.value, err)
		}
		if !tc.invalid && cfg.EffectiveSQLSlowThreshold() != tc.want {
			t.Fatalf("threshold=%s want %s", cfg.EffectiveSQLSlowThreshold(), tc.want)
		}
	}
}
