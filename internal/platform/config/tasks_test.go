package config

import (
	"strings"
	"testing"
	"time"
)

// TestValidateTasksConfig 钉住任务队列配置校验。
//
// 重点是权重为 0 的队列：它接收任务却永远不会被取，入队成功、永不处理，
// 而没有任何一处会提示这件事。
func TestValidateTasksConfig(t *testing.T) {
	t.Parallel()

	redis := RedisConfig{Addr: "127.0.0.1:6379"}

	tests := []struct {
		name    string
		cfg     TasksConfig
		redis   RedisConfig
		wantErr string
	}{
		{name: "关闭即不校验", cfg: TasksConfig{Enabled: false}},
		{name: "合法", cfg: TasksConfig{Enabled: true}, redis: redis},
		{
			name:    "启用但没有 redis",
			cfg:     TasksConfig{Enabled: true},
			wantErr: "no backing store",
		},
		{
			name:    "队列权重为零",
			cfg:     TasksConfig{Enabled: true, Queues: map[string]int{"low": 0}},
			redis:   redis,
			wantErr: "never processes them",
		},
		{
			name:    "队列名为空",
			cfg:     TasksConfig{Enabled: true, Queues: map[string]int{" ": 1}},
			redis:   redis,
			wantErr: "must not be empty",
		},
		{
			name:    "负数 redis_db",
			cfg:     TasksConfig{Enabled: true, RedisDB: -1},
			redis:   redis,
			wantErr: "must not be negative",
		},
		{
			name:    "关停预算超上限",
			cfg:     TasksConfig{Enabled: true, ShutdownTimeout: time.Hour},
			redis:   redis,
			wantErr: "exceeds the",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateTasksConfig(tt.cfg, tt.redis)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateTasksConfig() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateTasksConfig() = nil, want an error containing %q", tt.wantErr)
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

	empty := TasksConfig{}
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

	set := TasksConfig{Concurrency: 4, TaskTimeout: time.Minute, ShutdownTimeout: time.Second,
		Queues: map[string]int{"high": 3}}
	if got := set.EffectiveConcurrency(); got != 4 {
		t.Fatalf("EffectiveConcurrency() = %d, want 4", got)
	}
	if got := set.EffectiveQueues(); got["high"] != 3 {
		t.Fatalf("EffectiveQueues() = %v, want {high:3}", got)
	}
}
