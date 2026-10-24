package bootstrap

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	"github.com/gtkit/gin-api/internal/pkg/tasks"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/testutil"
	"github.com/redis/go-redis/v9"
)

func TestSchedulerDrainTimeoutPreservesSharedResources(t *testing.T) {
	testutil.RequireLoopback(t)
	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr(), ReadTimeout: 5 * time.Second})
	var resourceCloses, extensionCloses atomic.Int32
	t.Cleanup(func() {
		if resourceCloses.Load() == 0 {
			if err := client.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	entered, release := make(chan struct{}), make(chan struct{})
	var blocked atomic.Bool
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	// Hold one real queue script response while the cron job is in flight.
	srv.Server().SetPreHook(func(_ *server.Peer, command string, args ...string) bool {
		if strings.EqualFold(command, "evalsha") || strings.EqualFold(command, "eval") {
			for _, arg := range args[2:] {
				if strings.HasPrefix(arg, "asynq:") && blocked.CompareAndSwap(false, true) {
					close(entered)
					<-release
					break
				}
			}
		}
		return false
	})
	scheduler, err := tasks.NewScheduler(config.TasksConfig{
		Enabled: true, ShutdownTimeout: 20 * time.Millisecond,
		Scheduler: config.SchedulerConfig{Enabled: true},
	}, client, "blocked-scheduler", tasks.PeriodicTask{Spec: "@every 1s", TaskType: "test:drain"})
	if err != nil {
		t.Fatal(err)
	}
	budget := testWorkerBudget()
	// A premature Run return must not be hidden by a longer fallback Cleanup wait.
	budget.Cleanup = 10 * time.Millisecond
	manager, err := NewWorkerManager(budget, newAsynqSchedulerWorker(scheduler))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	manager.runMu.Lock()
	done := manager.running[KindProducer][0].done
	manager.runMu.Unlock()
	t.Cleanup(func() {
		unblock()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		if err := manager.StopAll(ctx); err != nil {
			t.Error(err)
		}
		select {
		case <-done:
		case <-ctx.Done():
			t.Error("scheduler goroutine did not exit after releasing the test server")
		}
	})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("periodic enqueue did not reach the controlled TCP server")
	}
	stopCtx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	stopErr := manager.StopKind(stopCtx, KindProducer)
	cancel()
	if !errors.Is(stopErr, ErrWorkerNotExited) {
		t.Fatalf("StopKind prematurely confirmed scheduler exit: %v", stopErr)
	}
	owned := &lifecycleComponent{stop: func(context.Context) error { resourceCloses.Add(1); return client.Close() }}
	cleanup := &providerCleanup{}
	cleanup.push("shared extension", cleanupExtensions, func(context.Context) error { extensionCloses.Add(1); return nil })
	app := &App{workers: manager, cleanup: cleanup, runtime: &Runtime{owners: &runtimeResources{redis: owned}}}
	if err := app.closeExtensions(t.Context()); !errors.Is(err, ErrWorkerNotExited) || extensionCloses.Load() != 0 {
		t.Fatalf("extension released during scheduler drain: err=%v closes=%d", err, extensionCloses.Load())
	}
	if err := app.closeResources(t.Context()); !errors.Is(err, ErrWorkerNotExited) || resourceCloses.Load() != 0 {
		t.Fatalf("shared Redis released during scheduler drain: err=%v closes=%d", err, resourceCloses.Load())
	}
	if err := manager.CleanupRemaining(t.Context()); err != nil {
		t.Fatal(err)
	}
	unblock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("late scheduler exit was not collected")
	}
	if len(manager.LiveWorkers()) != 0 {
		t.Fatalf("late exit tracking: live=%v", manager.LiveWorkers())
	}
	if err := app.closeExtensions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := app.closeResources(t.Context()); err != nil {
		t.Fatal(err)
	}
	if resourceCloses.Load() != 1 || extensionCloses.Load() != 1 {
		t.Fatalf("late cleanup counts: resources=%d extensions=%d", resourceCloses.Load(), extensionCloses.Load())
	}
}
