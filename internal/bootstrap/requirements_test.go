package bootstrap

import (
	"testing"

	"github.com/gtkit/gin-api/internal/platform/config"
)

// TestRedisRequiredForLocalAuth 钉住认证对 Redis 必需性的贡献
// （local-authentication spec）：刷新令牌存储与撤销表都在 Redis 上，
// 本地认证启用时 Redis 挂掉即认证不可用——readiness 不得再报 200 degraded。
func TestRedisRequiredForLocalAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{name: "nil 配置为 optional", cfg: nil, want: false},
		{
			name: "本地认证启用即 required",
			cfg:  &config.Config{Auth: config.AuthConfig{Mode: config.AuthModeLocal}},
			want: true,
		},
		{
			name: "外部身份模式不因认证抬高必需性",
			cfg:  &config.Config{Auth: config.AuthConfig{Mode: config.AuthModeExternal}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := RedisRequired(tt.cfg); got != tt.want {
				t.Fatalf("redisRequired = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRedisRequiredForTaskQueue 钉住任务队列对 Redis 必需性的贡献。
//
// 漏掉这一行的后果不是"Redis 挂了任务不跑"，而是**进程起不来且错误指错方向**：
// 推导为 optional → Redis 已配置但启动期不可达时 res.redis 保持 nil →
// Rdb() 返回 nil → tasks.NewProducer 报 "producer requires a redis client"
// → 启动失败。运维看到的那句话读起来像配置缺失，而真实原因（Redis 不可达）
// 不出现在错误里任何位置。
//
// 这条判据同时覆盖调度器：它向 Redis 队列投递，且以 tasks.enabled 为前提。
func TestRedisRequiredForTaskQueue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{
			name: "队列启用即 required",
			cfg:  &config.Config{Tasks: config.TasksConfig{Enabled: true}},
			want: true,
		},
		{
			name: "队列启用且开了调度器仍为 required",
			cfg: &config.Config{Tasks: config.TasksConfig{
				Enabled:   true,
				Scheduler: config.SchedulerConfig{Enabled: true},
			}},
			want: true,
		},
		{
			// 反向锚：缺了它，一个无条件返回 true 的实现也能让上面两条通过，
			// 而那会让不用队列的部署因为一个用不到的依赖而起不来。
			name: "队列关闭不抬高必需性",
			cfg:  &config.Config{Tasks: config.TasksConfig{Enabled: false}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := RedisRequired(tt.cfg); got != tt.want {
				t.Fatalf("redisRequired = %v, want %v", got, tt.want)
			}
		})
	}
}
