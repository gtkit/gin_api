package bootstrap

import (
	"testing"

	"github.com/gtkit/gin-api/internal/platform/config"
)

// TestRedisRequiredForLocalAuth 钉住认证对 Redis 必需性的贡献
// （local-authentication spec）：刷新令牌存储、撤销表与单实例租约全在 Redis 上，
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
			if got := redisRequired(tt.cfg); got != tt.want {
				t.Fatalf("redisRequired = %v, want %v", got, tt.want)
			}
		})
	}
}
