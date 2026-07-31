// Package cache 提供多级缓存降级能力。
package cache

import (
	"context"
	"time"

	"github.com/allegro/bigcache/v3"
	logger "github.com/gtkit/logger/v2"
	"go.uber.org/zap"
)

// FallbackCache 在主数据源失败时从本地内存兜底。
type FallbackCache struct {
	local *bigcache.BigCache
	name  string
}

// NewFallbackCache 创建本地内存兜底缓存。
//
//   - ttl: 兜底数据的最大存活时间，超过后自动淘汰。
//     建议设置为可接受的数据陈旧上限（如 5-10 分钟）。
//   - maxMB: 最大内存占用（MB）。
func NewFallbackCache(name string, ttl time.Duration, maxMB int) (*FallbackCache, error) {
	cfg := bigcache.DefaultConfig(ttl)
	cfg.HardMaxCacheSize = maxMB
	cfg.Verbose = false

	local, err := bigcache.New(context.Background(), cfg)
	if err != nil {
		return nil, err
	}

	return &FallbackCache{local: local, name: name}, nil
}

// Get 从本地缓存中获取数据。
func (c *FallbackCache) Get(key string) ([]byte, bool) {
	data, err := c.local.Get(key)
	if err != nil {
		return nil, false
	}
	return data, true
}

// Set 向本地缓存写入数据。
func (c *FallbackCache) Set(key string, data []byte) {
	if err := c.local.Set(key, data); err != nil {
		logger.Warn("fallback cache set failed",
			zap.String("cache", c.name),
			zap.String("key", key),
			zap.Error(err),
		)
	}
}

// Name 返回缓存实例名称。
func (c *FallbackCache) Name() string {
	return c.name
}
