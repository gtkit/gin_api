package bootstrap

import (
	"testing"

	"github.com/gtkit/gin-api/internal/platform/config"
)

// TestResponseCacheShouldDegrade 钉住响应缓存的启动降级判定：
// 只有「redis 后端 + 客户端缺失 + Redis 已配置（即配置了但启动期不可达）」
// 这一种组合降级；其余组合都按原路径处理（禁用直通、装配错误如实暴露）。
func TestResponseCacheShouldDegrade(t *testing.T) {
	t.Parallel()

	redisOn := config.CacheConfig{Enabled: true, Store: config.CacheStoreRedis}
	tests := []struct {
		name            string
		cfg             config.CacheConfig
		clientMissing   bool
		redisConfigured bool
		want            bool
	}{
		{name: "configured but unreachable degrades", cfg: redisOn, clientMissing: true, redisConfigured: true, want: true},
		{name: "client present never degrades", cfg: redisOn, clientMissing: false, redisConfigured: true, want: false},
		// Redis 未配置：属配置矛盾，配置校验期已拦截；这里不降级，
		// 让任何漏网的装配错误如实暴露而不是被静默吞掉。
		{name: "redis unconfigured does not degrade", cfg: redisOn, clientMissing: true, redisConfigured: false, want: false},
		{name: "cache disabled does not degrade", cfg: config.CacheConfig{Store: config.CacheStoreRedis}, clientMissing: true, redisConfigured: true, want: false},
		{name: "memory store does not degrade", cfg: config.CacheConfig{Enabled: true, Store: config.CacheStoreMemory}, clientMissing: true, redisConfigured: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := responseCacheShouldDegrade(tt.cfg, tt.clientMissing, tt.redisConfigured); got != tt.want {
				t.Fatalf("responseCacheShouldDegrade() = %t, want %t", got, tt.want)
			}
		})
	}
}
