package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/plugin"
	defaultcache "github.com/gtkit/gin-api/internal/plugin/builtin/cache"
	defaultconfig "github.com/gtkit/gin-api/internal/plugin/builtin/config"
	defaulthttp "github.com/gtkit/gin-api/internal/plugin/builtin/httpmiddleware"
	defaultlogging "github.com/gtkit/gin-api/internal/plugin/builtin/logging"
	defaultmysql "github.com/gtkit/gin-api/internal/plugin/builtin/mysql"
	defaultredis "github.com/gtkit/gin-api/internal/plugin/builtin/redis"
)

type nameOnlyPlugin string

func (p nameOnlyPlugin) Name() string              { return string(p) }
func (nameOnlyPlugin) Init(context.Context) error  { return nil }
func (nameOnlyPlugin) Start(context.Context) error { return nil }
func (nameOnlyPlugin) Stop(context.Context) error  { return nil }

func TestRegisterServerPluginsReturnsSelectedUninitializedCapabilities(t *testing.T) {
	r := core.NewPluginRegistry()
	set, err := registerServerPlugins(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := set.Configuration.(*defaultconfig.Plugin); !ok {
		t.Fatal("configuration type")
	}
	if _, ok := set.Logging.(*defaultlogging.Plugin); !ok {
		t.Fatal("logging type")
	}
	if _, ok := set.Database.(*defaultmysql.Plugin); !ok {
		t.Fatal("database type")
	}
	if _, ok := set.Redis.(*defaultredis.Plugin); !ok {
		t.Fatal("redis type")
	}
	if _, ok := set.Cache.(*defaultcache.Plugin); !ok {
		t.Fatal("cache type")
	}
	if _, ok := set.Middleware.(*defaulthttp.Plugin); !ok {
		t.Fatal("middleware type")
	}
	if _, err := set.Configuration.Value(); !errors.Is(err, plugin.ErrConfigurationUnavailable) {
		t.Fatal("registration loaded configuration")
	}
	if set.Database.DB() != nil || set.Redis.DefaultClient() != nil || set.Cache.Enabled() {
		t.Fatal("registration opened resources")
	}
	if set.DrainLogs == nil {
		t.Fatal("logging owner drain missing")
	}
	for _, name := range []string{"config", "logging", "mysql", "redis", "cache", "http-middleware"} {
		if err := r.Register(nameOnlyPlugin(name)); err == nil {
			t.Fatalf("%s capability not registered", name)
		}
	}
}

func TestRegisterMigrationPluginsIsMinimalAndLazy(t *testing.T) {
	r := core.NewPluginRegistry()
	set, err := registerMigrationPlugins(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set.Configuration.Value(); !errors.Is(err, plugin.ErrConfigurationUnavailable) {
		t.Fatal("migration registration loaded configuration")
	}
	if set.Database.DB() != nil || set.DrainLogs == nil {
		t.Fatal("migration opened resources or lost drain")
	}
	for _, name := range []string{"config", "logging", "mysql"} {
		if err := r.Register(nameOnlyPlugin(name)); err == nil {
			t.Fatalf("%s not registered", name)
		}
	}
	for _, name := range []string{"redis", "cache", "http-middleware"} {
		if err := r.Register(nameOnlyPlugin(name)); err != nil {
			t.Fatalf("unrelated plugin %s registered: %v", name, err)
		}
	}
}
func TestDefaultRegistrarsRejectNilRegistry(t *testing.T) {
	if _, err := registerServerPlugins(nil); err == nil {
		t.Fatal("nil server registry accepted")
	}
	if _, err := registerMigrationPlugins(nil); err == nil {
		t.Fatal("nil migration registry accepted")
	}
}
