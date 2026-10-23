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
	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
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
	var client *redis.Client
	if cfg.Enabled && strings.TrimSpace(cfg.Store) == config.CacheStoreRedis && options.Redis != nil {
		client = options.Redis.Client(cfg.RedisDB)
		if client == nil && options.Redis.Configured() {
			applog.Warnf("response cache disabled for this process lifetime: redis (db %d) is configured but unreachable at startup; restart after redis recovers to re-enable it", cfg.RedisDB)
			cfg.Enabled = false
		}
	}
	store, openErr := options.Factory(cfg, client, snapshot.EffectiveRedisPrefix())
	if store != nil {
		value := reflect.ValueOf(store)
		switch value.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Chan, reflect.Func, reflect.Slice:
			if value.IsNil() {
				store = nil
			}
		}
	}
	owner.store = store
	if store == nil && openErr == nil {
		openErr = fmt.Errorf("build cache: %w: nil store", core.ErrComponentState)
	}
	if err := errors.Join(openErr, ctx.Err()); err != nil {
		return nil, fmt.Errorf("build response cache: %w", err)
	}
	return owner, nil
}

func (p *Owner) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		p.mu.Lock()
		store := p.store
		p.store = nil
		p.mu.Unlock()
		if store != nil {
			p.closeErr = store.Close()
		}
		p.closeErr = errors.Join(p.closeErr, ctx.Err())
	})
	return p.closeErr
}

func (p *Owner) Enabled() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.store != nil && p.store.Enabled()
}
func (p *Owner) Middleware(ttl time.Duration) gin.HandlerFunc {
	if p == nil {
		return passthrough
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.store == nil {
		return passthrough
	}
	return p.store.Middleware(ttl)
}
func passthrough(c *gin.Context) { c.Next() }
