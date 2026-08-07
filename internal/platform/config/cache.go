package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// CacheConfig 是 GET 响应缓存的进程级配置。
//
// 默认关闭。响应缓存是少数"开着比关着更容易出事"的能力：它把一次响应的
// 字节原样发给后续请求，一旦挂到带身份的路由上，就是把 A 的数据发给 B。
// 因此母版不替使用者打开它，也不提供"全局开启"这条路径。
type CacheConfig struct {
	// Enabled 是总开关。关闭时不构造任何 store，装配器返回透传中间件。
	Enabled bool `mapstructure:"enabled"`
	// Store 是后端：redis（跨副本共享）或 memory（单副本本地）。
	Store string `mapstructure:"store"`
	// TTL 是默认缓存时长；路由可各自覆盖。
	TTL time.Duration `mapstructure:"ttl"`
	// MaxBodyBytes 是可缓存响应体的上限，超过即不缓存。
	MaxBodyBytes int64 `mapstructure:"max_body_bytes"`
	// MaxBytes 仅 memory 后端使用：本地缓存的总容量上限（字节）。
	//
	// 必填且必须有界。无界的本地缓存在键空间随请求变化时会一直涨——
	// 那不是"缓存命中率低"，是一次必然发生的 OOM。
	MaxBytes int64 `mapstructure:"max_bytes"`
}

// 缓存后端取值。
const (
	CacheStoreRedis  = "redis"
	CacheStoreMemory = "memory"
)

// 未配置时的兜底值。
//
// TTL 取 30s：响应缓存的价值集中在"同一份热点数据在短时间内被反复请求"，
// 而更长的 TTL 会把"数据已经变了"的窗口拉长到用户能察觉的程度。
//
// 响应体上限取 1 MiB：缓存大响应挤占的是给热点小响应的容量，命中率反而更差。
const (
	cacheDefaultTTL          = 30 * time.Second
	cacheDefaultMaxBodyBytes = 1 << 20
)

// maxCacheTTL 是允许配置的 TTL 上限。
//
// 超过它的响应缓存已经不是缓存而是一份副本：数据源变更后，用户会在长达
// 数小时里看到旧值，而没有任何一处会提示这件事正在发生。
const maxCacheTTL = 10 * time.Minute

// EffectiveTTL 返回生效的默认缓存时长。
func (c CacheConfig) EffectiveTTL() time.Duration {
	if c.TTL > 0 {
		return c.TTL
	}
	return cacheDefaultTTL
}

// EffectiveMaxBodyBytes 返回生效的响应体上限。
func (c CacheConfig) EffectiveMaxBodyBytes() int64 {
	if c.MaxBodyBytes > 0 {
		return c.MaxBodyBytes
	}
	return cacheDefaultMaxBodyBytes
}

// validateCacheConfig 校验响应缓存配置。
//
// 关闭时不校验：母版默认不启用，强制填一组用不到的值只会被随便填。
func validateCacheConfig(cfg CacheConfig, redis RedisConfig) error {
	if !cfg.Enabled {
		return nil
	}

	var errs []error

	switch strings.TrimSpace(cfg.Store) {
	case CacheStoreRedis:
		// redis 后端要求 Redis 已配置：否则启用了缓存却没有后端，
		// 表现为"缓存开着但一次也没命中"，而没有任何一处会报错。
		if strings.TrimSpace(redis.Addr) == "" {
			errs = append(errs, errors.New(
				"cache.store is redis but redis.addr is empty: the cache would silently never hit"))
		}
	case CacheStoreMemory:
		// memory 后端必须有容量上限。无界的本地缓存在键空间随请求变化时
		// 会一直涨，那不是命中率问题，是一次必然发生的 OOM。
		if cfg.MaxBytes <= 0 {
			errs = append(errs, errors.New(
				"cache.max_bytes must be positive for the memory store: an unbounded local cache "+
					"grows with the key space and eventually takes the process down"))
		}
	case "":
		errs = append(errs, fmt.Errorf(
			"cache.store is required when cache is enabled (%q or %q)", CacheStoreRedis, CacheStoreMemory))
	default:
		errs = append(errs, fmt.Errorf(
			"unknown cache.store %q (want %q or %q)", cfg.Store, CacheStoreRedis, CacheStoreMemory))
	}

	if cfg.EffectiveTTL() > maxCacheTTL {
		errs = append(errs, fmt.Errorf(
			"cache.ttl %s exceeds the %s cap: beyond that it is not a cache but a stale copy, and "+
				"nothing surfaces that users are seeing outdated data", cfg.EffectiveTTL(), maxCacheTTL))
	}

	return errors.Join(errs...)
}
