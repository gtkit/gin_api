package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/allegro/bigcache/v3"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

type TwoLevelCache struct {
	local *bigcache.BigCache
	redis *redis.Client
	// 防止同一个 key 被并发打穿，用 singleflight 合并请求
	sf singleflight.Group
}

func NewTwoLevelCache(localcache *bigcache.BigCache, redis *redis.Client) *TwoLevelCache {
	//cache, _ := bigcache.New(context.Background(), bigcache.DefaultConfig(10*time.Minute))
	return &TwoLevelCache{
		local: localcache,
		redis: redis,
	}
}

// Get 是典型的 cache-aside 模式：本地 -> Redis -> loader.
func (c *TwoLevelCache) Get(
	ctx context.Context,
	key string,
	redisTTL time.Duration,
	loader func(ctx context.Context, key string) ([]byte, error),
) ([]byte, error) {
	// 1. 先查本地缓存
	if v, err := c.local.Get(key); err == nil {
		return v, nil
	}

	// 2. 查 Redis
	val, err := c.redis.Get(ctx, key).Bytes()
	if err == nil {
		// 命中 Redis，回填本地缓存（注意本地 TTL 可以更短）
		if err := c.local.Set(key, val); err == nil {
			return val, nil
		}
	}
	if err != redis.Nil {
		// 真异常，先别打 DB 了，直接返回错误
		return nil, err
	}

	// 3. Redis 也没命中，防止同一 key 被 N 个协程同时打 DB，用 singleflight
	v, err, _ := c.sf.Do(key, func() (any, error) {
		// double-check 本地缓存，防止前面的请求已经填过了
		if vv, err := c.local.Get(key); err == nil {
			return vv, nil
		}
		// 真打 DB 或 RPC，下层 loader 自己实现
		data, e := loader(ctx, key)
		if e != nil {
			return "", e
		}
		// 回填 Redis + 本地缓存
		_ = c.redis.Set(ctx, key, data, redisTTL).Err()

		_ = c.local.Set(key, data)

		return data, nil
	})
	if err != nil {
		return nil, err
	}
	// 4. 返回结果
	data, ok := v.([]byte)
	if !ok {
		return nil, fmt.Errorf("cache value for key %q has type %T, want []byte", key, v)
	}

	return data, nil
}
