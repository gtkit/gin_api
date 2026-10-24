package bootstrap

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestAppClosesExtensionsAndPreservesCleanupError(t *testing.T) {
	t.Parallel()
	cause := errors.New("task producer cleanup failed")
	var producer, metrics int
	cleanup := &providerCleanup{}
	cleanup.push("task producer", cleanupExtensions, func(context.Context) error { producer++; return cause })
	cleanup.push("metrics", cleanupMetrics, func(context.Context) error { metrics++; return nil })
	app := &App{cleanup: cleanup}
	if err := app.closeExtensions(t.Context()); !errors.Is(err, cause) {
		t.Fatalf("cleanup error = %v", err)
	}
	if err := app.closeExtensions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if producer != 1 || metrics != 1 {
		t.Fatalf("cleanup calls: producer=%d metrics=%d", producer, metrics)
	}
	if !errors.Is(app.extensionsErr, cause) {
		t.Fatalf("first cleanup error was overwritten: %v", app.extensionsErr)
	}
}

func TestExtensionCleanupSkipsLiveWorkers(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	worker := &runningWorker{entry: &preparedWorker{worker: &funcWorker{name: "busy"}}, done: done}
	manager := &WorkerManager{stuck: []*runningWorker{worker}}
	var calls []string
	app := &App{
		workers: manager,
		cleanup: newRecordingProviderCleanup(func(_ context.Context, name string) error {
			calls = append(calls, name)
			return nil
		}),
	}
	if err := app.closeExtensions(t.Context()); !errors.Is(err, ErrWorkerNotExited) || len(calls) != 0 || app.extClosed.Load() || len(app.cleanup.steps) != 6 {
		t.Fatalf("live worker cleanup: calls=%v consumed=%t err=%v", calls, app.extClosed.Load(), err)
	}
	close(done)
	if err := app.closeExtensions(t.Context()); err != nil || len(calls) != 6 || app.extensionsErr != nil {
		t.Fatalf("exited worker cleanup: calls=%v err=%v", calls, err)
	}
	if err := app.closeExtensions(t.Context()); err != nil || len(calls) != 6 {
		t.Fatalf("duplicate cleanup: calls=%v err=%v", calls, err)
	}
}

func TestExtensionCleanupWaitsForHTTPDrain(t *testing.T) {
	t.Parallel()
	var calls []string
	app := &App{
		httpStarted: true,
		cleanup: newRecordingProviderCleanup(func(_ context.Context, name string) error {
			calls = append(calls, name)
			return nil
		}),
	}
	if err := app.closeExtensions(t.Context()); err == nil || len(calls) != 0 || app.extClosed.Load() || len(app.cleanup.steps) != 6 {
		t.Fatalf("undrained HTTP cleanup: calls=%v consumed=%t err=%v", calls, app.extClosed.Load(), err)
	}
	app.httpDrained = true
	if err := app.closeExtensions(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := []string{"rate limits", "outbound http clients", "task producer", "telemetry", "db pool metrics", "worker metrics"}
	if !slices.Equal(calls, want) || app.extensionsErr != nil {
		t.Fatalf("drained HTTP cleanup: calls=%v err=%v", calls, app.extensionsErr)
	}
}
