package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
	logger "github.com/gtkit/logger/v2"
	"github.com/gtkit/redisx"
	"github.com/redis/go-redis/v9"
)

// RedisOpener constructs the concrete Redis client owned by RedisPlugin.
type RedisOpener func(context.Context, config.RedisConfig) (*redisx.Client, error)

// RedisOptions configures a RedisPlugin.
type RedisOptions struct {
	Config   config.RedisConfig
	Required bool
	Open     RedisOpener
}

// RedisPlugin is the default Redis lifecycle adapter.
type RedisPlugin struct {
	config   config.RedisConfig
	required bool
	open     RedisOpener

	mu           sync.RWMutex
	client       *redisx.Client
	configured   bool
	availability *data.AvailabilityTracker

	stopOnce sync.Once
	stopErr  error
}

var _ core.Plugin = (*RedisPlugin)(nil)
var _ RedisLifecyclePlugin = (*RedisPlugin)(nil)

// NewRedisPlugin creates a Redis plugin using the default data.OpenRedis opener
// unless an opener is supplied in options.
func NewRedisPlugin(options RedisOptions) *RedisPlugin {
	open := options.Open
	if open == nil {
		open = data.OpenRedis
	}
	return &RedisPlugin{
		config:   options.Config,
		required: options.Required,
		open:     open,
	}
}

// Name returns the stable registry name.
func (p *RedisPlugin) Name() string { return "redis" }

// Init opens Redis when configured and applies required/optional failure
// semantics. Optional startup failure becomes a permanent degraded state for
// this process because redisx cannot return a reconnectable partial client.
func (p *RedisPlugin) Init(ctx context.Context) error {
	if p == nil {
		return errors.New("redis plugin is nil")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("init redis plugin: %w", err)
	}

	configured := strings.TrimSpace(p.config.Addr) != ""
	p.mu.Lock()
	p.configured = configured
	p.mu.Unlock()

	if !configured {
		p.setAvailability(data.NewPermanentlyDegradedTracker("redis"))
		if p.required {
			return errors.New("init redis plugin: redis.addr is empty but a required capability uses Redis")
		}
		return nil
	}

	client, err := p.open(ctx, p.config)
	if err != nil {
		p.setAvailability(data.NewPermanentlyDegradedTracker("redis"))
		if p.required {
			return fmt.Errorf("open redis plugin: %w", err)
		}
		logger.Warnf("redis is configured but unreachable at startup; optional capabilities stay degraded until restart: %v", err)
		return nil
	}
	if client == nil {
		p.setAvailability(data.NewPermanentlyDegradedTracker("redis"))
		return errors.New("open redis plugin: opener returned nil client")
	}

	tracker, trackerErr := data.NewAvailabilityTracker(
		"redis", data.DefaultAvailabilityPolicy(),
		func(probeCtx context.Context) error { return client.HealthCheck(probeCtx) },
	)
	if trackerErr != nil {
		closeErr := client.Close()
		return fmt.Errorf("build redis availability tracker: %w", errors.Join(trackerErr, closeErr))
	}

	p.mu.Lock()
	p.client = client
	p.availability = tracker
	p.mu.Unlock()
	return nil
}

// Start verifies that required Redis produced a client. The opener already
// performed the context-bound connectivity checks during Init.
func (p *RedisPlugin) Start(context.Context) error {
	if p == nil {
		return errors.New("start redis plugin: plugin is nil")
	}
	if p.required && p.Configured() && p.DefaultClient() == nil {
		return errors.New("start redis plugin: required client is not initialized")
	}
	return nil
}

// Stop closes the Redis client exactly once.
func (p *RedisPlugin) Stop(context.Context) error {
	if p == nil {
		return nil
	}
	p.stopOnce.Do(func() {
		p.mu.Lock()
		client := p.client
		p.client = nil
		p.mu.Unlock()
		if client != nil {
			p.stopErr = client.Close()
		}
	})
	if p.stopErr != nil {
		return fmt.Errorf("close redis plugin: %w", p.stopErr)
	}
	return nil
}

// Client returns a typed go-redis handle for an initialized logical DB.
func (p *RedisPlugin) Client(db int) *redis.Client {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.client == nil {
		return nil
	}
	client, ok := p.client.GetClient(db)
	if !ok {
		return nil
	}
	return client
}

// GetClient exposes the narrow Resources view without exposing Close.
func (p *RedisPlugin) GetClient(db int) (*redis.Client, bool) {
	if p == nil {
		return nil, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.client == nil {
		return nil, false
	}
	return p.client.GetClient(db)
}

// PoolStats exposes per-DB pool metrics to the Resources view.
func (p *RedisPlugin) PoolStats() map[int]*redis.PoolStats {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.client == nil {
		return nil
	}
	return p.client.PoolStats()
}

// HealthCheck runs a probe without updating the availability state. Callers
// that own the state tracker record the result after the probe returns.
func (p *RedisPlugin) HealthCheck(ctx context.Context) error {
	if p == nil {
		return errors.New("redis is not available in this process")
	}
	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()
	if client == nil {
		return errors.New("redis is not available in this process")
	}
	return client.HealthCheck(ctx)
}

// DefaultClient returns the default logical DB client.
func (p *RedisPlugin) DefaultClient() *redis.Client {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.client == nil {
		return nil
	}
	return p.client.DefaultClient()
}

// Configured reports whether Redis was configured with an address.
func (p *RedisPlugin) Configured() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.configured
}

// Availability returns the Redis availability tracker.
func (p *RedisPlugin) Availability() *data.AvailabilityTracker {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.availability
}

// PingRedis executes a health check and records the result in the tracker.
func (p *RedisPlugin) Ping(ctx context.Context) error {
	client := p.DefaultClient()
	if client == nil {
		return errors.New("redis is not available in this process")
	}
	p.mu.RLock()
	inner := p.client
	tracker := p.availability
	p.mu.RUnlock()
	if inner == nil || tracker == nil {
		return errors.New("redis is not available in this process")
	}
	err := inner.HealthCheck(ctx)
	tracker.Observe(err)
	return err
}

func (p *RedisPlugin) setAvailability(tracker *data.AvailabilityTracker) {
	p.mu.Lock()
	p.availability = tracker
	p.mu.Unlock()
}
