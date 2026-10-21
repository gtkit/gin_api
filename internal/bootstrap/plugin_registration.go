package bootstrap

import (
	"context"
	"errors"
	"reflect"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/plugin"
)

// PluginSet contains borrowed capabilities selected and registered by the entry point.
// Every capability must belong to a plugin in the supplied registry.
type PluginSet struct {
	Configuration plugin.ConfigurationProvider
	Logging       plugin.Logger
	Database      plugin.DatabaseProvider
	Redis         plugin.RedisProvider
	Cache         plugin.ResponseCache
	Middleware    plugin.MiddlewareBuilder
	// DrainLogs must use the same logging owner as registry Stop. It is used only
	// when a live worker prevents stopping the remaining plugins.
	DrainLogs func(context.Context) error
}

// PluginRegistrar selects implementations at compile time. Registration must not
// read configuration or create resources; those operations belong to Plugin.Init.
type PluginRegistrar func(*core.PluginRegistry) (PluginSet, error)

func (s PluginSet) validate() error {
	if nilCapability(s.Configuration) || nilCapability(s.Logging) || nilCapability(s.Database) ||
		nilCapability(s.Redis) || nilCapability(s.Cache) || nilCapability(s.Middleware) || s.DrainLogs == nil {
		return errors.New("register plugins: configuration, logging, database, redis, cache, middleware and logging drain are required")
	}
	return nil
}

// nilCapability handles typed nils at the registration boundary, never on request paths.
func nilCapability(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
