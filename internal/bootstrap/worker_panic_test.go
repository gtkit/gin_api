package bootstrap

import (
	"context"
	"errors"
	"testing"
)

func startWithoutPanic(t *testing.T, manager *WorkerManager) (err error) {
	t.Helper()
	defer func() {
		if value := recover(); value != nil {
			t.Errorf("worker phase panic escaped the manager: %v", value)
		}
	}()
	return manager.Start(context.Background())
}

func TestPreparePanicRollsBackPreparedWorkers(t *testing.T) {
	t.Parallel()
	first := &funcWorker{name: "first", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true}
	broken := &funcWorker{name: "broken", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
		prepare: func(context.Context) error { panic("prepare panic") }}
	manager, err := NewWorkerManager(testWorkerBudget(), first, broken)
	if err != nil {
		t.Fatal(err)
	}
	err = startWithoutPanic(t, manager)
	if failure, ok := errors.AsType[*WorkerPanicError](err); !ok || failure.Worker != "broken" || len(failure.Stack) == 0 {
		t.Errorf("prepare panic was lost: %v", err)
	}
	if got := first.cleanupCalls.Load(); got != 1 {
		t.Errorf("prepared worker cleanup count = %d, want 1", got)
	}
}

func TestCleanupPanicPreservesStartupFailureAndContinuesRollback(t *testing.T) {
	t.Parallel()
	startupErr := errors.New("prepare failed")
	first := &funcWorker{name: "first", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true}
	broken := &funcWorker{name: "broken", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
		cleanup: func(context.Context) error { panic("cleanup panic") }}
	last := &funcWorker{name: "last", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
		prepare: func(context.Context) error { return startupErr }}
	manager, err := NewWorkerManager(testWorkerBudget(), first, broken, last)
	if err != nil {
		t.Fatal(err)
	}
	err = startWithoutPanic(t, manager)
	if !errors.Is(err, startupErr) {
		t.Errorf("startup error was lost: %v", err)
	}
	if failure, ok := errors.AsType[*WorkerPanicError](err); !ok || failure.Worker != "broken" || len(failure.Stack) == 0 {
		t.Errorf("cleanup panic was lost: %v", err)
	}
	if first.cleanupCalls.Load() != 1 || broken.cleanupCalls.Load() != 1 {
		t.Errorf("rollback stopped early: first=%d broken=%d", first.cleanupCalls.Load(), broken.cleanupCalls.Load())
	}
}
