package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/platform/data"
	redisowner "github.com/gtkit/gin-api/internal/platform/redis"
	"github.com/gtkit/gin-api/internal/platform/responsecache"
	"github.com/gtkit/gin-api/internal/testutil"
	"github.com/gtkit/gin-api/pkg/ratelimit"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestAppAndMigrationConfigurationFailureOpenNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start func(context.Context, *runtimeFactories) (bool, error)
	}{
		{"app", func(ctx context.Context, factories *runtimeFactories) (bool, error) {
			app, err := NewApp(ctx, Options{factories: factories})
			return app != nil, err
		}},
		{"migration", func(ctx context.Context, factories *runtimeFactories) (bool, error) {
			runtime, err := NewMigrationRuntime(ctx, MigrationOptions{factories: factories})
			return runtime != nil, err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configErr := errors.New("configuration unavailable")
			var calls []string
			unexpected := func(name string) error {
				calls = append(calls, name)
				return fmt.Errorf("unexpected resource initialization: %s", name)
			}
			factories := runtimeFactories{
				loadConfig: func(context.Context) (*config.Config, error) {
					calls = append(calls, "configuration")
					return nil, configErr
				},
				logs: func(context.Context, *config.Config) (logOwner, error) {
					return nil, unexpected("logging")
				},
				database: func(context.Context, *config.Config, bool) (databaseOwner, error) {
					return nil, unexpected("database")
				},
				redis: func(context.Context, *config.Config) (redisOwner, error) {
					return nil, unexpected("redis")
				},
				cache: func(context.Context, *config.Config, data.RedisClientProvider) (cacheOwner, error) {
					return nil, unexpected("cache")
				},
			}
			hasOwner, err := tc.start(t.Context(), &factories)
			if hasOwner || !errors.Is(err, configErr) {
				t.Fatalf("owner returned=%v, error=%v; want only configuration failure", hasOwner, err)
			}
			if !slices.Equal(calls, []string{"configuration"}) {
				t.Fatalf("factory calls=%v, want only configuration", calls)
			}
		})
	}
}

func TestRuntimeStartupSharesTotalAndPerResourceDeadlines(t *testing.T) {
	for _, mode := range []string{"single", "total"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newLifecycleFixture(t)
				want := 10 * time.Second
				if mode == "single" {
					f.components[1].init = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
				} else {
					want = 30 * time.Second
					for _, p := range f.components {
						p.init = func(ctx context.Context) error { time.Sleep(8 * time.Second); return ctx.Err() }
					}
					// The fourth acquisition has only six seconds left.
					f.components[3].init = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
				}
				begin := time.Now()
				_, err := NewRuntime(t.Context(), f.options())
				if !errors.Is(err, context.DeadlineExceeded) || time.Since(begin) != want {
					t.Fatalf("startup=%v elapsed=%v want=%v", err, time.Since(begin), want)
				}
			})
		})
	}
}

// The Redis server and sockets are outside the virtual clock; only owner and
// limiter timers advance virtually. No real sleep guesses recovery timing.
func TestOptionalRedisRecoveryReachesMountedCacheAndSharedLimiter(t *testing.T) {
	testutil.RequireLoopback(t)
	server := miniredis.RunT(t)
	cfg := &config.Config{
		Redis: config.RedisConfig{Addr: server.Addr(), DBs: []int{0, 3}, Prefix: "recovery-contract"},
		Cache: config.CacheConfig{Enabled: true, Store: config.CacheStoreRedis, RedisDB: 3, TTL: time.Minute},
	}
	client, err := redisowner.OpenRedis(t.Context(), cfg.Redis)
	if err != nil {
		t.Fatal(err)
	}
	var owner *redisowner.Owner
	t.Cleanup(func() {
		if owner == nil {
			if err := client.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		mock.ExpectClose()
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	recorder := tracetest.NewSpanRecorder()
	tracer := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tracer)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		if err := tracer.Shutdown(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		owner, err = redisowner.Open(t.Context(), redisowner.Options{Config: cfg, Open: func(context.Context, config.RedisConfig) (redisowner.Client, error) {
			if calls.Add(1) == 1 {
				return nil, errors.New("initially offline")
			}
			return client, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := owner.Close(context.WithoutCancel(t.Context())); err != nil {
				t.Error(err)
			}
		}()
		resources, err := data.ResourcesFromClients(&lifecycleComponent{db: db, sql: sqlDB}, owner, true)
		if err != nil {
			t.Fatal(err)
		}
		cache, err := responsecache.Open(t.Context(), responsecache.Options{Config: cfg, Redis: owner})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := cache.Close(context.WithoutCancel(t.Context())); err != nil {
				t.Error(err)
			}
		}()
		engine := gin.New()
		handlers := 0
		engine.GET("/cached", cache.Middleware(7*time.Second), func(c *gin.Context) { handlers++; c.String(http.StatusOK, "shared response") })
		request := func() {
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/cached", nil))
			if w.Code != http.StatusOK || w.Body.String() != "shared response" {
				t.Fatalf("response=%d %s", w.Code, w.Body.String())
			}
		}
		request()
		var backend *ratelimit.RedisBackend
		policy := ratelimit.Policy{ID: "recovery.open", Rate: 1, Burst: 2, Scope: ratelimit.ScopeIP, Store: ratelimit.StoreRedis, FailureMode: ratelimit.FailureOpen, MaxKeys: 100}
		openLimiter, err := buildLimiter(t.Context(), policy, resources, &backend, cfg.EffectiveRedisPrefix(), true)
		if err != nil {
			t.Fatal(err)
		}
		defer openLimiter.Close()
		shared := backend
		policy.ID, policy.FailureMode = "recovery.fallback", ratelimit.FailureLocalFallback
		fallback, err := buildLimiter(t.Context(), policy, resources, &backend, cfg.EffectiveRedisPrefix(), true)
		if err != nil {
			t.Fatal(err)
		}
		defer fallback.Close()
		if backend != shared {
			t.Fatal("policies did not share a backend")
		}
		if decision, err := openLimiter.Check(t.Context(), "open-key"); !decision.Allowed() || !decision.Degraded || !errors.Is(err, errRedisUnavailable) {
			t.Fatalf("fail_open initial decision=%+v err=%v, want allowed with Redis unavailable", decision, err)
		}
		// 冷却从首次失败探测开始；后面的 Owner 恢复、readiness 和 tracing
		// 验证也会经过虚拟时间，因此最后等待截止点，不能再写死剩余 25 秒。
		healthPolicy := ratelimit.DefaultHealthPolicy()
		cooldownUntil := time.Now().Add(healthPolicy.Cooldown)
		for i := range 3 {
			decision, err := fallback.Check(t.Context(), "fallback-key")
			if err != nil || !decision.Degraded || decision.Allowed() != (i < 2) {
				t.Fatalf("fallback decision=%+v err=%v", decision, err)
			}
		}
		if err := resources.InstrumentTracing("recovery"); err != nil {
			t.Fatal(err)
		}
		if err := owner.ConfigureTracing(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		// 此处的 5 秒属于 Redis Owner 的独立重试周期，不是限流健康策略。
		synctest.Sleep(5 * time.Second)
		clientDB3, exists := client.GetClient(3)
		if calls.Load() != 2 || !exists || resources.Rdb(3) != clientDB3 {
			t.Fatal("owner recovery did not reach borrowed resources")
		}
		if resources.RedisAvailability().State() != data.AvailabilityDegraded {
			t.Fatal("publication fabricated health")
		}
		for range 2 {
			if err := resources.PingRedis(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		if resources.RedisAvailability().State() != data.AvailabilityHealthy {
			t.Fatal("observations did not recover readiness")
		}
		request()
		request()
		if handlers != 2 {
			t.Fatalf("mounted cache handler calls=%d", handlers)
		}
		before := len(recorder.Ended())
		for _, db := range []int{0, 3} {
			if err := resources.Rdb(db).Set(t.Context(), "trace-contract", "private-redis-value", time.Second).Err(); err != nil {
				t.Fatal(err)
			}
		}
		sets := 0
		for _, span := range recorder.Ended()[before:] {
			if span.Name() == "set" {
				sets++
			}
			for _, attribute := range span.Attributes() {
				if strings.Contains(attribute.Value.String(), "private-redis-value") || attribute.Key == "db.statement" {
					t.Fatal("Redis tracing exposed statement data")
				}
			}
		}
		if sets != 2 {
			t.Fatalf("set spans=%d, want one per DB operation", sets)
		}
		synctest.Sleep(time.Until(cooldownUntil))
		// 共享后端按生产策略要求的连续成功次数恢复；交替使用两个策略，
		// 并用独立 key 隔离探测成功与业务桶耗尽这两种不同的结果。
		for attempt := range healthPolicy.SuccessThreshold {
			if attempt > 0 {
				synctest.Sleep(healthPolicy.ProbeInterval)
			}
			limiter := openLimiter
			if attempt%2 != 0 {
				limiter = fallback
			}
			decision, err := limiter.Check(t.Context(), fmt.Sprintf("recovered-probe-%d", attempt))
			if err != nil || !decision.Allowed() || decision.Degraded {
				t.Fatalf("recovery probe=%d decision=%+v err=%v", attempt, decision, err)
			}
		}
		// 即使未来成功阈值降为一次，也仍单独验证 fallback 已回到 Redis。
		if decision, err := fallback.Check(t.Context(), "fallback-key"); err != nil || !decision.Allowed() || decision.Degraded {
			t.Fatalf("fallback did not return to Redis: %+v %v", decision, err)
		}
		if backend.Health().Phase() != ratelimit.PhaseHealthy || calls.Load() != 2 {
			t.Fatal("backend remained degraded or owner was replaced")
		}
	})
}

func TestCanceledStartupRollsBackWithFreshBudgetAndPreservesValues(t *testing.T) {
	type key struct{}
	f := newLifecycleFixture(t)
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), key{}, "kept"))
	defer cancel()
	f.components[3].init = func(context.Context) error { cancel(); return context.Canceled }
	f.components[2].stop = func(ctx context.Context) error {
		if ctx.Err() != nil || ctx.Value(key{}) != "kept" {
			t.Error("rollback inherited cancellation or lost request values")
		}
		if d, ok := ctx.Deadline(); !ok || time.Until(d) > cleanupBudget {
			t.Error("rollback has no bounded deadline")
		}
		return nil
	}
	if _, err := NewRuntime(ctx, f.options()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	f.assertStopped(t, "redis", "database", "logging")
}

func TestResourcePanicRollsBackAndClosePanicDoesNotSkipRemainingOwners(t *testing.T) {
	f := newLifecycleFixture(t)
	factories := f.factories()
	factories.redis = func(context.Context, *config.Config) (redisOwner, error) { panic("redis constructor") }
	f.components[2].stop = func(context.Context) error { panic("database close") }
	if _, err := NewRuntime(t.Context(), Options{factories: &factories}); !errors.Is(err, core.ErrComponentState) {
		t.Fatal(err)
	}
	f.assertStopped(t, "database", "logging")
}

func TestCloseWaiterCanBeForcedWithoutStartingAnotherClose(t *testing.T) {
	entered, release, force := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	p := &lifecycleComponent{stop: func(context.Context) error { calls.Add(1); close(entered); <-release; return nil }}
	r := &runtimeResources{database: p, force: force}
	done := make(chan error, 1)
	go func() { done <- r.Close(t.Context()) }()
	<-entered
	close(force)
	if err := r.Close(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("close calls=%d", calls.Load())
	}
}

func TestDrainWaiterCanBeForcedWithoutStartingAnotherDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release, force := make(chan struct{}), make(chan struct{}), make(chan struct{})
		var calls atomic.Int32
		r := &Runtime{force: force, drain: func(context.Context) error {
			calls.Add(1)
			close(entered)
			<-release
			return nil
		}}
		first, waiter := make(chan error, 1), make(chan error, 1)
		go func() { first <- r.DrainLogs(t.Context()) }()
		<-entered
		go func() { waiter <- r.DrainLogs(t.Context()) }()
		synctest.Wait()
		close(force)
		synctest.Wait()
		select {
		case err := <-waiter:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("forced waiter=%v", err)
			}
		default:
			t.Error("second signal did not release the drain waiter")
		}
		close(release)
		if err := <-first; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if calls.Load() != 1 {
			t.Fatalf("drains=%d", calls.Load())
		}
	})
}

func TestDrainFirstResultObservesForceBeforeContextObserverRuns(t *testing.T) {
	force := make(chan struct{})
	r := &Runtime{force: force, drain: func(context.Context) error { close(force); return nil }}
	if err := r.DrainLogs(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("force signal was lost when publishing the drain result: %v", err)
	}
}
