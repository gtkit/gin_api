package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/pkg/httpcache"
	"github.com/gtkit/gin-api/internal/platform/config"
	logger "github.com/gtkit/logger/v2"
	"github.com/redis/go-redis/v9"
)

// CacheOptions configures a response-cache plugin.
type CacheOptions struct {
	Config  config.CacheConfig
	Redis   RedisClientProvider
	AppName string
}

// CachePlugin is the default response-cache lifecycle adapter.
type CachePlugin struct {
	config  config.CacheConfig
	redis   RedisClientProvider
	appName string

	mu     sync.RWMutex
	stager *httpcache.Stager

	stopOnce sync.Once
	stopErr  error
}

var _ core.Plugin = (*CachePlugin)(nil)
var _ CacheLifecyclePlugin = (*CachePlugin)(nil)

// NewCachePlugin creates a cache plugin. Construction does not allocate a
// store; Init owns that side effect so the registry can roll it back.
func NewCachePlugin(options CacheOptions) *CachePlugin {
	return &CachePlugin{
		config:  options.Config,
		redis:   options.Redis,
		appName: options.AppName,
	}
}

// Name returns the stable registry name.
func (p *CachePlugin) Name() string { return "cache" }

// Init constructs the cache stager and preserves the existing Redis optional
// degradation rule.
func (p *CachePlugin) Init(ctx context.Context) error {
	if p == nil {
		return errors.New("cache plugin is nil")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("init cache plugin: %w", err)
	}

	cfg := p.config
	var client *redis.Client
	if p.redis != nil {
		client = p.redis.Client(cfg.RedisDB)
	}
	if cfg.Enabled && strings.TrimSpace(cfg.Store) == config.CacheStoreRedis &&
		client == nil && p.redis != nil && p.redis.Configured() {
		logger.Warnf("response cache disabled for this process lifetime: redis (db %d) is configured but unreachable at startup; restart after redis recovers to re-enable it", cfg.RedisDB)
		cfg.Enabled = false
	}

	stager, err := httpcache.New(cfg, client, p.appName)
	if err != nil {
		return fmt.Errorf("build response cache plugin: %w", err)
	}
	p.mu.Lock()
	p.stager = stager
	p.mu.Unlock()
	return nil
}

// Start verifies that Init produced a stager.
func (p *CachePlugin) Start(context.Context) error {
	if p == nil {
		return errors.New("start cache plugin: plugin is nil")
	}
	if p.Stager() == nil {
		return errors.New("start cache plugin: stager is not initialized")
	}
	return nil
}

// Stop closes the cache store exactly once.
func (p *CachePlugin) Stop(context.Context) error {
	if p == nil {
		return nil
	}
	p.stopOnce.Do(func() {
		p.mu.Lock()
		stager := p.stager
		p.stager = nil
		p.mu.Unlock()
		if stager != nil {
			p.stopErr = stager.Close()
		}
	})
	if p.stopErr != nil {
		return fmt.Errorf("close cache plugin: %w", p.stopErr)
	}
	return nil
}

// Stager returns the owned response-cache adapter for route assembly.
func (p *CachePlugin) Stager() *httpcache.Stager {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.stager
}

// Enabled reports whether the cache stager is active.
func (p *CachePlugin) Enabled() bool {
	stager := p.Stager()
	return stager != nil && stager.Enabled()
}

// Middleware returns the existing cache middleware for a route.
func (p *CachePlugin) Middleware(ttl time.Duration) gin.HandlerFunc {
	stager := p.Stager()
	if stager == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return stager.Middleware(ttl)
}
