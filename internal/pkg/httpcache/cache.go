// Package httpcache 提供 GET 响应缓存的装配器。
//
// 响应缓存是少数"开着比关着更容易出事"的能力：它把一次响应的**字节**原样发给
// 后续请求。一旦挂到带身份的路由上，就是把 A 的数据发给 B——而且不会报错、
// 不会有异常日志，只会在某天被用户发现。
//
// 因此本包的形状是：默认关闭；启用后也**只对显式挂了中间件的路由**生效；
// 挂上之后仍在每个请求上做一次身份判据，带身份的请求一律绕过缓存。
// 三道都是必要的——路由该不该缓存由模块决定，而人会看错。
package httpcache

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"

	"github.com/dgraph-io/ristretto/v2"
	"github.com/gin-gonic/gin"
	"github.com/gtkit/gincache"
	"github.com/gtkit/gincache/persist"
	"github.com/gtkit/gincache/persist/ristrettoadapter"
	"github.com/redis/go-redis/v9"
)

// Stager 按配置产出响应缓存中间件。
//
// 关闭时 Middleware 返回透传中间件而不是 nil：调用方无条件挂上即可，
// 不必在每个路由注册点写一次 if——那种 if 漏写一处就是行为不一致。
type Stager struct {
	enabled bool
	store   persist.CacheStore
	ttl     time.Duration
	maxBody int64
	// closer 释放本地 store 持有的后台 goroutine；redis 后端为 nil。
	closer func() error
}

// New 按配置构造装配器。
//
// client 为 redis 后端所需；memory 后端忽略它。
func New(cfg config.CacheConfig, client *redis.Client, keyPrefix string) (*Stager, error) {
	if !cfg.Enabled {
		return &Stager{}, nil
	}

	stager := &Stager{
		enabled: true,
		ttl:     cfg.EffectiveTTL(),
		maxBody: cfg.EffectiveMaxBodyBytes(),
	}

	switch strings.TrimSpace(cfg.Store) {
	case config.CacheStoreRedis:
		if client == nil {
			return nil, errors.New("httpcache: redis store requires a redis client")
		}
		// 前缀隔离：缓存键与业务键混在同一个 keyspace 里，一次 FLUSH 或一次
		// 键扫描就会互相波及。
		stager.store = persist.NewRedisStore(client, persist.WithKeyPrefix(keyPrefix+":httpcache:"))

	case config.CacheStoreMemory:
		if cfg.MaxBytes <= 0 {
			return nil, errors.New("httpcache: memory store requires a positive max_bytes")
		}
		// ristretto 而不是朴素 map：后者只按过期清理，键空间随请求变化时
		// 会一直涨。有界的准入/淘汰是本地缓存能安全上线的前提。
		//
		// NumCounters 取容量的 10 倍（ristretto 的经验值）：计数器用于
		// 估算访问频率，太少会让淘汰决策退化成随机。
		local, err := ristretto.NewCache(&ristretto.Config[string, []byte]{
			NumCounters: cfg.MaxBytes / 100,
			MaxCost:     cfg.MaxBytes,
			BufferItems: 64,
		})
		if err != nil {
			return nil, fmt.Errorf("httpcache: build local cache: %w", err)
		}
		adapter := ristrettoadapter.New(local,
			ristrettoadapter.WithDefaultExpiration(stager.ttl),
			// Store 拥有该实例，Close 时一并关闭它的后台 goroutine。
			ristrettoadapter.WithOwnedCache(),
			// 写入同步等待缓冲区落地。ristretto 默认异步 Set 是为吞吐服务的，
			// 但它让"写进去了没有"变成不确定：低 QPS 下相邻两次相同请求可能
			// 都不命中。一个有时缓存、有时不缓存的中间件，行为无法被推理，
			// 也无法被测试——而等待只发生在 miss 路径上，命中路径不受影响。
			ristrettoadapter.WithWait(),
		)
		stager.store = adapter
		stager.closer = adapter.Close

	default:
		return nil, fmt.Errorf("httpcache: unknown store %q", cfg.Store)
	}

	return stager, nil
}

// Middleware 返回挂在只读路由上的响应缓存中间件。
//
// ttl <= 0 时用配置的默认值。
func (s *Stager) Middleware(ttl time.Duration) gin.HandlerFunc {
	if s == nil || !s.enabled {
		return passthrough
	}
	if ttl <= 0 {
		ttl = s.ttl
	}

	// 必须用 Cache（走 Handler → 自定义策略），不能用 CacheByRequestURI：
	// 后者按 URI 直接组键，**根本不调用策略函数**——身份判据会被整个绕过，
	// 而配置看起来完全正常。这一点只在读它的实现时才看得出来。
	return gincache.Cache(s.store, ttl,
		// 只缓存 200：把 4xx/5xx 缓存下来，一次瞬时故障会被固定住 TTL 那么久，
		// 而故障恢复后用户仍在看错误页。
		gincache.WithCacheableStatusCodes(http.StatusOK),
		gincache.WithMaxBodySize(s.maxBody),
		gincache.WithCacheStrategyByRequest(cacheableRequest),
	)
}

// Close 释放本地 store 的后台 goroutine。
func (s *Stager) Close() error {
	if s == nil || s.closer == nil {
		return nil
	}
	return s.closer()
}

// Enabled 报告缓存是否启用（供装配期日志与 readiness 使用）。
func (s *Stager) Enabled() bool { return s != nil && s.enabled }

// passthrough 是关闭态的中间件：什么都不做。
func passthrough(c *gin.Context) { c.Next() }

// cacheableRequest 是每请求的安全判据。
//
// 这是本包最关键的一段：响应缓存的键里**没有身份**，因此任何带身份的请求
// 都不能进缓存——否则第一个用户的响应会被原样发给后面的所有人。
//
// 判据取"宁可不缓存"的方向：漏缓存的代价是一次多余的查询，误缓存的代价是
// 一次数据泄露。两者不对等，所以这里不做任何"聪明"的优化。
func cacheableRequest(c *gin.Context) (bool, gincache.Strategy) {
	if c.Request.Method != http.MethodGet {
		// 非 GET 有副作用，缓存它等于把一次写变成没发生。
		return false, gincache.Strategy{}
	}
	if c.Request.Header.Get("Authorization") != "" {
		return false, gincache.Strategy{}
	}
	if c.Request.Header.Get("Cookie") != "" {
		// Cookie 同样携带身份（会话 ID），且它常常是隐式带上的。
		return false, gincache.Strategy{}
	}
	if len(c.Request.Header.Values("Range")) > 0 {
		// 部分内容请求的响应是片段，缓存后会被当成完整响应发给别人。
		return false, gincache.Strategy{}
	}
	return true, gincache.Strategy{CacheKey: cacheKey(c.Request)}
}

// cacheKey 组成缓存键：方法 + 路径 + 规范化后的查询串。
//
// 方法进键是必要的：同一路径的 GET 与 HEAD 响应不同，共用一个键会让 HEAD
// 的空响应体被当成 GET 的结果发出去。
//
// 查询串按参数名排序：?a=1&b=2 与 ?b=2&a=1 是同一个请求，不排序会存成两份，
// 命中率白白减半。
func cacheKey(r *http.Request) string {
	query := r.URL.Query()
	if len(query) == 0 {
		return r.Method + " " + r.URL.Path
	}
	// url.Values.Encode 内部已按键排序，同名多值保持出现顺序。
	return r.Method + " " + r.URL.Path + "?" + query.Encode()
}
