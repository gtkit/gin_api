// Package redis owns the default redisx client while exposing borrowed handles.
package redis

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/gtkit/gin-api/internal/core"
	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/gin-api/internal/plugin"
	goredis "github.com/redis/go-redis/v9"
)

// Client is the owning factory result used only by this default implementation.
type Client interface {
	data.RedisHandle
	DefaultClient() *goredis.Client
	Close() error
}

// Opener constructs a default client during Init.
type Opener func(context.Context, config.RedisConfig) (Client, error)

// Requirement derives Redis necessity from the enabled capabilities.
type Requirement func(*config.Config) bool

// Options stores named dependencies without reading configuration.
type Options struct {
	Configuration plugin.ConfigurationProvider
	Required      Requirement
	Open          Opener
}

// Plugin owns one optional or required Redis connection set.
type Plugin struct {
	options                     Options
	lifecycle                   sync.Mutex
	initialized, stopped, ready bool
	initErr, stopErr            error
	mu                          sync.RWMutex
	client                      Client
	configured                  bool
}

var _ core.Plugin = (*Plugin)(nil)
var _ plugin.RedisProvider = (*Plugin)(nil)

// New creates no connections. A nil requirement selects optional Redis.
func New(options Options) *Plugin {
	if options.Open == nil {
		options.Open = func(ctx context.Context, c config.RedisConfig) (Client, error) {
			client, err := OpenRedis(ctx, c)
			if client == nil {
				return nil, err
			}
			return client, err
		}
	}
	return &Plugin{options: options}
}
func (*Plugin) Name() string { return "redis" }
func (p *Plugin) Init(ctx context.Context) error {
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	if p.initialized {
		return p.initErr
	}
	p.initialized = true
	p.initErr = p.initialize(ctx)
	p.ready = p.initErr == nil
	return p.initErr
}
func (p *Plugin) initialize(ctx context.Context) error {
	if p.stopped {
		return fmt.Errorf("init redis after stop: %w", core.ErrPluginContractViolation)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.options.Configuration == nil {
		return errors.New("redis configuration provider is required")
	}
	cfg, err := p.options.Configuration.Value()
	if err != nil {
		return fmt.Errorf("read redis config: %w", err)
	}
	if cfg == nil {
		return fmt.Errorf("read redis config: %w", core.ErrPluginContractViolation)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	required := p.options.Required != nil && p.options.Required(cfg)
	configured := strings.TrimSpace(cfg.Redis.Addr) != ""
	p.mu.Lock()
	p.configured = configured
	p.mu.Unlock()
	if !configured {
		if required {
			return errors.New("init redis plugin: redis.addr is empty but a required capability uses Redis")
		}
		return nil
	}
	client, openErr := p.options.Open(ctx, cfg.Redis)
	if client != nil {
		v := reflect.ValueOf(client)
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Chan, reflect.Func, reflect.Slice:
			if v.IsNil() {
				client = nil
			}
		}
	}
	contractErr := error(nil)
	if openErr == nil && (client == nil || client.DefaultClient() == nil) {
		contractErr = fmt.Errorf("open redis: %w: nil client", core.ErrPluginContractViolation)
	}
	err = errors.Join(openErr, contractErr, ctx.Err())
	if err != nil {
		if client != nil {
			p.stopErr = client.Close()
		}
		if required || contractErr != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || p.stopErr != nil {
			return errors.Join(fmt.Errorf("open redis plugin: %w", err), p.stopErr)
		}
		applog.Warnf("redis is configured but unreachable at startup; optional capabilities stay degraded until restart: %v", err)
		return nil
	}
	p.mu.Lock()
	p.client = client
	p.mu.Unlock()
	return nil
}
func (p *Plugin) Start(ctx context.Context) error {
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	if !p.ready || p.stopped {
		return fmt.Errorf("start redis: %w: initialization unavailable", core.ErrPluginContractViolation)
	}
	return ctx.Err()
}
func (p *Plugin) Stop(ctx context.Context) error {
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	if !p.stopped {
		p.stopped = true
		p.mu.Lock()
		client := p.client
		p.client = nil
		p.mu.Unlock()
		if client != nil {
			p.stopErr = client.Close()
		}
		p.stopErr = errors.Join(p.stopErr, ctx.Err())
	}
	return p.stopErr
}
func (p *Plugin) Client(db int) *goredis.Client { client, _ := p.GetClient(db); return client }
func (p *Plugin) GetClient(db int) (*goredis.Client, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.client == nil {
		return nil, false
	}
	return p.client.GetClient(db)
}
func (p *Plugin) DefaultClient() *goredis.Client {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.client == nil {
		return nil
	}
	return p.client.DefaultClient()
}
func (p *Plugin) Configured() bool { p.mu.RLock(); defer p.mu.RUnlock(); return p.configured }
func (p *Plugin) PoolStats() map[int]*goredis.PoolStats {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.client == nil {
		return nil
	}
	return p.client.PoolStats()
}
func (p *Plugin) HealthCheck(ctx context.Context) error {
	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()
	if client == nil {
		return errors.New("redis is not available in this process")
	}
	return client.HealthCheck(ctx)
}
