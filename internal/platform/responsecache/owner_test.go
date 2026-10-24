package responsecache

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/core"
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
	if p.store != nil || p.Enabled() || !p.waiting {
		t.Fatal("optional Redis failure did not keep a waiting cache")
	}
}

type lateRedis struct{ current atomic.Pointer[redis.Client] }

func (p *lateRedis) Client(int) *redis.Client     { return p.current.Load() }
func (p *lateRedis) DefaultClient() *redis.Client { return p.current.Load() }
func (*lateRedis) Configured() bool               { return true }

type countedStore struct {
	ownedStore
	middlewareCalls atomic.Int32
}

func (s *countedStore) Middleware(time.Duration) gin.HandlerFunc {
	s.middlewareCalls.Add(1)
	return passthrough
}

func TestRecoveredCacheSharesStoreAndRetainsEachRouteMiddleware(t *testing.T) {
	provider := new(lateRedis)
	var factories atomic.Int32
	store := new(countedStore)
	p := mustOpen(t, Options{
		Config: &config.Config{Cache: config.CacheConfig{Enabled: true, Store: config.CacheStoreRedis}}, Redis: provider,
		Factory: func(config.CacheConfig, *redis.Client, string) (Store, error) { factories.Add(1); return store, nil },
	})
	engine := gin.New()
	for _, path := range []string{"/one", "/two"} {
		engine.GET(path, p.Middleware(time.Second), func(c *gin.Context) { c.Status(http.StatusOK) })
	}
	client := redis.NewClient(&redis.Options{Addr: "unused"})
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	provider.current.Store(client)
	var requests sync.WaitGroup
	for range 16 {
		requests.Go(func() {
			for _, path := range []string{"/one", "/two"} {
				engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
			}
		})
	}
	requests.Wait()
	if factories.Load() != 1 || store.middlewareCalls.Load() != 2 {
		t.Fatalf("store factories=%d route middleware factories=%d", factories.Load(), store.middlewareCalls.Load())
	}
	if err := p.Close(t.Context()); err != nil || store.closes.Load() != 1 {
		t.Fatalf("close=%v count=%d", err, store.closes.Load())
	}
}

// TestCacheReadPathsShareOwnerLock 区分功能正确与读路径确实不需要写锁。
// 先固定持有一个读锁，再分别调用 Enabled 和已挂载路由：另一个读者可以
// 完成，取写锁的旧实现则只能等测试释放锁。超时仅作为失败上界，不用于猜测
// goroutine 是否已经运行；失败后也先释放锁并接回结果，避免遗留请求。
func TestCacheReadPathsShareOwnerLock(t *testing.T) {
	for _, state := range []string{"waiting", "ready"} {
		t.Run(state, func(t *testing.T) {
			provider := new(lateRedis)
			store := new(countedStore)
			p := mustOpen(t, Options{
				Config: &config.Config{Cache: config.CacheConfig{Enabled: true, Store: config.CacheStoreRedis}},
				Redis:  provider,
				Factory: func(config.CacheConfig, *redis.Client, string) (Store, error) {
					return store, nil
				},
			})
			if state == "ready" {
				client := redis.NewClient(&redis.Options{Addr: "unused"})
				t.Cleanup(func() {
					if err := client.Close(); err != nil {
						t.Error(err)
					}
				})
				provider.current.Store(client)
			}
			engine := gin.New()
			engine.GET("/cache", p.Middleware(time.Second), func(c *gin.Context) {
				c.Status(http.StatusNoContent)
			})
			request := func() bool {
				recorder := httptest.NewRecorder()
				engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/cache", nil))
				return recorder.Code == http.StatusNoContent
			}
			if !request() {
				t.Fatal("initial request failed")
			}
			for _, operation := range []struct {
				name string
				run  func() bool
			}{
				{name: "enabled", run: func() bool { return p.Enabled() == (state == "ready") }},
				{name: "middleware", run: request},
			} {
				t.Run(operation.name, func(t *testing.T) {
					done := make(chan bool, 1)
					p.mu.RLock()
					go func() { done <- operation.run() }()
					var passed, blocked bool
					select {
					case passed = <-done:
					case <-time.After(time.Second):
						blocked = true
					}
					p.mu.RUnlock()
					if blocked {
						<-done
						t.Fatal("read path waited for the Owner write lock")
					}
					if !passed {
						t.Fatal("read path changed the response or enabled state")
					}
				})
			}
		})
	}
}

func TestLateFactoryFailureIsTerminalAndClosesPartialStore(t *testing.T) {
	for _, mode := range []string{"error", "typed-nil", "panic"} {
		t.Run(mode, func(t *testing.T) {
			provider := new(lateRedis)
			client := redis.NewClient(&redis.Options{Addr: "unused"})
			t.Cleanup(func() {
				if err := client.Close(); err != nil {
					t.Error(err)
				}
			})
			root, closing := errors.New("factory failed"), errors.New("partial store close failed")
			store := &ownedStore{err: closing}
			calls := 0
			p, err := Open(t.Context(), Options{
				Config: &config.Config{Cache: config.CacheConfig{Enabled: true, Store: config.CacheStoreRedis}},
				Redis:  provider,
				Factory: func(cfg config.CacheConfig, got *redis.Client, _ string) (Store, error) {
					calls++
					if !cfg.Enabled || got != client {
						t.Error("late factory lost config or borrowed client")
					}
					switch mode {
					case "typed-nil":
						var absent *ownedStore
						return absent, nil
					case "panic":
						panic("factory")
					default:
						return store, root
					}
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 0 {
				t.Fatal("factory called before Redis was available")
			}
			engine := gin.New()
			handlers := 0
			engine.GET("/cache", p.Middleware(time.Second), func(c *gin.Context) { handlers++; c.Status(http.StatusOK) })
			provider.current.Store(client)
			for range 2 {
				engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/cache", nil))
			}
			if calls != 1 || handlers != 2 || p.Enabled() {
				t.Fatalf("calls=%d handlers=%d", calls, handlers)
			}
			closeErr := p.Close(t.Context())
			if mode == "error" {
				if !errors.Is(closeErr, root) || !errors.Is(closeErr, closing) || store.closes.Load() != 1 {
					t.Fatalf("close=%v count=%d", closeErr, store.closes.Load())
				}
			} else if !errors.Is(closeErr, core.ErrComponentState) {
				t.Fatalf("close=%v", closeErr)
			}
		})
	}
}

func TestWaitingCacheCannotInitializeAfterClose(t *testing.T) {
	provider := new(lateRedis)
	p := mustOpen(t, Options{
		Config: &config.Config{Cache: config.CacheConfig{Enabled: true, Store: config.CacheStoreRedis}}, Redis: provider,
		Factory: func(config.CacheConfig, *redis.Client, string) (Store, error) {
			t.Error("late factory ran after close")
			return new(ownedStore), nil
		},
	})
	handler := p.Middleware(time.Second)
	if err := p.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(&redis.Options{Addr: "unused"})
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	provider.current.Store(client)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	handler(c)
	if p.Enabled() {
		t.Fatal("closed cache became enabled")
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
