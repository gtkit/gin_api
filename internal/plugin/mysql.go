package plugin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/ormx"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// MySQLOpener constructs the concrete database client owned by MySQLPlugin.
//
// The default opener is data.OpenDB. The function is injected so a business
// entry point can select another compile-time implementation without changing
// the registry or repository layer.
type MySQLOpener func(
	context.Context,
	config.DatabaseConfig,
	config.DBCredentials,
	gormlogger.Interface,
) (*ormx.Client, error)

// MySQLOptions configures a MySQLPlugin.
type MySQLOptions struct {
	Config      config.DatabaseConfig
	Credentials config.DBCredentials
	GormLogger  gormlogger.Interface
	Open        MySQLOpener
}

// MySQLPlugin is the default MySQL lifecycle adapter.
type MySQLPlugin struct {
	config      config.DatabaseConfig
	credentials config.DBCredentials
	gormLogger  gormlogger.Interface
	open        MySQLOpener

	mu     sync.RWMutex
	client *ormx.Client

	stopOnce sync.Once
	stopErr  error
}

var _ core.Plugin = (*MySQLPlugin)(nil)
var _ DatabasePlugin = (*MySQLPlugin)(nil)

// NewMySQLPlugin creates a MySQL plugin using the default data.OpenDB opener
// unless an opener is supplied in options.
func NewMySQLPlugin(options MySQLOptions) *MySQLPlugin {
	open := options.Open
	if open == nil {
		open = data.OpenDB
	}
	return &MySQLPlugin{
		config:      options.Config,
		credentials: options.Credentials,
		gormLogger:  options.GormLogger,
		open:        open,
	}
}

// Name returns the stable registry name.
func (p *MySQLPlugin) Name() string { return "mysql" }

// Init opens and validates the MySQL connection.
func (p *MySQLPlugin) Init(ctx context.Context) error {
	if p == nil {
		return errors.New("mysql plugin is nil")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("init mysql plugin: %w", err)
	}

	client, err := p.open(ctx, p.config, p.credentials, p.gormLogger)
	if err != nil {
		return fmt.Errorf("open mysql plugin: %w", err)
	}
	if client == nil {
		return errors.New("open mysql plugin: opener returned nil client")
	}

	p.mu.Lock()
	p.client = client
	p.mu.Unlock()
	return nil
}

// Start verifies that Init produced a usable client. The opener already owns
// the context-bound startup probe, so Start does not issue a second network
// round trip.
func (p *MySQLPlugin) Start(context.Context) error {
	if p == nil {
		return errors.New("start mysql plugin: plugin is nil")
	}
	if p.DB() == nil {
		return errors.New("start mysql plugin: client is not initialized")
	}
	return nil
}

// Stop closes the client exactly once. The underlying ormx Close operation has
// no context parameter; the registry still supplies context to keep one
// uniform lifecycle contract across plugins.
func (p *MySQLPlugin) Stop(context.Context) error {
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
		return fmt.Errorf("close mysql plugin: %w", p.stopErr)
	}
	return nil
}

// DB returns the GORM handle for repositories and module assembly.
func (p *MySQLPlugin) DB() *gorm.DB {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.client == nil {
		return nil
	}
	return p.client.DB()
}

// SQLDB returns the underlying database/sql handle for pool metrics and probes.
func (p *MySQLPlugin) SQLDB() *sql.DB {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.client == nil {
		return nil
	}
	return p.client.SQLDB()
}

// PingContext checks the owned database connection without exposing its close
// method.
func (p *MySQLPlugin) PingContext(ctx context.Context) error {
	if p == nil {
		return errors.New("mysql connection is not available")
	}
	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()
	if client == nil {
		return errors.New("mysql connection is not available")
	}
	return client.PingContext(ctx)
}
