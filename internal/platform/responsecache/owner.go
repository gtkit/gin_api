// Package responsecache owns the response store and borrows Redis when selected.
package responsecache

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/pkg/httpcache"
	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/logger/v2"
	"github.com/redis/go-redis/v9"
)

type Store interface {
	httpcache.ResponseCache
	Close() error
}

type Factory func(config.CacheConfig, *redis.Client, string) (Store, error)

type Options struct {
	Config  *config.Config
	Redis   data.RedisClientProvider
	Factory Factory
}

// Owner exposes middleware for explicit route mounting until Close releases it.
type Owner struct {
	mu        sync.RWMutex
	store     Store
	config    config.CacheConfig
	redis     data.RedisClientProvider
	factory   Factory
	prefix    string
	waiting   bool
	closed    bool
	initErr   error
	closeOnce sync.Once
	closeErr  error
}

func Open(ctx context.Context, options Options) (result *Owner, err error) {
	owner := new(Owner)
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("open response cache panic: %w: %v", core.ErrComponentState, value)
		}
		if err != nil {
			err = errors.Join(err, owner.Close(ctx))
			result = nil
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshot := options.Config
	if snapshot == nil {
		return nil, fmt.Errorf("cache configuration is required: %w", core.ErrComponentState)
	}
	if options.Factory == nil {
		options.Factory = func(c config.CacheConfig, r *redis.Client, prefix string) (Store, error) {
			store, err := httpcache.New(c, r, prefix)
			if store == nil {
				return nil, err
			}
			return store, err
		}
	}
	cfg := snapshot.Cache
	owner.config, owner.redis, owner.factory = cfg, options.Redis, options.Factory
	owner.prefix = snapshot.EffectiveRedisPrefix()
	var client *redis.Client
	if cfg.Enabled && strings.TrimSpace(cfg.Store) == config.CacheStoreRedis {
		if options.Redis == nil || !options.Redis.Configured() {
			return nil, errors.New("response cache requires configured Redis")
		}
		client = options.Redis.Client(cfg.RedisDB)
		if client == nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			owner.waiting = true
			logger.Warnf("response cache is waiting for Redis recovery (db %d)", cfg.RedisDB)
			return owner, nil
		}
	}
	store, openErr := buildStore(options.Factory, cfg, client, owner.prefix)
	owner.store = store
	if err := errors.Join(openErr, ctx.Err()); err != nil {
		return nil, fmt.Errorf("build response cache: %w", err)
	}
	return owner, nil
}

func buildStore(factory Factory, cfg config.CacheConfig, client *redis.Client, prefix string) (store Store, err error) {
	defer func() {
		if value := recover(); value != nil {
			err = errors.Join(err, fmt.Errorf("build response cache panic: %w: %v", core.ErrComponentState, value))
		}
	}()
	store, err = factory(cfg, client, prefix)
	if store != nil {
		value := reflect.ValueOf(store)
		switch value.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Chan, reflect.Func, reflect.Slice:
			if value.IsNil() {
				store = nil
			}
		}
	}
	if store == nil {
		err = errors.Join(err, fmt.Errorf("build cache: %w: nil store", core.ErrComponentState))
	}
	return store, err
}

func closeStore(store Store) (err error) {
	if store == nil {
		return nil
	}
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("close response cache panic: %w: %v", core.ErrComponentState, value)
		}
	}()
	return store.Close()
}

// resolveStoreLocked 在写锁内把首次恢复构造与 Close 串行化。调用方已在锁外
// 取得非 nil 的 Redis 客户端，但读取状态和取得写锁之间仍可能发生关闭，
// 或由另一请求先完成构造，因此必须再次检查。失败保持终态，不重复构造；
// 请求处理器始终在锁外执行。
func (p *Owner) resolveStoreLocked(client *redis.Client) {
	if p.closed || !p.waiting {
		return
	}
	p.waiting = false
	store, err := buildStore(p.factory, p.config, client, p.prefix)
	if err != nil {
		p.initErr = errors.Join(err, closeStore(store))
		logger.Errorf("response cache recovery initialization failed: %v", p.initErr)
		return
	}
	p.store = store
}

func (p *Owner) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		store := p.store
		p.store = nil
		p.mu.Unlock()
		p.closeErr = errors.Join(closeStore(store), p.initErr, ctx.Err())
	})
	return p.closeErr
}

// Enabled 读取当前可用状态；只有等待恢复且已取得客户端时才进入写锁。
// 稳定状态下在读锁内查询 Store，既允许并发读取，也保持与 Close 的互斥。
func (p *Owner) Enabled() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	if p.closed || !p.waiting {
		defer p.mu.RUnlock()
		return !p.closed && p.store != nil && p.store.Enabled()
	}
	p.mu.RUnlock()

	client := p.redis.Client(p.config.RedisDB)
	if client == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resolveStoreLocked(client)
	return !p.closed && p.store != nil && p.store.Enabled()
}

// Middleware 为每个挂载点保留一次构造的处理器。读锁只用于获取一致状态，
// 不覆盖实际请求处理；未恢复时继续透传，稳定运行时不再竞争 Owner 写锁。
// 恢复构造和该挂载点的首次处理器构造仍受同一写锁保护。
func (p *Owner) Middleware(ttl time.Duration) gin.HandlerFunc {
	if p == nil {
		return passthrough
	}
	var middleware gin.HandlerFunc
	return func(c *gin.Context) {
		handler := func() gin.HandlerFunc {
			p.mu.RLock()
			closed, waiting, hasStore := p.closed, p.waiting, p.store != nil
			current := middleware
			p.mu.RUnlock()
			if closed {
				return passthrough
			}
			if current != nil {
				return current
			}

			var client *redis.Client
			if !hasStore {
				if !waiting {
					return passthrough
				}
				client = p.redis.Client(p.config.RedisDB)
				if client == nil {
					return passthrough
				}
			}

			// 不能持读锁升级写锁。这里重新确认关闭/构造状态，也保护
			// 各请求共享的 middleware 闭包变量，避免首次请求重复创建。
			p.mu.Lock()
			defer p.mu.Unlock()
			if client != nil {
				p.resolveStoreLocked(client)
			}
			if p.closed || p.store == nil {
				return passthrough
			}
			if middleware == nil {
				middleware = p.store.Middleware(ttl)
			}
			return middleware
		}()
		handler(c)
	}
}
func passthrough(c *gin.Context) { c.Next() }
