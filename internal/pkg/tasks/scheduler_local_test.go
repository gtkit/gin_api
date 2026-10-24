package tasks

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gtkit/asynqx"
)

func TestSchedulerPrepareAndRunUseOneInstance(t *testing.T) {
	_, client := newTestRedis(t)
	s, err := NewScheduler(schedulerConfig(), client, "local", periodicTask())
	if err != nil {
		t.Fatal(err)
	}
	inner := newControlledScheduler()
	inner.finish()
	var builds atomic.Int32
	s.newRunner = func() (schedulerRunner, error) {
		builds.Add(1)
		return inner, nil
	}
	if err := s.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(cancel)
	awaitSchedulerSignal(t, inner.started)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("Prepare and Run built %d schedulers, want one", got)
	}
}

func TestSchedulerConcurrentPrepareRegistersOnce(t *testing.T) {
	_, client := newTestRedis(t)
	s, err := NewScheduler(schedulerConfig(), client, "local", periodicTask())
	if err != nil {
		t.Fatal(err)
	}
	inner := newControlledScheduler()
	inner.finish()
	var builds, registrations atomic.Int32
	inner.registerHook = func() { registrations.Add(1) }
	s.newRunner = func() (schedulerRunner, error) {
		builds.Add(1)
		return inner, nil
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if err := s.Prepare(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if builds.Load() != 1 || registrations.Load() != 1 {
		t.Fatalf("builds=%d registrations=%d, want one each", builds.Load(), registrations.Load())
	}
	if err := s.Cleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type optionRecordingScheduler struct {
	*controlledScheduler
	options []asynqx.TaskOption
}

func (r *optionRecordingScheduler) Register(_ context.Context, _, _ string, _ any, opts ...asynqx.TaskOption) (string, error) {
	r.options = opts
	return "entry", nil
}

func TestSchedulerDoesNotAddDefaultDeduplication(t *testing.T) {
	_, client := newTestRedis(t)
	s, err := NewScheduler(schedulerConfig(), client, "local", periodicTask())
	if err != nil {
		t.Fatal(err)
	}
	inner := &optionRecordingScheduler{controlledScheduler: newControlledScheduler()}
	if err := s.register(t.Context(), inner); err != nil {
		t.Fatal(err)
	}
	if len(inner.options) != 0 {
		t.Fatalf("scheduler added %d options to a task with no options", len(inner.options))
	}
}
