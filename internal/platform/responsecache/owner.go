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

// resolveStoreLocked serializes the single late construction with Close. No
// handler is executed under this lock, and terminal failures are never retried.
func (p *Owner) resolveStoreLocked() {
	if p.closed || !p.waiting {
		return
	}
	client := p.redis.Client(p.config.RedisDB)
	if client == nil {
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

func (p *Owner) Enabled() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resolveStoreLocked()
	return !p.closed && p.store != nil && p.store.Enabled()
}
func (p *Owner) Middleware(ttl time.Duration) gin.HandlerFunc {
	if p == nil {
		return passthrough
	}
	var middleware gin.HandlerFunc
	return func(c *gin.Context) {
		handler := func() gin.HandlerFunc {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.resolveStoreLocked()
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
