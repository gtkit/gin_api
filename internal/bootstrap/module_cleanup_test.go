package bootstrap

import (
	"errors"
	"testing"
)

func TestAppClosesModulesAndPreservesCleanupError(t *testing.T) {
	t.Parallel()
	cause := errors.New("module cleanup failed")
	var modules, metrics int
	app := &App{
		moduleCleanup:  func() error { modules++; return cause },
		metricsCleanup: []func() error{func() error { metrics++; return nil }},
	}
	if err := app.closeExtensions(t.Context()); !errors.Is(err, cause) {
		t.Fatalf("cleanup error = %v", err)
	}
	if err := app.closeExtensions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if modules != 1 || metrics != 1 {
		t.Fatalf("cleanup calls: modules=%d metrics=%d", modules, metrics)
	}
}

func TestModuleCleanupSkipsLiveWorkers(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	worker := &runningWorker{entry: &preparedWorker{worker: &funcWorker{name: "busy"}}, done: done}
	manager := &WorkerManager{stuck: []*runningWorker{worker}}
	calls := 0
	app := &App{workers: manager, moduleCleanup: func() error { calls++; return nil }}
	if err := app.closeModules(); !errors.Is(err, ErrWorkerNotExited) || calls != 0 {
		t.Fatalf("live worker cleanup: calls=%d err=%v", calls, err)
	}
	close(done)
	if err := app.closeModules(); err != nil || calls != 1 {
		t.Fatalf("exited worker cleanup: calls=%d err=%v", calls, err)
	}
}
