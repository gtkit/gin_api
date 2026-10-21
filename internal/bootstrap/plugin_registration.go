package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/gin-api/internal/plugin"
)

// PluginRegistrationOptions carries compile-time registration inputs selected
// by the process entry point. It contains values, not a service locator.
type PluginRegistrationOptions struct {
	// DatabaseCredentials are selected by the caller for the database plugin.
	DatabaseCredentials config.DBCredentials
}

// PluginSet is the typed capability set required by the bootstrap layer.
// Every value must also be registered in the supplied PluginRegistry.
type PluginSet struct {
	Database plugin.DatabasePlugin
	Redis    plugin.RedisLifecyclePlugin
	Cache    plugin.CacheLifecyclePlugin
}

// PluginRegistrar lets a business main choose the concrete compile-time
// implementations while core lifecycle orchestration remains unchanged.
type PluginRegistrar func(
	context.Context,
	*config.Config,
	*core.PluginRegistry,
	PluginRegistrationOptions,
) (PluginSet, error)

// RegisterDefaultPlugins registers the scaffold's built-in MySQL, Redis and
// response-cache implementations. A business entry point may pass this
// function explicitly or provide another registrar with the same typed set.
func RegisterDefaultPlugins(
	_ context.Context,
	cfg *config.Config,
	registry *core.PluginRegistry,
	options PluginRegistrationOptions,
) (PluginSet, error) {
	if cfg == nil {
		return PluginSet{}, errors.New("register plugins: config is required")
	}
	if registry == nil {
		return PluginSet{}, errors.New("register plugins: registry is required")
	}

	mysqlPlugin := plugin.NewMySQLPlugin(plugin.MySQLOptions{
		Config:      cfg.Database,
		Credentials: options.DatabaseCredentials,
		GormLogger:  data.NewGormLogger(cfg),
	})
	redisPlugin := plugin.NewRedisPlugin(plugin.RedisOptions{
		Config:   cfg.Redis,
		Required: resourceRequirements(cfg).Redis,
	})
	cachePlugin := plugin.NewCachePlugin(plugin.CacheOptions{
		Config:  cfg.Cache,
		Redis:   redisPlugin,
		AppName: cfg.App.Name,
	})
	if err := registry.Register(mysqlPlugin); err != nil {
		return PluginSet{}, fmt.Errorf("register mysql plugin: %w", err)
	}
	if err := registry.Register(redisPlugin); err != nil {
		return PluginSet{}, fmt.Errorf("register redis plugin: %w", err)
	}
	cacheDependencies := []string(nil)
	if cfg.Cache.Enabled && strings.TrimSpace(cfg.Cache.Store) == config.CacheStoreRedis {
		cacheDependencies = []string{redisPlugin.Name()}
	}
	if err := registry.Register(cachePlugin, cacheDependencies...); err != nil {
		return PluginSet{}, fmt.Errorf("register cache plugin: %w", err)
	}

	return PluginSet{
		Database: mysqlPlugin,
		Redis:    redisPlugin,
		Cache:    cachePlugin,
	}, nil
}
