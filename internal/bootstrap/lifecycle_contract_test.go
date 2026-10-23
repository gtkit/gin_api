package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/pkg/jwtauth"
	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type lifecycleComponent struct {
	sql       *sql.DB
	name      string
	events    *[]string
	cfg       *config.Config
	db        *gorm.DB
	init      func(context.Context) error
	stop      func(context.Context) error
	closeOnce sync.Once
	closeErr  error
}

func (p *lifecycleComponent) open(ctx context.Context) error {
	if p.events != nil {
		*p.events = append(*p.events, "open:"+p.name)
	}
	if p.init != nil {
		return p.init(ctx)
	}
	return ctx.Err()
}
func (p *lifecycleComponent) Close(ctx context.Context) error {
	p.closeOnce.Do(func() {
		if p.events != nil {
			*p.events = append(*p.events, "stop:"+p.name)
		}
		if p.stop != nil {
			p.closeErr = p.stop(ctx)
		} else {
			p.closeErr = ctx.Err()
		}
	})
	return p.closeErr
}
func (p *lifecycleComponent) Drain(ctx context.Context) error     { return p.Close(ctx) }
func (p *lifecycleComponent) DB() *gorm.DB                        { return p.db }
func (p *lifecycleComponent) SQLDB() *sql.DB                      { return p.sql }
func (*lifecycleComponent) PingContext(ctx context.Context) error { return ctx.Err() }
func (*lifecycleComponent) Client(int) *redis.Client              { return nil }
func (*lifecycleComponent) DefaultClient() *redis.Client          { return nil }
func (*lifecycleComponent) Configured() bool                      { return false }
func (*lifecycleComponent) GetClient(int) (*redis.Client, bool)   { return nil, false }
func (*lifecycleComponent) PoolStats() map[int]*redis.PoolStats   { return nil }
func (*lifecycleComponent) HealthCheck(ctx context.Context) error { return ctx.Err() }
func (*lifecycleComponent) Enabled() bool                         { return false }
func (*lifecycleComponent) Middleware(time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) { c.Next() }
}

type lifecycleFixture struct {
	events     []string
	components []*lifecycleComponent
}

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	cfg := jwtTestConfig(t)
	f := &lifecycleFixture{}
	for _, name := range []string{"configuration", "logging", "database", "redis", "cache"} {
		f.components = append(f.components, &lifecycleComponent{name: name, events: &f.events, cfg: cfg, db: &gorm.DB{}})
	}
	return f
}
func (f *lifecycleFixture) factories() runtimeFactories {
	p := f.components
	return runtimeFactories{
		loadConfig: func(ctx context.Context) (*config.Config, error) { return p[0].cfg, p[0].open(ctx) },
		logs:       func(ctx context.Context, _ *config.Config) (logOwner, error) { return p[1], p[1].open(ctx) },
		database: func(ctx context.Context, _ *config.Config, _ bool) (databaseOwner, error) {
			return p[2], p[2].open(ctx)
		},
		redis: func(ctx context.Context, _ *config.Config) (redisOwner, error) { return p[3], p[3].open(ctx) },
		cache: func(ctx context.Context, _ *config.Config, _ data.RedisClientProvider) (cacheOwner, error) {
			return p[4], p[4].open(ctx)
		},
	}
}
func (f *lifecycleFixture) options() Options {
	factories := f.factories()
	return Options{factories: &factories}
}
func (f *lifecycleFixture) migrationOptions() MigrationOptions {
	factories := f.factories()
	return MigrationOptions{factories: &factories}
}
func (f *lifecycleFixture) assertStopped(t *testing.T, names ...string) {
	t.Helper()
	var got []string
	for _, event := range f.events {
		if len(event) > 5 && event[:5] == "stop:" {
			got = append(got, event[5:])
		}
	}
	if !slices.Equal(got, names) {
		t.Fatalf("stopped=%v want=%v ledger=%v", got, names, f.events)
	}
}

func TestRuntimeOpensFixedResourcesAndClosesOwnersOnce(t *testing.T) {
	f := newLifecycleFixture(t)
	r, err := NewRuntime(t.Context(), f.options())
	if err != nil {
		t.Fatal(err)
	}
	if r.Config() != f.components[0].cfg || r.Resources().Mdb() != f.components[2].db || r.Cache() != f.components[4] {
		t.Fatal("runtime did not borrow opened resources")
	}
	want := []string{"open:configuration", "open:logging", "open:database", "open:redis", "open:cache"}
	if !slices.Equal(f.events, want) {
		t.Fatalf("open order=%v", f.events)
	}
	for range 2 {
		if err := r.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	f.assertStopped(t, "cache", "redis", "database", "logging")
}
func TestRuntimeFailureKeepsCauseAndAllCleanupErrors(t *testing.T) {
	for _, stage := range []int{1, 2, 3, 4} {
		t.Run([]string{"logging", "database", "redis", "cache"}[stage-1], func(t *testing.T) {
			f := newLifecycleFixture(t)
			root, closing := errors.New("startup failure"), errors.New("close failure")
			f.components[stage].init = func(context.Context) error { return root }
			f.components[1].stop = func(context.Context) error { return closing }
			r, err := NewRuntime(t.Context(), f.options())
			if r != nil || !errors.Is(err, root) || !errors.Is(err, closing) {
				t.Fatalf("failure chain=%v", err)
			}
			names := []string{"logging", "database", "redis", "cache"}[:stage]
			slices.Reverse(names)
			f.assertStopped(t, names...)
		})
	}
}
func TestRuntimeConfigurationFailureOpensNothing(t *testing.T) {
	f := newLifecycleFixture(t)
	root := errors.New("invalid config")
	f.components[0].init = func(context.Context) error { return root }
	if r, err := NewRuntime(t.Context(), f.options()); r != nil || !errors.Is(err, root) {
		t.Fatal(err)
	}
	if !slices.Equal(f.events, []string{"open:configuration"}) {
		t.Fatal(f.events)
	}
}
func TestRuntimeJWTFailsAndRollsBackResources(t *testing.T) {
	f := newLifecycleFixture(t)
	root := errors.New("jwt failed")
	original := initJWTWithConfig
	t.Cleanup(func() { initJWTWithConfig = original })
	initJWTWithConfig = func(*config.Config) (*jwtauth.Assembly, error) { return nil, root }
	if r, err := NewRuntime(t.Context(), f.options()); r != nil || !errors.Is(err, root) {
		t.Fatal(err)
	}
	f.assertStopped(t, "cache", "redis", "database", "logging")
}
func TestRuntimeClosePreservesParentAndSharedBudget(t *testing.T) {
	for _, mode := range []string{"total", "shorter", "canceled", "force"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				force := make(chan struct{})
				var calls atomic.Int32
				var deadline time.Time
				makeOwner := func() *lifecycleComponent {
					return &lifecycleComponent{stop: func(ctx context.Context) error {
						calls.Add(1)
						d, ok := ctx.Deadline()
						if !ok {
							t.Error("missing shutdown deadline")
						}
						if deadline.IsZero() {
							deadline = d
						} else if d != deadline {
							t.Error("budget renewed per owner")
						}
						<-ctx.Done()
						return ctx.Err()
					}}
				}
				r := &Runtime{owners: &runtimeResources{force: force, cache: makeOwner(), redis: makeOwner(), database: makeOwner()}}
				ctx := t.Context()
				duration := 30 * time.Second
				if mode == "shorter" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
					defer cancel()
					duration = 2 * time.Second
				}
				if mode == "canceled" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
					duration = 0
				}
				if mode == "force" {
					close(force)
					duration = 0
				}
				begin := time.Now()
				err := r.Close(ctx)
				if err == nil || time.Since(begin) != duration || calls.Load() != 3 {
					t.Fatalf("close=%v elapsed=%v calls=%d", err, time.Since(begin), calls.Load())
				}
			})
		})
	}
}
func TestConcurrentRuntimeCloseSharesResultAndWaiterCanCancel(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	failure := errors.New("close failed")
	var calls atomic.Int32
	owner := &lifecycleComponent{stop: func(context.Context) error { calls.Add(1); close(entered); <-release; return failure }}
	r := &Runtime{owners: &runtimeResources{database: owner}}
	done := make(chan error, 1)
	go func() { done <- r.Close(t.Context()) }()
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, failure) {
		t.Fatal(err)
	}
	var callers sync.WaitGroup
	for range 32 {
		callers.Go(func() {
			if err := r.Close(t.Context()); !errors.Is(err, failure) {
				t.Error(err)
			}
		})
	}
	callers.Wait()
	if calls.Load() != 1 {
		t.Fatalf("closes=%d", calls.Load())
	}
}
func TestNewAppRollsBackResourcesAfterRouterFailure(t *testing.T) {
	f := newLifecycleFixture(t)
	cfg := f.components[0].cfg
	cfg.Auth.Mode = config.AuthModeExternal
	cfg.Modules.AI = config.AIModuleConfig{}
	cfg.Proxy.TrustedProxies = []string{"invalid proxy"}
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	f.components[2].db, f.components[2].sql = db, sqlDB
	closing := errors.New("database close failed")
	var deadline time.Time
	f.components[2].stop = func(ctx context.Context) error {
		if ctx.Err() != nil {
			t.Error("startup cancellation leaked into rollback")
		}
		deadline, _ = ctx.Deadline()
		return errors.Join(sqlDB.Close(), closing)
	}
	mock.ExpectClose()
	if app, err := NewApp(t.Context(), f.options()); app != nil || !errors.Is(err, closing) {
		t.Fatalf("app=%v err=%v", app, err)
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > 30*time.Second {
		t.Fatalf("rollback remaining=%v", remaining)
	}
	f.assertStopped(t, "cache", "redis", "database", "logging")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestRuntimeFailureUsesRemainingOuterRollbackBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newLifecycleFixture(t)
		root := errors.New("redis failed")
		f.components[3].init = func(context.Context) error { return root }
		budget := &rollbackBudget{}
		defer budget.Close()
		outer := budget.Context(t.Context())
		deadline, _ := outer.Deadline()
		time.Sleep(20 * time.Second)
		f.components[3].stop = func(ctx context.Context) error {
			d, _ := ctx.Deadline()
			if d != deadline {
				t.Error("rollback deadline reset")
			}
			<-ctx.Done()
			return ctx.Err()
		}
		begin := time.Now()
		_, err := newRuntime(t.Context(), f.options(), budget)
		if !errors.Is(err, root) || !errors.Is(err, context.DeadlineExceeded) || time.Since(begin) != 10*time.Second {
			t.Fatalf("error=%v elapsed=%v", err, time.Since(begin))
		}
	})
}

func TestDrainLogsCooperativeBudgetAndForce(t *testing.T) {
	for _, mode := range []string{"total", "shorter", "canceled", "force"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				force := make(chan struct{})
				var calls atomic.Int32
				runtime := &Runtime{force: force, drain: func(ctx context.Context) error { calls.Add(1); <-ctx.Done(); return ctx.Err() }}
				ctx := t.Context()
				duration := 30 * time.Second
				if mode == "shorter" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, time.Second)
					defer cancel()
					duration = time.Second
				}
				if mode == "canceled" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
					duration = 0
				}
				if mode == "force" {
					close(force)
					duration = 0
				}
				start := time.Now()
				if err := runtime.DrainLogs(ctx); err == nil {
					t.Fatal("cancellation lost")
				}
				if time.Since(start) != duration || calls.Load() != 1 {
					t.Fatalf("elapsed=%v calls=%d", time.Since(start), calls.Load())
				}
				if err := runtime.DrainLogs(t.Context()); err == nil || calls.Load() != 1 {
					t.Fatal("drain result not shared")
				}
			})
		})
	}
}

func TestConcurrentDrainSharesOwnerAndWaiterMayCancel(t *testing.T) {
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	failure := errors.New("drain failure")
	r := &Runtime{drain: func(context.Context) error { calls.Add(1); close(entered); <-release; return failure }}
	done := make(chan error, 1)
	go func() { done <- r.DrainLogs(t.Context()) }()
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.DrainLogs(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter=%v", err)
	}
	var waiters sync.WaitGroup
	for range 32 {
		waiters.Go(func() {
			if err := r.DrainLogs(t.Context()); !errors.Is(err, failure) {
				t.Errorf("drain=%v", err)
			}
		})
	}
	close(release)
	if err := <-done; !errors.Is(err, failure) {
		t.Fatal(err)
	}
	waiters.Wait()
	if calls.Load() != 1 {
		t.Fatalf("owners=%d", calls.Load())
	}
}

func TestRollbackBudgetDetachesOnlyStartupAndIsShared(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		type key struct{}
		parent, cancel := context.WithCancel(context.WithValue(t.Context(), key{}, "kept"))
		cancel()
		budget := &rollbackBudget{}
		defer budget.Close()
		first := budget.Context(parent)
		if first.Err() != nil || first.Value(key{}) != "kept" {
			t.Fatal("startup cancel or values propagated incorrectly")
		}
		deadline, _ := first.Deadline()
		time.Sleep(20 * time.Second)
		second := budget.Context(parent)
		d, _ := second.Deadline()
		if first != second || deadline != d {
			t.Fatal("rollback budget renewed")
		}
		<-second.Done()
		if !errors.Is(second.Err(), context.DeadlineExceeded) {
			t.Fatal("rollback not bounded")
		}
	})
}

func TestDrainPanicIsPublishedToWaiters(t *testing.T) {
	r := &Runtime{drain: func(context.Context) error { panic("broken logging adapter") }}
	for range 2 {
		if err := r.DrainLogs(t.Context()); !errors.Is(err, core.ErrComponentState) {
			t.Fatalf("drain panic=%v", err)
		}
	}
}

func TestConcurrentFirstDrainAdmissionHasOneOwner(t *testing.T) {
	var calls atomic.Int32
	r := &Runtime{drain: func(context.Context) error { calls.Add(1); return nil }}
	start := make(chan struct{})
	var callers sync.WaitGroup
	for range 64 {
		callers.Go(func() {
			<-start
			if err := r.DrainLogs(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	close(start)
	callers.Wait()
	if calls.Load() != 1 {
		t.Fatalf("first drain owners=%d", calls.Load())
	}
}
