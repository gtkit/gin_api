package responsecache

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/testutil"
	"github.com/redis/go-redis/v9"
)

type ownedStore struct {
	closes atomic.Int32
	err    error
}

func (*ownedStore) Enabled() bool                            { return true }
func (*ownedStore) Middleware(time.Duration) gin.HandlerFunc { return passthrough }
func (s *ownedStore) Close() error                           { s.closes.Add(1); return s.err }
func TestFactoryFailureOwnership(t *testing.T) {
	for _, tc := range []struct {
		name                                                    string
		partial, cancel, nilStore, typedNil, closeError, panics bool
	}{
		{name: "success"}, {name: "normal close error", closeError: true}, {name: "partial", partial: true}, {name: "cancellation", cancel: true}, {name: "nil", nilStore: true}, {name: "typed nil", typedNil: true}, {name: "close error", partial: true, closeError: true}, {name: "panic", panics: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, closeErr := errors.New("store failed"), errors.New("close failed")
			owned := new(ownedStore)
			if tc.closeError {
				owned.err = closeErr
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			p, err := Open(ctx, Options{Config: &config.Config{}, Factory: func(config.CacheConfig, *redis.Client, string) (Store, error) {
				calls++
				if tc.cancel {
					cancel()
				}
				if tc.panics {
					panic("factory")
				}
				if tc.nilStore {
					return nil, nil
				}
				if tc.typedNil {
					var absent *ownedStore
					return absent, nil
				}
				if tc.partial {
					return owned, root
				}
				return owned, nil
			}})
			wantErr := tc.partial || tc.cancel || tc.nilStore || tc.typedNil || tc.panics
			if (err != nil) != wantErr {
				t.Fatalf("Open=%v", err)
			}
			if wantErr && p != nil {
				t.Fatal("failed owner published")
			}
			if tc.partial && !errors.Is(err, root) {
				t.Fatal(err)
			}
			if tc.cancel && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if tc.partial && tc.closeError && !errors.Is(err, closeErr) {
				t.Fatal(err)
			}
			if (tc.nilStore || tc.typedNil || tc.panics) && !errors.Is(err, core.ErrComponentState) {
				t.Fatal(err)
			}
			if p != nil {
				var group sync.WaitGroup
				for range 16 {
					group.Go(func() {
						closeResult := p.Close(context.WithoutCancel(t.Context()))
						if tc.closeError {
							if !errors.Is(closeResult, closeErr) {
								t.Errorf("lost close error: %v", closeResult)
							}
						} else if closeResult != nil {
							t.Error(closeResult)
						}
					})
				}
				group.Wait()
				if p.Enabled() {
					t.Fatal("closed store enabled")
				}
			}
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
			if !tc.nilStore && !tc.typedNil && !tc.panics && owned.closes.Load() != 1 {
				t.Fatalf("closes=%d", owned.closes.Load())
			}
		})
	}
}
func TestConfigurationFailuresAndCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		if canceled {
			cancel()
		}
		p, err := Open(ctx, Options{Factory: func(config.CacheConfig, *redis.Client, string) (Store, error) {
			t.Fatal("store opened without config")
			return nil, nil
		}})
		cancel()
		if p != nil || err == nil {
			t.Fatal("invalid input accepted")
		}
		if canceled && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
}

type redisView struct {
	client     *redis.Client
	configured bool
	wantDB     int
	t          *testing.T
}

func (p redisView) Client(db int) *redis.Client {
	if db != p.wantDB {
		p.t.Fatalf("Redis DB=%d want=%d", db, p.wantDB)
	}
	return p.client
}
func (p redisView) DefaultClient() *redis.Client { return p.client }
func (p redisView) Configured() bool             { return p.configured }

type forbiddenRedis struct{ t *testing.T }

func (p forbiddenRedis) Client(int) *redis.Client {
	p.t.Fatal("memory/disabled cache accessed Redis")
	return nil
}
func (p forbiddenRedis) DefaultClient() *redis.Client {
	p.t.Fatal("cache requested default Redis DB")
	return nil
}
func (p forbiddenRedis) Configured() bool {
	p.t.Fatal("cache queried unrelated Redis config")
	return false
}
func TestMemoryAndDisabledHaveNoRedisDependency(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "memory"}[enabled], func(t *testing.T) {
			p := mustOpen(t, Options{Config: &config.Config{Cache: config.CacheConfig{Enabled: enabled, Store: config.CacheStoreMemory, MaxBytes: 1 << 20}}, Redis: forbiddenRedis{t}})
			t.Cleanup(func() {
				if err := p.Close(context.WithoutCancel(t.Context())); err != nil {
					t.Errorf("Stop: %v", err)
				}
			})
			if p.Enabled() != enabled {
				t.Fatal("enabled changed")
			}
		})
	}
}
func TestRedisStoreBorrowingPrefixTTLAndAdmission(t *testing.T) {
	testutil.RequireLoopback(t)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), DB: 3})
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("Close Redis: %v", err)
		}
	})
	cfg := &config.Config{Cache: config.CacheConfig{Enabled: true, Store: config.CacheStoreRedis, RedisDB: 3, TTL: 7 * time.Second}}
	cfg.App.Name = "same-application"
	cfg.Redis.Prefix = "contract"
	provider := new(lateRedis)
	p := mustOpen(t, Options{Config: cfg, Redis: provider})
	t.Cleanup(func() {
		if err := p.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	engine := gin.New()
	calls := 0
	engine.GET("/public", p.Middleware(3*time.Second), func(c *gin.Context) { calls++; c.String(http.StatusOK, "public") })
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/public?b=2&a=1", nil))
	if calls != 1 || p.Enabled() {
		t.Fatal("waiting cache did not pass through")
	}
	provider.current.Store(client)
	for range 2 {
		engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/public?b=2&a=1", nil))
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	request := httptest.NewRequest(http.MethodGet, "/public?b=2&a=1", nil)
	request.Header.Set("App-Token", "identity-sentinel")
	engine.ServeHTTP(httptest.NewRecorder(), request)
	if calls != 3 {
		t.Fatal("identity request hit public cache")
	}
	keys, err := client.Keys(t.Context(), "contract:httpcache:*").Result()
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys=%v err=%v", keys, err)
	}
	if !strings.HasPrefix(keys[0], "contract:httpcache:") {
		t.Fatal("cache namespace drift")
	}
	ttl, err := client.TTL(t.Context(), keys[0]).Result()
	if err != nil || ttl != 3*time.Second {
		t.Fatalf("TTL=%v err=%v", ttl, err)
	}
	if err := p.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Fatal("cache closed borrowed Redis:", err)
	}
}
