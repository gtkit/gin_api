package responsecache

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/redis/go-redis/v9"
)

type cacheRedisProvider struct{ configured bool }

func (p cacheRedisProvider) Client(int) *redis.Client     { return nil }
func (p cacheRedisProvider) DefaultClient() *redis.Client { return nil }
func (p cacheRedisProvider) Configured() bool             { return p.configured }
func TestMemoryOwner(t *testing.T) {
	t.Parallel()
	p := mustOpen(t, Options{Config: &config.Config{Cache: config.CacheConfig{Enabled: true, Store: config.CacheStoreMemory, MaxBytes: 1 << 20}, App: config.AppConfig{Name: "test"}}})
	if !p.Enabled() {
		t.Fatal("memory cache disabled")
	}
	if err := p.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if p.Enabled() {
		t.Fatal("closed store enabled")
	}
}
func TestRedisFailureDegrades(t *testing.T) {
	t.Parallel()
	p := mustOpen(t, Options{Config: &config.Config{Cache: config.CacheConfig{Enabled: true, Store: config.CacheStoreRedis}}, Redis: cacheRedisProvider{configured: true}})
	if p.store == nil || p.Enabled() {
		t.Fatal("optional Redis failure did not disable cache")
	}
}
func TestNilAndEmptyOwnerMiddleware(t *testing.T) {
	t.Parallel()
	for _, p := range []*Owner{nil, new(Owner)} {
		if err := p.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		p.Middleware(time.Second)(c)
		if c.IsAborted() {
			t.Fatal("passthrough aborted request")
		}
	}
}
func TestRedisStoreRequiresProvider(t *testing.T) {
	t.Parallel()
	p, err := Open(t.Context(), Options{Config: &config.Config{Cache: config.CacheConfig{Enabled: true, Store: config.CacheStoreRedis}}})
	if err == nil || p != nil {
		t.Fatal("Redis cache accepted missing provider")
	}
}
func mustOpen(t *testing.T, options Options) *Owner {
	t.Helper()
	p, err := Open(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	return p
}
