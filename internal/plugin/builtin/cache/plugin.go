// Package cache owns the default response store, borrowing Redis when selected.
package cache

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
	"github.com/gtkit/gin-api/internal/plugin"
	"github.com/redis/go-redis/v9"
)

// Store is the owning factory result inside this default implementation.
type Store interface {
	plugin.ResponseCache
	Close() error
}

// Factory constructs a response store during Init, never during registration.
type Factory func(config.CacheConfig, *redis.Client, string) (Store, error)

// Options describes named dependencies. The Redis client is always borrowed.
type Options struct {
	Configuration plugin.ConfigurationProvider
	Redis         plugin.RedisClientProvider
	Factory       Factory
}

// Plugin owns one initialized store; consumers only see response-cache methods.
type Plugin struct {
	options              Options
	lifecycle            sync.Mutex
	initialized, stopped bool
	initErr, stopErr     error
	mu                   sync.RWMutex
	store                Store
}

var _ core.Plugin = (*Plugin)(nil)
var _ plugin.ResponseCache = (*Plugin)(nil)

// New creates no store and reads no configuration.
func New(options Options) *Plugin {
	if options.Factory == nil {
		options.Factory = func(c config.CacheConfig, r *redis.Client, prefix string) (Store, error) {
			store, err := httpcache.New(c, r, prefix)
			if store == nil {
				return nil, err
			}
			return store, err
		}
	}
	return &Plugin{options: options}
}
func (*Plugin) Name() string { return "cache" }
func (p *Plugin) Init(ctx context.Context) error {
	if p == nil {
		return errors.New("cache plugin is nil")
	}
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	if p.initialized {
		return p.initErr
	}
	p.initialized = true
	p.initErr = p.initialize(ctx)
	return p.initErr
}
func (p *Plugin) initialize(ctx context.Context) error {
	if p.stopped {
		return fmt.Errorf("init cache after stop: %w", core.ErrPluginContractViolation)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.options.Configuration == nil {
		return errors.New("cache configuration provider is required")
	}
	snapshot, err := p.options.Configuration.Value()
	if err != nil {
		return fmt.Errorf("read cache config: %w", err)
	}
	if snapshot == nil {
		return fmt.Errorf("read cache config: %w", core.ErrPluginContractViolation)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg := snapshot.Cache
	var client *redis.Client
	if cfg.Enabled && strings.TrimSpace(cfg.Store) == config.CacheStoreRedis && p.options.Redis != nil {
		client = p.options.Redis.Client(cfg.RedisDB)
		if client == nil && p.options.Redis.Configured() {
			applog.Warnf("response cache disabled for this process lifetime: redis (db %d) is configured but unreachable at startup; restart after redis recovers to re-enable it", cfg.RedisDB)
			cfg.Enabled = false
		}
	}
	store, err := p.options.Factory(cfg, client, snapshot.App.Name)
	if store != nil {
		v := reflect.ValueOf(store)
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Chan, reflect.Func, reflect.Slice:
			if v.IsNil() {
				store = nil
			}
		}
	}
	if store == nil && err == nil {
		err = fmt.Errorf("build cache: %w: nil store", core.ErrPluginContractViolation)
	}
	err = errors.Join(err, ctx.Err())
	if err != nil {
		if store != nil {
			p.stopped = true
			p.stopErr = store.Close()
		}
		return errors.Join(fmt.Errorf("build response cache: %w", err), p.stopErr)
	}
	p.mu.Lock()
	p.store = store
	p.mu.Unlock()
	return nil
}
func (p *Plugin) Start(ctx context.Context) error {
	if p == nil {
		return errors.New("start cache plugin: plugin is nil")
	}
	p.mu.RLock()
	ready := p.store != nil
	p.mu.RUnlock()
	if !ready {
		return fmt.Errorf("start cache: %w: store unavailable", core.ErrPluginContractViolation)
	}
	return ctx.Err()
}
func (p *Plugin) Stop(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	if !p.stopped {
		p.stopped = true
		p.mu.Lock()
		store := p.store
		p.store = nil
		p.mu.Unlock()
		if store != nil {
			p.stopErr = store.Close()
		}
		p.stopErr = errors.Join(p.stopErr, ctx.Err())
	}
	return p.stopErr
}
func (p *Plugin) Enabled() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.store != nil && p.store.Enabled()
}
func (p *Plugin) Middleware(ttl time.Duration) gin.HandlerFunc {
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
