package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gtkit/gin-api/internal/core"
	logger "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/plugin"
	"gorm.io/gorm"
)

const defaultMigrationStartupTimeout = 15 * time.Second

// MigrationPluginSet is the deliberately minimal borrowed capability set.
type MigrationPluginSet struct {
	Configuration plugin.ConfigurationProvider
	Logging       plugin.Logger
	Database      plugin.DatabaseProvider
	DrainLogs     func(context.Context) error
}

type MigrationPluginRegistrar func(*core.PluginRegistry) (MigrationPluginSet, error)

type MigrationOptions struct {
	RegisterPlugins MigrationPluginRegistrar
	Shutdown        *ShutdownCoordinator
}

// MigrationRuntime owns only its registry. It never initializes HTTP, Redis,
// cache, JWT or workers and never grants database closing rights to consumers.
type MigrationRuntime struct {
	cfg      *config.Config
	database plugin.DatabaseProvider
	plugins  *core.PluginRegistry
}

func NewMigrationRuntime(ctx context.Context, opts MigrationOptions) (_ *MigrationRuntime, err error) {
	if opts.RegisterPlugins == nil {
		return nil, errors.New("migration plugin registrar is required")
	}
	force := opts.Shutdown.ForcedCh()
	budget := &rollbackBudget{force: force}
	defer budget.Close()
	registry := core.NewPluginRegistry()
	if err := registry.SetForceSignal(force); err != nil {
		return nil, err
	}
	set, err := opts.RegisterPlugins(registry)
	if err != nil {
		return nil, fmt.Errorf("register migration plugins: %w", err)
	}
	if nilCapability(set.Configuration) || nilCapability(set.Logging) || nilCapability(set.Database) || set.DrainLogs == nil {
		return nil, errors.New("migration configuration, logging, database and logging drain are required")
	}
	openCtx, cancel := context.WithTimeout(ctx, defaultMigrationStartupTimeout)
	defer cancel()
	if err := registry.Init(openCtx); err != nil {
		return nil, fmt.Errorf("init migration plugins: %w", err)
	}
	if err := registry.Start(openCtx); err != nil {
		return nil, fmt.Errorf("start migration plugins: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, registry.Stop(budget.Context(ctx)))
		}
	}()
	cfg, err := set.Configuration.Value()
	if err != nil {
		return nil, fmt.Errorf("read migration configuration: %w", err)
	}
	if cfg == nil || set.Database.DB() == nil {
		return nil, errors.New("migration configuration or database unavailable")
	}
	if err := openCtx.Err(); err != nil {
		return nil, err
	}
	logger.Infof("migration runtime initialized with env=%s", cfg.Env)
	return &MigrationRuntime{cfg: cfg, database: set.Database, plugins: registry}, nil
}

func (r *MigrationRuntime) Config() *config.Config {
	if r == nil {
		return nil
	}
	return r.cfg
}

// DB returns a borrowed GORM handle. The database plugin is its sole owner.
func (r *MigrationRuntime) DB() *gorm.DB {
	if r == nil || r.database == nil {
		return nil
	}
	return r.database.DB()
}

// Close shares the registry result and preserves the caller's stop context.
func (r *MigrationRuntime) Close(ctx context.Context) error {
	if r == nil || r.plugins == nil {
		return nil
	}
	if err := r.plugins.Stop(ctx); err != nil {
		return fmt.Errorf("close migration plugins: %w", err)
	}
	return nil
}
