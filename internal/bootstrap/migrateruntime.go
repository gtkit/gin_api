package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/logger/v2"
	"gorm.io/gorm"
)

const defaultMigrationStartupTimeout = 15 * time.Second

type MigrationOptions struct {
	Shutdown  *ShutdownCoordinator
	factories *runtimeFactories
}

// MigrationRuntime owns only logging/notifications and a migration DB connection.
// HTTP, Redis, cache, JWT and workers are never constructed on this path.
type MigrationRuntime struct {
	cfg    *config.Config
	owners *runtimeResources
}

func NewMigrationRuntime(ctx context.Context, opts MigrationOptions) (_ *MigrationRuntime, err error) {
	force := opts.Shutdown.ForcedCh()
	budget := &rollbackBudget{force: force}
	defer budget.Close()
	ctx, finish := forceContext(ctx, force)
	defer finish()
	ctx, cancel := context.WithTimeout(ctx, defaultMigrationStartupTimeout)
	defer cancel()
	factories := defaultRuntimeFactories()
	if opts.factories != nil {
		factories = *opts.factories
	}
	cfg, err := openResource(ctx, "configuration", factories.loadConfig)
	if err != nil {
		return nil, err
	}
	ApplyRuntimeTimezone(cfg)
	owners, err := openRuntimeResources(ctx, cfg, factories, true, budget)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, owners.Close(budget.Context(ctx)))
		}
	}()
	if owners.database.DB() == nil {
		return nil, errors.New("migration database unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	logger.Infof("migration runtime initialized with env=%s", cfg.Env)
	return &MigrationRuntime{cfg: cfg, owners: owners}, nil
}

func (r *MigrationRuntime) Config() *config.Config {
	if r == nil {
		return nil
	}
	return r.cfg
}

// DB returns a borrowed GORM handle. The startup deadline never becomes a DDL deadline.
func (r *MigrationRuntime) DB() *gorm.DB {
	if r == nil || r.owners == nil || r.owners.database == nil {
		return nil
	}
	return r.owners.database.DB()
}

func (r *MigrationRuntime) Close(ctx context.Context) error {
	if r == nil || r.owners == nil {
		return nil
	}
	if err := r.owners.Close(ctx); err != nil {
		return fmt.Errorf("close migration runtime: %w", err)
	}
	return nil
}
