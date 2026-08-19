package config

import (
	"strings"
	"testing"
)

// TestValidateCacheConfigRedisDB 钉住 cache.redis_db 的声明校验：
// 资源层只打开 redis.dbs 里声明过的库，未声明的库号必须在校验期失败
// 并指明真实原因，而不是让装配报出"requires a redis client"这种误导错误。
func TestValidateCacheConfigRedisDB(t *testing.T) {
	t.Parallel()

	redis := RedisConfig{Addr: "127.0.0.1:6379", DBs: []int{0, 1}}
	tests := []struct {
		name    string
		cfg     CacheConfig
		wantErr string // 空串表示要求通过
	}{
		{
			name: "declared db passes",
			cfg:  CacheConfig{Enabled: true, Store: CacheStoreRedis, RedisDB: 1},
		},
		{
			name: "zero value db 0 passes when declared",
			cfg:  CacheConfig{Enabled: true, Store: CacheStoreRedis},
		},
		{
			name:    "undeclared db fails naming the real cause",
			cfg:     CacheConfig{Enabled: true, Store: CacheStoreRedis, RedisDB: 3},
			wantErr: "cache.redis_db 3 is not listed in redis.dbs",
		},
		{
			name:    "negative db fails",
			cfg:     CacheConfig{Enabled: true, Store: CacheStoreRedis, RedisDB: -1},
			wantErr: "must not be negative",
		},
		{
			name: "memory store ignores redis_db",
			cfg:  CacheConfig{Enabled: true, Store: CacheStoreMemory, MaxBytes: 1 << 20, RedisDB: 9},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateCacheConfig(tt.cfg, redis)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateCacheConfig() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateCacheConfig() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}
