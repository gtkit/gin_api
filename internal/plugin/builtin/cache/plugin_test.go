package cache

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/redis/go-redis/v9"
)

type cacheRedisProvider struct {
	configured bool
}

func (p cacheRedisProvider) Client(int) *redis.Client { return nil }

func (p cacheRedisProvider) DefaultClient() *redis.Client { return nil }

func (p cacheRedisProvider) Configured() bool { return p.configured }

func TestCachePluginMemoryLifecycle(t *testing.T) {
	t.Parallel()

	plugin := New(Options{
		Configuration: testConfiguration{config.Config{Cache: config.CacheConfig{Enabled: true, Store: config.CacheStoreMemory, MaxBytes: 1 << 20}, App: config.AppConfig{Name: "test"}}},
	})
	if err := plugin.Init(t.Context()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !plugin.Enabled() || plugin.store == nil {
		t.Fatal("cache plugin is not enabled after Init")
	}
	if err := plugin.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := plugin.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if plugin.store != nil || plugin.Enabled() {
		t.Fatal("Stop left a closed cache stager published")
	}
	if err := plugin.Stop(t.Context()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

func TestCachePluginRedisFailureDegrades(t *testing.T) {
	t.Parallel()

	plugin := New(Options{
		Configuration: testConfiguration{config.Config{Cache: config.CacheConfig{Enabled: true, Store: config.CacheStoreRedis}}},
		Redis:         cacheRedisProvider{configured: true},
	})
	if err := plugin.Init(t.Context()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if plugin.store == nil || plugin.Enabled() {
		t.Fatal("cache plugin did not disable itself after optional Redis degradation")
	}
}

func TestCachePluginZeroValueAndMiddlewareContracts(t *testing.T) {
	t.Parallel()

	var nilPlugin *Plugin
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

	plugin := New(Options{})
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

	plugin := New(Options{
		Configuration: testConfiguration{config.Config{Cache: config.CacheConfig{Enabled: true, Store: config.CacheStoreRedis}}},
	})
	if err := plugin.Init(t.Context()); err == nil {
		t.Fatal("Redis cache accepted a missing provider")
	}
}

type testConfiguration struct{ cfg config.Config }

func (c testConfiguration) Value() (*config.Config, error) { return &c.cfg, nil }
