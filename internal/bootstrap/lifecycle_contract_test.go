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
	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/plugin"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type lifecycleComponent struct {
	sql         *sql.DB
	build       func(plugin.MiddlewareDeps) ([]gin.HandlerFunc, error)
	name        string
	events      *[]string
	initialized bool
	cfg         *config.Config
	valueErr    error
	db          *gorm.DB
	init        func(context.Context) error
	start       func(context.Context) error
	stop        func(context.Context) error
}

func (p *lifecycleComponent) Name() string { return p.name }
func (p *lifecycleComponent) Init(ctx context.Context) error {
	if p.events != nil {
		*p.events = append(*p.events, "init:"+p.name)
	}
	p.initialized = true
	if p.init != nil {
		return p.init(ctx)
	}
	return ctx.Err()
}
func (p *lifecycleComponent) Start(ctx context.Context) error {
	if p.events != nil {
		*p.events = append(*p.events, "start:"+p.name)
	}
	if p.start != nil {
		return p.start(ctx)
	}
	return ctx.Err()
}
func (p *lifecycleComponent) Stop(ctx context.Context) error {
	if p.events != nil {
		*p.events = append(*p.events, "stop:"+p.name)
	}
	if p.stop != nil {
		return p.stop(ctx)
	}
	return ctx.Err()
}
func (p *lifecycleComponent) Value() (*config.Config, error) {
	if !p.initialized {
		return nil, plugin.ErrConfigurationUnavailable
	}
	return p.cfg, p.valueErr
}
func (*lifecycleComponent) Log(context.Context, applog.LogEntry)  {}
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
func (p *lifecycleComponent) Build(deps plugin.MiddlewareDeps) ([]gin.HandlerFunc, error) {
	if p.build != nil {
		return p.build(deps)
	}
	return []gin.HandlerFunc{func(c *gin.Context) { c.Next() }}, nil
}

type lifecycleFixture struct {
	events     []string
	components []*lifecycleComponent
}

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	cfg := jwtTestConfig(t)
	f := &lifecycleFixture{}
	for _, name := range []string{"config", "logging", "database", "redis", "cache", "middleware"} {
		f.components = append(f.components, &lifecycleComponent{name: name, events: &f.events, cfg: cfg, db: &gorm.DB{}})
	}
	return f
}
func (f *lifecycleFixture) register(r *core.PluginRegistry) (PluginSet, error) {
	for i, p := range f.components {
		var deps []string
		if i > 0 {
			deps = []string{f.components[i-1].Name()}
		}
		if err := r.Register(p, deps...); err != nil {
			return PluginSet{}, err
		}
	}
	p := f.components
	return PluginSet{Configuration: p[0], Logging: p[1], Database: p[2], Redis: p[3], Cache: p[4], Middleware: p[5], DrainLogs: p[1].Stop}, nil
}
func (f *lifecycleFixture) migration(r *core.PluginRegistry) (MigrationPluginSet, error) {
	for i, p := range f.components[:3] {
		var deps []string
		if i > 0 {
			deps = []string{f.components[i-1].Name()}
		}
		if err := r.Register(p, deps...); err != nil {
			return MigrationPluginSet{}, err
		}
	}
	return MigrationPluginSet{Configuration: f.components[0], Logging: f.components[1], Database: f.components[2], DrainLogs: f.components[1].Stop}, nil
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
		t.Fatalf("stopped = %v, want %v; ledger=%v", got, names, f.events)
	}
}

func TestRuntimeUsesExplicitCapabilitiesAndStopsOwnersOnce(t *testing.T) {
	f := newLifecycleFixture(t)
	r, err := NewRuntime(t.Context(), Options{RegisterPlugins: f.register})
	if err != nil {
		t.Fatal(err)
	}
	if r.Config() != f.components[0].cfg || r.Resources().Mdb() != f.components[2].db || r.CachePlugin() != f.components[4] || r.middleware != f.components[5] {
		t.Fatal("runtime did not borrow the selected instances")
	}
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.assertStopped(t, "middleware", "cache", "redis", "database", "logging", "config")
}

func TestRuntimeFailureKeepsCauseAndAllCleanupErrors(t *testing.T) {
	for _, stage := range []string{"init", "start", "config", "jwt"} {
		t.Run(stage, func(t *testing.T) {
			f := newLifecycleFixture(t)
			root, closing := errors.New("startup failure"), errors.New("close failure")
			f.components[1].stop = func(context.Context) error { return closing }
			switch stage {
			case "init":
				f.components[3].init = func(context.Context) error { return root }
			case "start":
				f.components[3].start = func(context.Context) error { return root }
			case "config":
				f.components[0].valueErr = root
			case "jwt":
				original := initJWTWithConfig
				t.Cleanup(func() { initJWTWithConfig = original })
				initJWTWithConfig = func(*config.Config) (*jwtauth.Assembly, error) { return nil, root }
			}
			runtime, err := NewRuntime(t.Context(), Options{RegisterPlugins: f.register})
			if runtime != nil || !errors.Is(err, root) || !errors.Is(err, closing) {
				t.Fatalf("failure chain lost: %v", err)
			}
			if stage == "init" {
				f.assertStopped(t, "redis", "database", "logging", "config")
			} else {
				f.assertStopped(t, "middleware", "cache", "redis", "database", "logging", "config")
			}
		})
	}
}

func TestRuntimeClosePreservesParentAndSharedBudget(t *testing.T) {
	for _, mode := range []string{"total", "shorter", "canceled", "force"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				registry := core.NewPluginRegistry()
				force := make(chan struct{})
				if err := registry.SetForceSignal(force); err != nil {
					t.Fatal(err)
				}
				var calls atomic.Int32
				var deadline time.Time
				for _, name := range []string{"a", "b", "c"} {
					p := &lifecycleComponent{name: name, stop: func(ctx context.Context) error {
						calls.Add(1)
						d, ok := ctx.Deadline()
						if !ok {
							t.Error("no shutdown deadline")
						}
						if deadline.IsZero() {
							deadline = d
						} else if d != deadline {
							t.Error("budget renewed per plugin")
						}
						<-ctx.Done()
						return ctx.Err()
					}}
					if err := registry.Register(p); err != nil {
						t.Fatal(err)
					}
				}
				if err := registry.Start(t.Context()); err != nil {
					t.Fatal(err)
				}
				runtime := &Runtime{plugins: registry}
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
				start := time.Now()
				err := runtime.Close(ctx)
				if err == nil || time.Since(start) != duration || calls.Load() != 3 {
					t.Fatalf("close=%v elapsed=%v calls=%d", err, time.Since(start), calls.Load())
				}
				if err2 := runtime.Close(t.Context()); err2 != err && err2.Error() != err.Error() {
					t.Fatalf("result changed: %v / %v", err, err2)
				}
			})
		})
	}
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
		if err := r.DrainLogs(t.Context()); !errors.Is(err, core.ErrPluginContractViolation) {
			t.Fatalf("drain panic=%v", err)
		}
	}
}

func TestConcurrentRuntimeCloseStopsEachOwnerOnce(t *testing.T) {
	f := newLifecycleFixture(t)
	r, err := NewRuntime(t.Context(), Options{RegisterPlugins: f.register})
	if err != nil {
		t.Fatal(err)
	}
	var callers sync.WaitGroup
	for range 32 {
		callers.Go(func() {
			if err := r.Close(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	callers.Wait()
	f.assertStopped(t, "middleware", "cache", "redis", "database", "logging", "config")
}

func TestNewAppRollsBackProvidersAndPluginsAfterMiddlewareFailure(t *testing.T) {
	f := newLifecycleFixture(t)
	cfg := f.components[0].cfg
	cfg.Auth.Mode = config.AuthModeExternal
	cfg.Modules.AI = config.AIModuleConfig{}
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	gormDB, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database := f.components[2]
	database.db, database.sql = gormDB, sqlDB
	root, closing := errors.New("middleware build failed"), errors.New("database close failed")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var rollbackDeadline time.Time
	f.components[5].build = func(deps plugin.MiddlewareDeps) ([]gin.HandlerFunc, error) {
		if deps.Metrics == nil {
			t.Error("middleware built before final metrics")
		}
		cancel()
		return nil, root
	}
	database.stop = func(ctx context.Context) error {
		if ctx.Err() != nil {
			t.Error("startup cancellation leaked into provider rollback")
		}
		rollbackDeadline, _ = ctx.Deadline()
		return errors.Join(sqlDB.Close(), closing)
	}
	mock.ExpectClose()
	app, err := NewApp(ctx, Options{RegisterPlugins: f.register})
	if app != nil || !errors.Is(err, root) || !errors.Is(err, closing) {
		t.Fatalf("assembly=%v error=%v", app, err)
	}
	if remaining := time.Until(rollbackDeadline); remaining <= 0 || remaining > 30*time.Second {
		t.Fatalf("rollback budget=%v", remaining)
	}
	f.assertStopped(t, "middleware", "cache", "redis", "database", "logging", "config")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
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

func TestRuntimeFailureUsesRemainingOuterRollbackBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newLifecycleFixture(t)
		root := errors.New("configuration view failed")
		f.components[0].valueErr = root
		budget := &rollbackBudget{}
		defer budget.Close()
		outer := budget.Context(t.Context())
		deadline, _ := outer.Deadline()
		time.Sleep(20 * time.Second)
		f.components[5].stop = func(ctx context.Context) error {
			got, _ := ctx.Deadline()
			if got != deadline {
				t.Error("runtime reset outer rollback deadline")
			}
			<-ctx.Done()
			return ctx.Err()
		}
		begin := time.Now()
		_, err := newRuntime(t.Context(), Options{RegisterPlugins: f.register}, budget)
		if !errors.Is(err, root) || !errors.Is(err, context.DeadlineExceeded) || time.Since(begin) != 10*time.Second {
			t.Fatalf("rollback=%v elapsed=%v", err, time.Since(begin))
		}
		f.assertStopped(t, "middleware", "cache", "redis", "database", "logging", "config")
	})
}
