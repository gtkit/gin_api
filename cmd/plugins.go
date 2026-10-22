package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/gtkit/gin-api/internal/bootstrap"
	"github.com/gtkit/gin-api/internal/core"
	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/platform/config"
	defaultcache "github.com/gtkit/gin-api/internal/plugin/builtin/cache"
	defaultconfig "github.com/gtkit/gin-api/internal/plugin/builtin/config"
	defaulthttpmiddleware "github.com/gtkit/gin-api/internal/plugin/builtin/httpmiddleware"
	defaultlogging "github.com/gtkit/gin-api/internal/plugin/builtin/logging"
	defaultmail "github.com/gtkit/gin-api/internal/plugin/builtin/mail"
	defaultmysql "github.com/gtkit/gin-api/internal/plugin/builtin/mysql"
	defaultnotification "github.com/gtkit/gin-api/internal/plugin/builtin/notification"
	defaultredis "github.com/gtkit/gin-api/internal/plugin/builtin/redis"
	defaultrobot "github.com/gtkit/gin-api/internal/plugin/builtin/robot"
	defaultsms "github.com/gtkit/gin-api/internal/plugin/builtin/sms"
	gormlogger "gorm.io/gorm/logger"
)

// loadRuntimeConfiguration runs inside config.Init before logging starts workers.
func loadRuntimeConfiguration(ctx context.Context) (*config.Config, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg, err := defaultconfig.Load()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bootstrap.ApplyRuntimeTimezone(cfg)
	return cfg, nil
}

// registerServerPlugins is the compile-time composition root. Constructors and
// Register perform no configuration reads or component I/O.
func registerServerPlugins(registry *core.PluginRegistry) (bootstrap.PluginSet, error) {
	if registry == nil {
		return bootstrap.PluginSet{}, errors.New("plugin registry is required")
	}
	configuration := defaultconfig.New(loadRuntimeConfiguration)
	database := defaultmysql.New(defaultmysql.Options{Configuration: configuration})
	redis := defaultredis.New(defaultredis.Options{Configuration: configuration, Required: bootstrap.RedisRequired})
	cache := defaultcache.New(defaultcache.Options{Configuration: configuration, Redis: redis})
	middleware := defaulthttpmiddleware.New(defaulthttpmiddleware.Options{Configuration: configuration})
	if err := registry.Register(configuration); err != nil {
		return bootstrap.PluginSet{}, err
	}
	logging, err := registerNotificationLogging(registry, configuration)
	if err != nil {
		return bootstrap.PluginSet{}, err
	}
	for _, component := range []core.Plugin{database, redis} {
		if err := registry.Register(component, configuration.Name(), logging.Name()); err != nil {
			return bootstrap.PluginSet{}, err
		}
	}
	// The graph is fixed before config.Init. Memory and disabled caches never
	// access Redis even though the default graph declares this static edge.
	if err := registry.Register(cache, configuration.Name(), logging.Name(), redis.Name()); err != nil {
		return bootstrap.PluginSet{}, err
	}
	if err := registry.Register(middleware, configuration.Name(), logging.Name()); err != nil {
		return bootstrap.PluginSet{}, err
	}
	return bootstrap.PluginSet{
		Configuration: configuration, Logging: logging, Database: database,
		Redis: redis, Cache: cache, Middleware: middleware, DrainLogs: logging.Drain,
	}, nil
}

// registerMigrationPlugins selects configuration, notification, logging and DB;
// it does not register Redis, cache or HTTP. DDL uses the command timeout.
func registerMigrationPlugins(registry *core.PluginRegistry) (bootstrap.MigrationPluginSet, error) {
	if registry == nil {
		return bootstrap.MigrationPluginSet{}, errors.New("plugin registry is required")
	}
	configuration := defaultconfig.New(loadRuntimeConfiguration)
	database := defaultmysql.New(defaultmysql.Options{
		Configuration: configuration, SelectCredentials: migrationCredentials,
		Open: func(ctx context.Context, cfg config.DatabaseConfig, creds config.DBCredentials, logger gormlogger.Interface) (defaultmysql.Client, error) {
			client, err := defaultmysql.OpenMigrationDB(ctx, cfg, creds, logger)
			if client == nil {
				return nil, err
			}
			return client, err
		},
	})
	if err := registry.Register(configuration); err != nil {
		return bootstrap.MigrationPluginSet{}, err
	}
	logging, err := registerNotificationLogging(registry, configuration)
	if err != nil {
		return bootstrap.MigrationPluginSet{}, err
	}
	if err := registry.Register(database, configuration.Name(), logging.Name()); err != nil {
		return bootstrap.MigrationPluginSet{}, fmt.Errorf("register migration database: %w", err)
	}
	return bootstrap.MigrationPluginSet{
		Configuration: configuration, Logging: logging, Database: database, DrainLogs: logging.Drain,
	}, nil
}

// registerNotificationLogging keeps both entry points on the same ownership graph.
// Only Init may read configuration or open a sender.
func registerNotificationLogging(registry *core.PluginRegistry, configuration *defaultconfig.Plugin) (*defaultlogging.Plugin, error) {
	robot := defaultrobot.New(defaultrobot.Options{Configuration: configuration})
	mail := defaultmail.New(defaultmail.Options{Configuration: configuration})
	sms := defaultsms.New(defaultsms.Options{Configuration: configuration})
	for _, channel := range []core.Plugin{robot, mail, sms} {
		if err := registry.Register(channel, configuration.Name()); err != nil {
			return nil, fmt.Errorf("register notification channel: %w", err)
		}
	}
	notification := defaultnotification.New(defaultnotification.Options{Configuration: configuration, Robot: robot, Mail: mail, SMS: sms})
	if err := registry.Register(notification, configuration.Name(), robot.Name(), mail.Name(), sms.Name()); err != nil {
		return nil, fmt.Errorf("register notification: %w", err)
	}
	logging := defaultlogging.New(defaultlogging.Options{Configuration: configuration, Notification: notification})
	if err := registry.Register(logging, configuration.Name(), notification.Name()); err != nil {
		return nil, fmt.Errorf("register notification logging: %w", err)
	}
	return logging, nil
}

func migrationCredentials(cfg config.DatabaseConfig) config.DBCredentials {
	if creds, ok := cfg.MigrationCredentials(); ok {
		return creds
	}
	applog.Warn(migrationFallbackWarning)
	return cfg.RuntimeCredentials()
}

// A fixed warning must never interpolate credentials.
const migrationFallbackWarning = "migration is using the runtime database credentials: " +
	"set database.migrate_username to grant DDL only to a dedicated account"
