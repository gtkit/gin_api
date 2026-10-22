package config

import (
	"strings"
	"testing"
)

// TestValidateRedisConfig 覆盖 Redis 配置的必填项、取值范围与**交叉**约束。
//
// 交叉约束是重点：min_idle_conns > pool_size 时每个字段单看都合法，
// 组合起来却让连接池永远补不到空闲目标，表现为持续的建连尝试——
// 一个只在压测或高峰期才显形的配置错误。它在启动时是零成本可判的。
func TestValidateRedisConfig(t *testing.T) {
	t.Parallel()

	valid := RedisConfig{Addr: "127.0.0.1:6379", DBs: []int{0, 1}}

	cases := []struct {
		name     string
		mutate   func(*RedisConfig)
		wantErr  bool
		wantHint string
	}{
		{name: "valid minimal config", mutate: func(*RedisConfig) {}},
		{
			name:   "addr may be omitted without consumers",
			mutate: func(c *RedisConfig) { c.Addr = "" },
		},
		{
			name:     "dbs required",
			mutate:   func(c *RedisConfig) { c.DBs = nil },
			wantErr:  true,
			wantHint: "redis.dbs",
		},
		{
			name:     "duplicate dbs rejected",
			mutate:   func(c *RedisConfig) { c.DBs = []int{0, 1, 0} },
			wantErr:  true,
			wantHint: "duplicate",
		},
		{
			name:     "negative db rejected",
			mutate:   func(c *RedisConfig) { c.DBs = []int{0, -1} },
			wantErr:  true,
			wantHint: "negative database number",
		},
		{
			name:   "pool settings unset is valid",
			mutate: func(c *RedisConfig) { c.PoolSize, c.MinIdleConns = 0, 0 },
		},
		{
			name:   "consistent pool settings accepted",
			mutate: func(c *RedisConfig) { c.PoolSize, c.MinIdleConns = 20, 5 },
		},
		{
			name:     "min idle above pool size rejected",
			mutate:   func(c *RedisConfig) { c.PoolSize, c.MinIdleConns = 10, 20 },
			wantErr:  true,
			wantHint: "redis.min_idle_conns",
		},
		{
			name:     "negative pool size rejected",
			mutate:   func(c *RedisConfig) { c.PoolSize = -1 },
			wantErr:  true,
			wantHint: "redis.pool_size",
		},
		{
			name:     "negative min idle rejected",
			mutate:   func(c *RedisConfig) { c.MinIdleConns = -1 },
			wantErr:  true,
			wantHint: "redis.min_idle_conns",
		},
		{
			name:     "negative dial timeout rejected",
			mutate:   func(c *RedisConfig) { c.DialTimeoutMS = -1 },
			wantErr:  true,
			wantHint: "redis.dial_timeout_ms",
		},
		{
			name:     "negative read timeout rejected",
			mutate:   func(c *RedisConfig) { c.ReadTimeoutMS = -1 },
			wantErr:  true,
			wantHint: "redis.read_timeout_ms",
		},
		{
			name:     "negative write timeout rejected",
			mutate:   func(c *RedisConfig) { c.WriteTimeoutMS = -1 },
			wantErr:  true,
			wantHint: "redis.write_timeout_ms",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := valid
			cfg.DBs = append([]int(nil), valid.DBs...)
			tc.mutate(&cfg)

			err := validateRedisConfig(cfg)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("validateRedisConfig() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("validateRedisConfig() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tc.wantHint) {
				t.Fatalf("error %q does not mention %q", err, tc.wantHint)
			}
		})
	}
}

func TestHasDuplicateInts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input []int
		want  bool
	}{
		{name: "empty", input: nil, want: false},
		{name: "unique", input: []int{0, 1, 2}, want: false},
		{name: "duplicate at end", input: []int{0, 1, 0}, want: true},
		{name: "all same", input: []int{3, 3}, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := hasDuplicateInts(tc.input); got != tc.want {
				t.Fatalf("hasDuplicateInts(%v) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// TestDeprecatedWriteTimeoutIsRejected 钉住旧配置键的 fail-fast。
//
// 该键曾经设置 http.Server.WriteTimeout——一个所有路由共享的绝对写上限。
// 现在写超时由每条路由在连接级设置。若保留这个键并静默忽略，
// 使用者会以为自己配置了保护而实际没有；因此必须拒绝启动，
// 并且错误里要给出迁移方向（严格解码的"未知键"不会告诉任何人该往哪改）。
func TestDeprecatedWriteTimeoutIsRejected(t *testing.T) {
	t.Parallel()

	app := AppConfig{Host: "127.0.0.1", Port: "8008", Name: "app"}

	if err := validateAppConfig(app); err != nil {
		t.Fatalf("validateAppConfig without the deprecated key: %v", err)
	}

	app.WriteTimeoutSeconds = 1
	err := validateAppConfig(app)
	if err == nil {
		t.Fatal("validateAppConfig accepted the deprecated application.writertimeout key")
	}
	// 断言指向**实际存在**的机制。此前这里断言 "TimeoutPolicy"——那是随路由 DSL
	// 一并移除的机制名，照它去找会一无所获，而这条文案出现在启动失败的那一刻，
	// 是使用者最需要它准确的时候。
	for _, want := range []string{
		"application.writertimeout", "no longer supported", "conservative fallback", "route timeout middleware",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestEffectiveRedisPrefixPreservesNamespaceBytes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, prefix, want string }{
		{"legacy", "", "legacy"}, {" legacy:", "", " legacy:"},
		{"legacy", "explicit", "explicit"}, {"legacy", " raw: ", " raw: "},
	} {
		cfg := Config{}
		cfg.App.Name = tc.name
		cfg.Redis.Prefix = tc.prefix
		if got := cfg.EffectiveRedisPrefix(); got != tc.want {
			t.Errorf("prefix=%q want %q", got, tc.want)
		}
	}
	if err := validateRedisConfig(RedisConfig{Prefix: " \t"}); err == nil || !strings.Contains(err.Error(), "redis.prefix") {
		t.Fatalf("whitespace accepted: %v", err)
	}
}
