package cache

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

type configValue struct {
	cfg   *config.Config
	err   error
	reads int
}

func (c *configValue) Value() (*config.Config, error) { c.reads++; return c.cfg, c.err }

type ownedStore struct {
	closes atomic.Int32
	err    error
}

func (*ownedStore) Enabled() bool                            { return true }
func (*ownedStore) Middleware(time.Duration) gin.HandlerFunc { return passthrough }
func (s *ownedStore) Close() error                           { s.closes.Add(1); return s.err }
func TestFactoryFailureOwnership(t *testing.T) {
	for _, tc := range []struct {
		name                                            string
		partial, cancel, nilStore, typedNil, closeError bool
	}{
		{name: "success"}, {name: "partial", partial: true}, {name: "cancellation", cancel: true}, {name: "nil", nilStore: true}, {name: "typed nil", typedNil: true}, {name: "close error", partial: true, closeError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := errors.New("store failed")
			closeErr := errors.New("close failed")
			owned := &ownedStore{}
			if tc.closeError {
				owned.err = closeErr
			}
			cfg := &configValue{cfg: &config.Config{}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			p := New(Options{Configuration: cfg, Factory: func(config.CacheConfig, *redis.Client, string) (Store, error) {
				calls++
				if tc.cancel {
					cancel()
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
			if calls != 0 || cfg.reads != 0 {
				t.Fatal("construction performed work")
			}
			err := p.Init(ctx)
			wantErr := tc.partial || tc.cancel || tc.nilStore || tc.typedNil
			if (err != nil) != wantErr {
				t.Fatalf("Init=%v", err)
			}
			if tc.partial && !errors.Is(err, root) {
				t.Fatal(err)
			}
			if tc.cancel && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if tc.closeError && !errors.Is(err, closeErr) {
				t.Fatal(err)
			}
			if (tc.nilStore || tc.typedNil) && !errors.Is(err, core.ErrPluginContractViolation) {
				t.Fatal(err)
			}
			_ = p.Init(t.Context())
			if calls != 1 || cfg.reads != 1 {
				t.Fatal("reconstructed store")
			}
			var group sync.WaitGroup
			for range 16 {
				group.Go(func() { _ = p.Stop(context.Background()) })
			}
			group.Wait()
			if !tc.nilStore && !tc.typedNil && owned.closes.Load() != 1 {
				t.Fatalf("closes=%d", owned.closes.Load())
			}
			if p.Enabled() || p.Start(t.Context()) == nil {
				t.Fatal("closed store remains usable")
			}
		})
	}
}
func TestConfigurationFailuresAndCancellation(t *testing.T) {
	root := errors.New("config failure")
	for _, tc := range []struct {
		name     string
		provider *configValue
		canceled bool
	}{
		{"missing", nil, false}, {"failed", &configValue{err: root}, false}, {"nil config", &configValue{}, false}, {"canceled", &configValue{cfg: &config.Config{}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := Options{Factory: func(config.CacheConfig, *redis.Client, string) (Store, error) {
				t.Fatal("store opened without config")
				return nil, nil
			}}
			if tc.provider != nil {
				opts.Configuration = tc.provider
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			p := New(opts)
			if err := p.Init(ctx); err == nil {
				t.Fatal("invalid config accepted")
			}
			_ = p.Stop(t.Context())
		})
	}
	p := New(Options{})
	_ = p.Stop(t.Context())
	if !errors.Is(p.Init(t.Context()), core.ErrPluginContractViolation) {
		t.Fatal("init after stop")
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
			p := New(Options{Configuration: testConfiguration{config.Config{Cache: config.CacheConfig{Enabled: enabled, Store: config.CacheStoreMemory, MaxBytes: 1 << 20}}}, Redis: forbiddenRedis{t}})
			if err := p.Init(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = p.Stop(context.Background()) })
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
	t.Cleanup(func() { _ = client.Close() })
	cfg := &config.Config{Cache: config.CacheConfig{Enabled: true, Store: config.CacheStoreRedis, RedisDB: 3, TTL: 7 * time.Second}}
	cfg.App.Name = "contract"
	p := New(Options{Configuration: &configValue{cfg: cfg}, Redis: redisView{client: client, configured: true, wantDB: 3, t: t}})
	if err := p.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	engine := gin.New()
	calls := 0
	engine.GET("/public", p.Middleware(3*time.Second), func(c *gin.Context) { calls++; c.String(http.StatusOK, "public") })
	for range 2 {
		engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/public?b=2&a=1", nil))
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	request := httptest.NewRequest(http.MethodGet, "/public?b=2&a=1", nil)
	request.Header.Set("App-Token", "identity-sentinel")
	engine.ServeHTTP(httptest.NewRecorder(), request)
	if calls != 2 {
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
	if err := p.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Fatal("cache closed borrowed Redis:", err)
	}
}
