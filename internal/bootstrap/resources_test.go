package bootstrap

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/platform/config"
)

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
