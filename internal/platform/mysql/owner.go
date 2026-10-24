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
	"github.com/gtkit/gin-api/internal/platform/data"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Client is the owning factory result; consumers borrow DatabaseHandle instead.
type Client interface {
	data.DatabaseHandle
	Close() error
}

type Opener func(context.Context, config.DatabaseConfig, config.DBCredentials, gormlogger.Interface) (Client, error)
type CredentialSelector func(config.DatabaseConfig) config.DBCredentials

type Options struct {
	Config            *config.Config
	SelectCredentials CredentialSelector
	Open              Opener
}

// Owner publishes a borrowed view until Close releases the client.
type Owner struct {
	mu        sync.RWMutex
	client    Client
	closeOnce sync.Once
	closeErr  error
}

// Open constructs and verifies one client. Every failure releases partial results.
func Open(ctx context.Context, options Options) (result *Owner, err error) {
	owner := new(Owner)
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("open mysql panic: %w: %v", core.ErrComponentState, value)
		}
		if err != nil {
			err = errors.Join(err, owner.Close(ctx))
			result = nil
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.Config == nil {
		return nil, fmt.Errorf("mysql configuration is required: %w", core.ErrComponentState)
	}
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
	cfg := options.Config
	client, openErr := options.Open(ctx, cfg.Database, options.SelectCredentials(cfg.Database), data.NewGormLogger(cfg))
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
	if openErr == nil && (client == nil || client.DB() == nil) {
		openErr = fmt.Errorf("open mysql: %w: client unavailable", core.ErrComponentState)
	}
	if err := errors.Join(openErr, ctx.Err()); err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	return owner, nil
}

// Close releases the client once and caches its result for concurrent callers.
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

func (p *Owner) DB() *gorm.DB {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.client == nil {
		return nil
	}
	return p.client.DB()
}

func (p *Owner) SQLDB() *sql.DB {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.client == nil {
		return nil
	}
	return p.client.SQLDB()
}

func (p *Owner) PingContext(ctx context.Context) error {
	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()
	if client == nil {
		return errors.New("mysql connection is not available")
	}
	return client.PingContext(ctx)
}
