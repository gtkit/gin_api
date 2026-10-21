// Package mysql owns the default MySQL client and its startup verification.
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/gin-api/internal/plugin"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Client is the owning factory result used only inside this default implementation.
type Client interface {
	plugin.DatabaseProvider
	Close() error
}

// Opener constructs a client owned solely by this implementation.
type Opener func(context.Context, config.DatabaseConfig, config.DBCredentials, gormlogger.Interface) (Client, error)

// CredentialSelector selects the already configured identity without opening resources.
type CredentialSelector func(config.DatabaseConfig) config.DBCredentials

// Options defers all configuration reads and connection work to Init.
type Options struct {
	Configuration     plugin.ConfigurationProvider
	SelectCredentials CredentialSelector
	Open              Opener
}

// Plugin publishes a borrowed view after successful construction and verification.
type Plugin struct {
	options              Options
	lifecycle            sync.Mutex
	initialized, stopped bool
	initErr, stopErr     error
	mu                   sync.RWMutex
	client               Client
}

var _ core.Plugin = (*Plugin)(nil)
var _ plugin.DatabaseProvider = (*Plugin)(nil)

// New creates no resources. Nil selectors use runtime credentials and OpenDB.
func New(options Options) *Plugin {
	if options.Open == nil {
		options.Open = func(ctx context.Context, cfg config.DatabaseConfig, creds config.DBCredentials, logger gormlogger.Interface) (Client, error) {
			client, err := OpenDB(ctx, cfg, creds, logger)
			if client == nil {
				return nil, err
			}
			return client, err
		}
	}
	if options.SelectCredentials == nil {
		options.SelectCredentials = func(c config.DatabaseConfig) config.DBCredentials { return c.RuntimeCredentials() }
	}
	return &Plugin{options: options}
}
func (*Plugin) Name() string { return "mysql" }
func (p *Plugin) Init(ctx context.Context) error {
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
		return fmt.Errorf("init mysql after stop: %w", core.ErrPluginContractViolation)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.options.Configuration == nil {
		return errors.New("mysql configuration provider is required")
	}
	cfg, err := p.options.Configuration.Value()
	if err != nil {
		return fmt.Errorf("read mysql config: %w", err)
	}
	if cfg == nil {
		return fmt.Errorf("read mysql config: %w", core.ErrPluginContractViolation)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	client, err := p.options.Open(ctx, cfg.Database, p.options.SelectCredentials(cfg.Database), data.NewGormLogger(cfg))
	if client != nil {
		v := reflect.ValueOf(client)
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Chan, reflect.Func, reflect.Slice:
			if v.IsNil() {
				client = nil
			}
		}
	}
	if client == nil && err == nil {
		err = fmt.Errorf("open mysql: %w: nil client", core.ErrPluginContractViolation)
	}
	err = errors.Join(err, ctx.Err())
	if err != nil {
		if client != nil {
			p.stopped = true
			p.stopErr = client.Close()
		}
		return errors.Join(fmt.Errorf("open mysql plugin: %w", err), p.stopErr)
	}
	p.mu.Lock()
	p.client = client
	p.mu.Unlock()
	return nil
}
func (p *Plugin) Start(ctx context.Context) error {
	if p.DB() == nil {
		return fmt.Errorf("start mysql: %w: client unavailable", core.ErrPluginContractViolation)
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
	if p.stopErr != nil {
		return fmt.Errorf("close mysql plugin: %w", p.stopErr)
	}
	return nil
}
func (p *Plugin) DB() *gorm.DB {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.client == nil {
		return nil
	}
	return p.client.DB()
}
func (p *Plugin) SQLDB() *sql.DB {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.client == nil {
		return nil
	}
	return p.client.SQLDB()
}
func (p *Plugin) PingContext(ctx context.Context) error {
	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()
	if client == nil {
		return errors.New("mysql connection is not available")
	}
	return client.PingContext(ctx)
}
