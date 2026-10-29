package bootstrap

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Connection identities are sufficient here; real protocol checks are separate.
type namedTestResource struct {
	*lifecycleComponent
	clients   map[int]*redis.Client
	closes    atomic.Int32
	healthErr error
}

func (r *namedTestResource) PingContext(ctx context.Context) error {
	return errors.Join(r.healthErr, ctx.Err())
}
func (r *namedTestResource) HealthCheck(ctx context.Context) error {
	return errors.Join(r.healthErr, ctx.Err())
}

func (r *namedTestResource) Close(ctx context.Context) error {
	r.closes.Add(1)
	return r.lifecycleComponent.Close(ctx)
}
func (r *namedTestResource) GetClient(db int) (*redis.Client, bool) {
	client, ok := r.clients[db]
	return client, ok
}
func (r *namedTestResource) PoolStats() map[int]*redis.PoolStats {
	result := make(map[int]*redis.PoolStats, len(r.clients))
	for db := range r.clients {
		result[db] = new(redis.PoolStats)
	}
	return result
}

func namedConnectionFixture(t *testing.T) (*lifecycleFixture, runtimeFactories, map[string]*namedTestResource) {
	t.Helper()
	f := newLifecycleFixture(t)
	cfg := f.components[0].cfg
	cfg.Database.Name = "default"
	cfg.DatabaseConnections = map[string]config.DatabaseConfig{
		"archive": {Name: "archive", Host: "archive.local"},
		"reports": {Name: "reports", Host: "reports.local"},
	}
	cfg.RedisConnections = map[string]config.RedisConfig{
		"jobs":     {Addr: "jobs.local:6379", DBs: []int{0}},
		"sessions": {Addr: "sessions.local:6380", DBs: []int{1}},
	}
	owners := make(map[string]*namedTestResource)
	for _, name := range []string{"database[archive]", "database[reports]", "redis[jobs]", "redis[sessions]"} {
		owners[name] = &namedTestResource{lifecycleComponent: &lifecycleComponent{
			name: name, events: &f.events, db: new(gorm.DB),
		}, clients: map[int]*redis.Client{0: new(redis.Client), 1: new(redis.Client)}}
	}
	factories := f.factories()
	factories.database = func(ctx context.Context, selected *config.Config, _ bool) (databaseOwner, error) {
		if selected.Database.Name == "default" {
			return f.components[2], f.components[2].open(ctx)
		}
		if selected.Database.Host != selected.Database.Name+".local" {
			t.Error("named database did not receive its own configuration")
		}
		owner := owners["database["+selected.Database.Name+"]"]
		return owner, owner.open(ctx)
	}
	factories.redis = func(ctx context.Context, selected *config.Config) (redisOwner, error) {
		name := "jobs"
		if selected.Redis.Addr == cfg.Redis.Addr {
			return f.components[3], f.components[3].open(ctx)
		}
		if selected.Redis.Addr == "sessions.local:6380" {
			name = "sessions"
		}
		owner := owners["redis["+name+"]"]
		return owner, owner.open(ctx)
	}
	return f, factories, owners
}

func TestNamedConnectionsLookupAndOwnership(t *testing.T) {
	f, factories, owners := namedConnectionFixture(t)
	runtime, err := NewRuntime(t.Context(), Options{factories: &factories})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			for _, name := range []string{"archive", "reports"} {
				db, err := runtime.Database(name)
				if err != nil || db != owners["database["+name+"]"].db {
					t.Errorf("wrong database for %s: %v", name, err)
				}
			}
			client, err := runtime.Redis("sessions", 1)
			if err != nil || client != owners["redis[sessions]"].clients[1] {
				t.Errorf("wrong Redis connection: %v", err)
			}
		})
	}
	workers.Wait()
	checker, ok := newHealthChecker(runtime.Resources(), runtime.owners).(*runtimeHealthChecker)
	if !ok {
		t.Fatal("unexpected health checker")
	}
	checks := checker.NamedConnectionChecks()
	if len(checks) != 4 {
		t.Fatalf("named health checks=%d", len(checks))
	}
	failure := errors.New("named dependency down")
	owners["redis[sessions]"].healthErr = failure
	if err := checks["redis/sessions"](t.Context()); !errors.Is(err, failure) {
		t.Fatal("health check did not use the named owner")
	}
	if db, err := runtime.Database(config.DefaultConnection); err != nil || db != f.components[2].db {
		t.Fatal("default database changed")
	}
	if db, err := runtime.Database("missing"); err == nil || db != nil {
		t.Fatal("unknown database fell back to default")
	}
	if client, err := runtime.Redis("missing", 0); err == nil || client != nil {
		t.Fatal("unknown Redis fell back to default")
	}
	if client, err := runtime.Redis("sessions", 2); err == nil || client != nil {
		t.Fatal("unknown Redis DB fell back to another DB")
	}
	if client, err := runtime.Redis("sessions", 0); err == nil || client != nil {
		t.Fatal("implicitly initialized DB0 bypassed the declared DB set")
	}
	if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	f.assertStopped(t, "cache", "redis[sessions]", "redis[jobs]", "redis", "database[reports]", "database[archive]", "database", "logging")
	for name, owner := range owners {
		if owner.closes.Load() != 1 {
			t.Errorf("%s closed %d times", name, owner.closes.Load())
		}
	}
}

func TestNamedConnectionFailureRollsBack(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "open error", true: "canceled"}[canceled], func(t *testing.T) {
			f, factories, owners := namedConnectionFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("connection failed")
			closeFailure := errors.New("connection close failed")
			owners["redis[jobs]"].stop = func(context.Context) error { return closeFailure }
			owners["redis[sessions]"].init = func(context.Context) error {
				if canceled {
					cancel()
					return nil
				}
				return failure
			}
			runtime, err := NewRuntime(ctx, Options{factories: &factories})
			want := failure
			if canceled {
				want = context.Canceled
			}
			if runtime != nil || !errors.Is(err, want) || !errors.Is(err, closeFailure) {
				t.Fatalf("runtime=%v error=%v", runtime, err)
			}
			f.assertStopped(t, "redis[sessions]", "redis[jobs]", "redis", "database[reports]", "database[archive]", "database", "logging")
		})
	}
}

func TestMigrationDoesNotOpenNamedConnections(t *testing.T) {
	f, factories, owners := namedConnectionFixture(t)
	runtime, err := NewMigrationRuntime(t.Context(), MigrationOptions{factories: &factories})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.events, []string{"open:configuration", "open:logging", "open:database", "stop:database", "stop:logging"}) {
		t.Fatalf("migration acquired extra resources: %v", f.events)
	}
	for _, owner := range owners {
		if owner.closes.Load() != 0 {
			t.Fatal("migration acquired a named runtime connection")
		}
	}
}
