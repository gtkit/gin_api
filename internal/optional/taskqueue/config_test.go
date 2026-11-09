package taskqueue

import (
	"strings"
	"testing"
	"time"
)

type redisConfig struct {
	Addr string
	DBs  []int
}

// TestValidateConfig 覆盖非法队列、预算和 Redis 依赖配置。
func TestValidateConfig(t *testing.T) {
	t.Parallel()

	redis := redisConfig{Addr: "127.0.0.1:6379", DBs: []int{0, 1}}

	tests := []struct {
		name    string
		cfg     Config
		redis   redisConfig
		wantErr string
	}{
		{name: "关闭即不校验", cfg: Config{Enabled: false}},
		{name: "合法", cfg: Config{Enabled: true}, redis: redis},
		{
			name:    "负并发",
			cfg:     Config{Enabled: true, Concurrency: -1},
			redis:   redis,
			wantErr: "negative concurrency",
		},
		{
			name:    "启用但没有 redis",
			cfg:     Config{Enabled: true},
			wantErr: "no backing store",
		},
		{
			name:    "队列权重为零",
			cfg:     Config{Enabled: true, Queues: map[string]int{"low": 0}},
			redis:   redis,
			wantErr: "never processes them",
		},
		{
			name:    "队列名为空",
			cfg:     Config{Enabled: true, Queues: map[string]int{" ": 1}},
			redis:   redis,
			wantErr: "must not be empty",
		},
		{
			name:    "负数 redis_db",
			cfg:     Config{Enabled: true, RedisDB: -1},
			redis:   redis,
			wantErr: "must not be negative",
		},
		{
			// 未在 redis.dbs 里声明的库拿不到客户端，装配会按"redis 未配置"
			// 处理——表现为"配了队列却说没有 redis"，而配置看起来完全正常。
			name:    "redis_db 未在 dbs 中声明",
			cfg:     Config{Enabled: true, RedisDB: 7},
			redis:   redis,
			wantErr: "not listed in redis.dbs",
		},
		{
			name:  "使用已声明的非默认库",
			cfg:   Config{Enabled: true, RedisDB: 1},
			redis: redis,
		},
		{
			name:    "关停预算超上限",
			cfg:     Config{Enabled: true, ShutdownTimeout: time.Hour},
			redis:   redis,
			wantErr: "exceeds the",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate(tt.redis.Addr, tt.redis.DBs)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateConfig() = nil, want an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestTasksEffectiveValues 钉住兜底值，尤其是关停预算必须小于容器宽限期。
func TestTasksEffectiveValues(t *testing.T) {
	t.Parallel()

	empty := Config{}
	if got := empty.EffectiveConcurrency(); got != tasksDefaultConcurrency {
		t.Fatalf("EffectiveConcurrency() = %d, want %d", got, tasksDefaultConcurrency)
	}
	if got := empty.EffectiveTaskTimeout(); got != tasksDefaultTaskTimeout {
		t.Fatalf("EffectiveTaskTimeout() = %s, want %s", got, tasksDefaultTaskTimeout)
	}
	// 必须小于常见的容器 SIGKILL 宽限期（30s），否则 drain 会被直接杀掉，
	// 正在处理的任务既没完成也没重新入队。
	if got := empty.EffectiveShutdownTimeout(); got >= 30*time.Second {
		t.Fatalf("默认关停预算 %s 不小于 30s 的容器宽限期", got)
	}
	if got := empty.EffectiveQueues(); len(got) != 1 || got["default"] != 1 {
		t.Fatalf("EffectiveQueues() = %v, want {default:1}", got)
	}

	set := Config{Concurrency: 4, TaskTimeout: time.Minute, ShutdownTimeout: time.Second,
		Queues: map[string]int{"high": 3}}
	if got := set.EffectiveConcurrency(); got != 4 {
		t.Fatalf("EffectiveConcurrency() = %d, want 4", got)
	}
	if got := set.EffectiveQueues(); got["high"] != 3 {
		t.Fatalf("EffectiveQueues() = %v, want {high:3}", got)
	}
}
