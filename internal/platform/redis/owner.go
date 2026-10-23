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
	goredis "github.com/redis/go-redis/v9"
)

type Client interface {
	data.RedisHandle
	DefaultClient() *goredis.Client
	Close() error
}

type Opener func(context.Context, config.RedisConfig) (Client, error)
type Requirement func(*config.Config) bool

type Options struct {
	Config   *config.Config
	Required Requirement
	Open     Opener
}

// Owner holds one optional or required Redis connection set.
type Owner struct {
	mu         sync.RWMutex
	client     Client
	configured bool
	closeOnce  sync.Once
	closeErr   error
}

// Open keeps optional connection failures degraded until the process restarts.
func Open(ctx context.Context, options Options) (result *Owner, err error) {
	owner := new(Owner)
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("open redis panic: %w: %v", core.ErrComponentState, value)
		}
		if err != nil {
			err = errors.Join(err, owner.Close(ctx))
			result = nil
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg := options.Config
	if cfg == nil {
		return nil, fmt.Errorf("redis configuration is required: %w", core.ErrComponentState)
	}
	if options.Open == nil {
		options.Open = func(ctx context.Context, c config.RedisConfig) (Client, error) {
			client, err := OpenRedis(ctx, c)
			if client == nil {
				return nil, err
			}
			return client, err
		}
	}
	required := options.Required != nil && options.Required(cfg)
	owner.configured = strings.TrimSpace(cfg.Redis.Addr) != ""
	if !owner.configured {
		if required {
			return nil, errors.New("redis.addr is empty but a required capability uses Redis")
		}
		return owner, nil
	}
	client, openErr := options.Open(ctx, cfg.Redis)
	if client != nil {
		value := reflect.ValueOf(client)
		switch value.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Chan, reflect.Func, reflect.Slice:
			if value.IsNil() {
				client = nil
			}
		}
	}
	owner.client = client
	var contractErr error
	if openErr == nil && (client == nil || client.DefaultClient() == nil) {
		contractErr = fmt.Errorf("open redis: %w: nil client", core.ErrComponentState)
	}
	if err := errors.Join(openErr, contractErr, ctx.Err()); err != nil {
		var closeErr error
		if client != nil {
			owner.client = nil
			closeErr = client.Close()
		}
		if required || contractErr != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || closeErr != nil {
			return nil, errors.Join(fmt.Errorf("open redis: %w", err), closeErr)
		}
		applog.Warnf("redis is configured but unreachable at startup; optional capabilities stay degraded until restart: %v", err)
	}
	return owner, nil
}

func (p *Owner) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		p.mu.Lock()
		client := p.client
		p.client = nil
		p.mu.Unlock()
		if client != nil {
			p.closeErr = client.Close()
		}
		p.closeErr = errors.Join(p.closeErr, ctx.Err())
	})
	return p.closeErr
}

func (p *Owner) Client(db int) *goredis.Client { client, _ := p.GetClient(db); return client }
func (p *Owner) GetClient(db int) (*goredis.Client, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.client == nil {
		return nil, false
	}
	return p.client.GetClient(db)
}
func (p *Owner) DefaultClient() *goredis.Client {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.client == nil {
		return nil
	}
	return p.client.DefaultClient()
}
func (p *Owner) Configured() bool { return p.configured }
func (p *Owner) PoolStats() map[int]*goredis.PoolStats {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.client == nil {
		return nil
	}
	return p.client.PoolStats()
}
func (p *Owner) HealthCheck(ctx context.Context) error {
	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()
	if client == nil {
		return errors.New("redis is not available in this process")
	}
	return client.HealthCheck(ctx)
}
