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
			name:     "addr required",
			mutate:   func(c *RedisConfig) { c.Addr = "" },
			wantErr:  true,
			wantHint: "redis.addr",
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
