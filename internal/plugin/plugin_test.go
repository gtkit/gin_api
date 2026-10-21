package plugin

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/ormx"
	"github.com/gtkit/redisx"
	"github.com/redis/go-redis/v9"
	gormlogger "gorm.io/gorm/logger"
)

func TestMySQLPluginLifecycle(t *testing.T) {
	t.Parallel()

	sqlDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	client, err := ormx.OpenWithDB(t.Context(), sqlDB,
		ormx.WithSkipInitializeWithVersion(true),
		ormx.WithServerVersion("8.0.36"),
		ormx.WithStartupPing(false),
	)
	if err != nil {
		t.Fatalf("ormx.OpenWithDB: %v", err)
	}

	called := false
	plugin := NewMySQLPlugin(MySQLOptions{
		Open: func(context.Context, config.DatabaseConfig, config.DBCredentials, gormlogger.Interface) (*ormx.Client, error) {
			called = true
			return client, nil
		},
	})
	if err := plugin.Init(t.Context()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !called || plugin.DB() == nil || plugin.SQLDB() == nil {
		t.Fatalf("plugin did not publish the injected client: called=%v db=%v sql=%v", called, plugin.DB(), plugin.SQLDB())
	}
	if err := plugin.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := plugin.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if plugin.DB() != nil || plugin.SQLDB() != nil {
		t.Fatal("Stop left the owned client published")
	}
	if err := plugin.Stop(t.Context()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

func TestMySQLPluginRejectsNilAndPropagatesOpenError(t *testing.T) {
	t.Parallel()

	openErr := errors.New("open failed")
	plugin := NewMySQLPlugin(MySQLOptions{
		Open: func(context.Context, config.DatabaseConfig, config.DBCredentials, gormlogger.Interface) (*ormx.Client, error) {
			return nil, openErr
		},
	})
	if err := plugin.Init(t.Context()); !errors.Is(err, openErr) {
		t.Fatalf("Init error = %v, want open error", err)
	}

	nilPlugin := NewMySQLPlugin(MySQLOptions{
		Open: func(context.Context, config.DatabaseConfig, config.DBCredentials, gormlogger.Interface) (*ormx.Client, error) {
			return nil, nil
		},
	})
	if err := nilPlugin.Init(t.Context()); err == nil {
		t.Fatal("Init accepted a nil client")
	}
}

func TestMySQLPluginZeroValueAndCancellationContracts(t *testing.T) {
	t.Parallel()

	plugin := NewMySQLPlugin(MySQLOptions{})
	if plugin.Name() != "mysql" {
		t.Fatalf("Name() = %q, want mysql", plugin.Name())
	}
	if err := plugin.Start(t.Context()); err == nil {
		t.Fatal("Start accepted an uninitialized plugin")
	}
	if err := plugin.PingContext(t.Context()); err == nil {
		t.Fatal("PingContext accepted an uninitialized plugin")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := plugin.Init(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Init error = %v, want context.Canceled", err)
	}
	if err := plugin.Stop(t.Context()); err != nil {
		t.Fatalf("Stop without a client: %v", err)
	}
}

func TestRedisPluginOptionalFailureDegrades(t *testing.T) {
	t.Parallel()

	openErr := errors.New("redis unavailable")
	plugin := NewRedisPlugin(RedisOptions{
		Config:   config.RedisConfig{Addr: "redis:6379"},
		Required: false,
		Open: func(context.Context, config.RedisConfig) (*redisx.Client, error) {
			return nil, openErr
		},
	})
	if err := plugin.Init(t.Context()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !plugin.Configured() || plugin.DefaultClient() != nil {
		t.Fatal("optional failure did not preserve configured/degraded state")
	}
	if plugin.Availability() == nil || plugin.Availability().State() != data.AvailabilityDegraded {
		t.Fatalf("availability = %v, want degraded", plugin.Availability())
	}
	if err := plugin.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
}

func TestRedisPluginRequiredFailureIsReturned(t *testing.T) {
	t.Parallel()

	openErr := errors.New("redis unavailable")
	plugin := NewRedisPlugin(RedisOptions{
		Config:   config.RedisConfig{Addr: "redis:6379"},
		Required: true,
		Open: func(context.Context, config.RedisConfig) (*redisx.Client, error) {
			return nil, openErr
		},
	})
	if err := plugin.Init(t.Context()); !errors.Is(err, openErr) {
		t.Fatalf("Init error = %v, want open error", err)
	}
	if err := plugin.Start(t.Context()); err == nil {
		t.Fatal("Start accepted a required plugin without a client")
	}
}

func TestRedisPluginUnavailableAccessorsAreSafe(t *testing.T) {
	t.Parallel()

	plugin := NewRedisPlugin(RedisOptions{})
	if plugin.Name() != "redis" {
		t.Fatalf("Name() = %q, want redis", plugin.Name())
	}
	if plugin.Client(0) != nil {
		t.Fatal("Client returned a handle before Init")
	}
	if client, ok := plugin.GetClient(0); client != nil || ok {
		t.Fatalf("GetClient = (%v, %t), want (nil, false)", client, ok)
	}
	if plugin.PoolStats() != nil {
		t.Fatal("PoolStats returned data before Init")
	}
	if plugin.DefaultClient() != nil {
		t.Fatal("DefaultClient returned a handle before Init")
	}
	if err := plugin.HealthCheck(t.Context()); err == nil {
		t.Fatal("HealthCheck accepted an unavailable plugin")
	}
	if err := plugin.Ping(t.Context()); err == nil {
		t.Fatal("Ping accepted an unavailable plugin")
	}
	if plugin.Configured() {
		t.Fatal("unconfigured plugin reported Configured")
	}
	if plugin.Availability() != nil {
		t.Fatal("uninitialized plugin published availability")
	}
	if err := plugin.Stop(t.Context()); err != nil {
		t.Fatalf("Stop without a client: %v", err)
	}
}

func TestRedisPluginUnconfiguredRequiredAndCanceled(t *testing.T) {
	t.Parallel()

	required := NewRedisPlugin(RedisOptions{Required: true})
	if err := required.Init(t.Context()); err == nil {
		t.Fatal("required unconfigured Redis accepted")
	}
	if required.Availability() == nil {
		t.Fatal("required unconfigured Redis did not publish degraded state")
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	optional := NewRedisPlugin(RedisOptions{})
	if err := optional.Init(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Init error = %v, want context.Canceled", err)
	}
}

type cacheRedisProvider struct {
	configured bool
}

func (p cacheRedisProvider) Client(int) *redis.Client { return nil }

func (p cacheRedisProvider) DefaultClient() *redis.Client { return nil }

func (p cacheRedisProvider) Configured() bool { return p.configured }

func TestCachePluginMemoryLifecycle(t *testing.T) {
	t.Parallel()

	plugin := NewCachePlugin(CacheOptions{
		Config: config.CacheConfig{
			Enabled:  true,
			Store:    config.CacheStoreMemory,
			MaxBytes: 1 << 20,
		},
		AppName: "test",
	})
	if err := plugin.Init(t.Context()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !plugin.Enabled() || plugin.Stager() == nil {
		t.Fatal("cache plugin is not enabled after Init")
	}
	if err := plugin.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := plugin.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if plugin.Stager() != nil || plugin.Enabled() {
		t.Fatal("Stop left a closed cache stager published")
	}
	if err := plugin.Stop(t.Context()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

func TestCachePluginRedisFailureDegrades(t *testing.T) {
	t.Parallel()

	plugin := NewCachePlugin(CacheOptions{
		Config: config.CacheConfig{Enabled: true, Store: config.CacheStoreRedis},
		Redis:  cacheRedisProvider{configured: true},
	})
	if err := plugin.Init(t.Context()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if plugin.Stager() == nil || plugin.Enabled() {
		t.Fatal("cache plugin did not disable itself after optional Redis degradation")
	}
}

func TestCachePluginZeroValueAndMiddlewareContracts(t *testing.T) {
	t.Parallel()

	var nilPlugin *CachePlugin
	if nilPlugin.Name() != "cache" {
		t.Fatalf("Name() = %q, want cache", nilPlugin.Name())
	}
	if err := nilPlugin.Init(t.Context()); err == nil {
		t.Fatal("nil cache plugin accepted Init")
	}
	if err := nilPlugin.Start(t.Context()); err == nil {
		t.Fatal("nil cache plugin accepted Start")
	}
	if err := nilPlugin.Stop(t.Context()); err != nil {
		t.Fatalf("nil cache plugin Stop: %v", err)
	}

	plugin := NewCachePlugin(CacheOptions{})
	if err := plugin.Start(t.Context()); err == nil {
		t.Fatal("Start accepted a cache plugin before Init")
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	plugin.Middleware(time.Second)(c)
	if !c.IsAborted() {
		// The disabled middleware is a pass-through; reaching this point proves
		// the returned handler executed without touching a nil stager.
		return
	}
	t.Fatal("disabled cache middleware unexpectedly aborted the request")
}

func TestCachePluginRejectsRedisStoreWithoutProvider(t *testing.T) {
	t.Parallel()

	plugin := NewCachePlugin(CacheOptions{
		Config: config.CacheConfig{Enabled: true, Store: config.CacheStoreRedis},
	})
	if err := plugin.Init(t.Context()); err == nil {
		t.Fatal("Redis cache accepted a missing provider")
	}
}
