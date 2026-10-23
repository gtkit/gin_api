// Package redis owns the default redisx client while exposing borrowed handles.
package redis

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/logger/v2"
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
	config     config.RedisConfig
	open       Opener
	instrument func(*goredis.Client) error

	recoverable       bool
	tracingConfigured bool
	recoveryCancel    context.CancelFunc
	recoveryDone      chan struct{}
	lastErr           error
	terminalErr       error
	closed            bool
	closeDone         chan struct{}
	closeErr          error
}

const (
	recoveryInterval = 5 * time.Second
	recoveryTimeout  = 10 * time.Second
)

// Open retains optional failures for recovery after ConfigureTracing. Required
// failures and invalid component results still abort startup.
func Open(ctx context.Context, options Options) (result *Owner, err error) {
	owner := &Owner{instrument: data.InstrumentRedisClient}
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
	owner.config = cfg.Redis
	owner.config.DBs = slices.Clone(cfg.Redis.DBs)
	owner.open = options.Open
	if !owner.configured {
		if required {
			return nil, errors.New("redis.addr is empty but a required capability uses Redis")
		}
		return owner, nil
	}
	client, openErr, terminal := openCandidate(ctx, owner.open, owner.config)
	if openErr != nil {
		closeErr := closeClient(client)
		if required || terminal || errors.Is(openErr, context.Canceled) || errors.Is(openErr, context.DeadlineExceeded) || closeErr != nil {
			return nil, errors.Join(fmt.Errorf("open redis: %w", openErr), closeErr)
		}
		owner.lastErr = openErr
		owner.recoverable = true
		logger.Warnf("redis is configured but unreachable at startup; optional recovery starts after tracing is ready: %v", openErr)
		return owner, nil
	}
	owner.client = client
	return owner, nil
}

func openCandidate(ctx context.Context, open Opener, cfg config.RedisConfig) (client Client, err error, terminal bool) {
	defer func() {
		if value := recover(); value != nil {
			err = errors.Join(err, fmt.Errorf("open redis panic: %w: %v", core.ErrComponentState, value))
			terminal = true
		}
		err = errors.Join(err, ctx.Err())
	}()
	client, err = open(ctx, cfg)
	if client != nil {
		value := reflect.ValueOf(client)
		switch value.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Chan, reflect.Func, reflect.Slice:
			if value.IsNil() {
				client = nil
				return nil, errors.Join(err, fmt.Errorf("open redis: %w: typed-nil client", core.ErrComponentState)), true
			}
		}
	}
	if err == nil && (client == nil || client.DefaultClient() == nil) {
		return client, fmt.Errorf("open redis: %w: nil default client", core.ErrComponentState), true
	}
	return client, err, false
}

func closeClient(client Client) (err error) {
	if client == nil {
		return nil
	}
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("close redis panic: %w: %v", core.ErrComponentState, value)
		}
	}()
	return client.Close()
}

// ConfigureTracing must follow telemetry initialization. It instruments existing
// clients once and registers the sole recovery task before releasing the lock.
func (p *Owner) ConfigureTracing() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return fmt.Errorf("configure redis tracing after close: %w", core.ErrComponentState)
	}
	if p.terminalErr != nil {
		return p.terminalErr
	}
	if p.tracingConfigured {
		return nil
	}
	if p.client != nil {
		if err := p.instrumentCandidate(p.client); err != nil {
			p.terminalErr = err
			return err
		}
	}
	p.tracingConfigured = true
	if p.recoverable {
		ctx, cancel := context.WithCancel(context.Background())
		p.recoveryCancel = cancel
		p.recoveryDone = make(chan struct{})
		go p.recoverClient(ctx)
	}
	return nil
}

// instrumentCandidate runs only before publication or during startup assembly.
func (p *Owner) instrumentCandidate(client Client) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("instrument redis panic: %w: %v", core.ErrComponentState, value)
		}
	}()
	seen := make(map[*goredis.Client]bool)
	for db := range client.PoolStats() {
		rdb, ok := client.GetClient(db)
		if !ok || rdb == nil {
			return fmt.Errorf("instrument redis db %d: %w: missing client", db, core.ErrComponentState)
		}
		if !seen[rdb] {
			if err := p.instrument(rdb); err != nil {
				return fmt.Errorf("instrument redis db %d: %w", db, err)
			}
			seen[rdb] = true
		}
	}
	if !seen[client.DefaultClient()] {
		return fmt.Errorf("instrument redis: %w: default client absent from pool stats", core.ErrComponentState)
	}
	return nil
}

func (p *Owner) recoverClient(ctx context.Context) {
	defer close(p.recoveryDone)
	timer := time.NewTimer(recoveryInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return
		}
		attemptCtx, cancel := context.WithTimeout(ctx, recoveryTimeout)
		candidate, err, terminal := openCandidate(attemptCtx, p.open, p.config)
		cancel()
		p.mu.Lock()
		if !p.closed && err == nil {
			err = p.instrumentCandidate(candidate)
			terminal = err != nil
			if err == nil {
				p.client, p.lastErr = candidate, nil
				p.mu.Unlock()
				return
			}
		}
		p.mu.Unlock()
		closeErr := closeClient(candidate)
		err = errors.Join(err, closeErr)
		p.mu.Lock()
		if closeErr != nil {
			terminal = true
		}
		if terminal {
			p.terminalErr = errors.Join(p.terminalErr, err)
		}
		p.lastErr = err
		closed := p.closed
		p.mu.Unlock()
		if closed || ctx.Err() != nil || terminal {
			return
		}
		timer.Reset(recoveryInterval)
	}
}

// Close seals publication immediately. A caller timeout does not finalize the
// result: later callers can wait for the same candidate cleanup to finish.
func (p *Owner) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.closeDone == nil {
		p.closed = true
		p.closeDone = make(chan struct{})
		client := p.client
		p.client = nil
		if p.recoveryCancel != nil {
			p.recoveryCancel()
		}
		go p.finishClose(client, p.recoveryDone, p.closeDone)
	}
	done := p.closeDone
	p.mu.Unlock()
	select {
	case <-done:
		return p.closeErr
	default:
	}
	select {
	case <-done:
		return p.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Owner) finishClose(client Client, recoveryDone, done chan struct{}) {
	err := closeClient(client)
	if recoveryDone != nil {
		<-recoveryDone
	}
	p.mu.Lock()
	p.closeErr = errors.Join(err, p.terminalErr)
	close(done)
	p.mu.Unlock()
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
	err := p.lastErr
	p.mu.RUnlock()
	if client == nil {
		return errors.Join(errors.New("redis is not available in this process"), err)
	}
	return client.HealthCheck(ctx)
}
