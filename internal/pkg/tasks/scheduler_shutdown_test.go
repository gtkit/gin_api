package tasks

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gtkit/asynqx"
	"github.com/hibiken/asynq"
)

type controlledScheduler struct {
	started      chan struct{}
	stopping     chan struct{}
	waitingAgain chan struct{}
	drained      chan struct{}
	stopOnce     sync.Once
	drainOnce    sync.Once
	shutdowns    atomic.Int32
	registerErr  error
	startErr     error
	registerHook func()
}

func newControlledScheduler() *controlledScheduler {
	return &controlledScheduler{
		started: make(chan struct{}), stopping: make(chan struct{}),
		waitingAgain: make(chan struct{}), drained: make(chan struct{}),
	}
}

func (r *controlledScheduler) Register(context.Context, string, string, any, ...asynqx.TaskOption) (string, error) {
	if r.registerHook != nil {
		r.registerHook()
	}
	return "entry", r.registerErr
}

func (r *controlledScheduler) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.startErr != nil {
		return r.startErr
	}
	close(r.started)
	return nil
}

func (r *controlledScheduler) Shutdown(ctx context.Context) error {
	if r.shutdowns.Add(1) == 2 {
		close(r.waitingAgain)
	}
	r.stopOnce.Do(func() { close(r.stopping) })
	select {
	case <-r.drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *controlledScheduler) finish() { r.drainOnce.Do(func() { close(r.drained) }) }

func awaitSchedulerSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler did not reach the expected lifecycle boundary")
	}
}

func TestSchedulerShutdownTimeoutWaitsForActualExit(t *testing.T) {
	_, client := newTestRedis(t)
	cfg := schedulerConfig()
	cfg.ShutdownTimeout = 20 * time.Millisecond
	inner := newControlledScheduler()
	s, err := NewScheduler(cfg, client, "shutdown-contract", periodicTask())
	if err != nil {
		t.Fatal(err)
	}
	s.newRunner = func() (schedulerRunner, error) { return inner, nil }
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var runErr error
	go func() { defer close(done); runErr = s.Run(ctx) }()
	t.Cleanup(func() { cancel(); inner.finish(); awaitSchedulerSignal(t, done) })
	awaitSchedulerSignal(t, inner.started)
	cancel()
	awaitSchedulerSignal(t, inner.waitingAgain)
	select {
	case <-done:
		t.Fatal("Run returned while the underlying scheduler was still draining")
	default:
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
	cleanupErr := s.Cleanup(cleanupCtx)
	cleanupCancel()
	if !errors.Is(cleanupErr, context.DeadlineExceeded) {
		t.Fatalf("Cleanup during drain = %v", cleanupErr)
	}
	inner.finish()
	awaitSchedulerSignal(t, done)
	if !errors.Is(runErr, context.DeadlineExceeded) {
		t.Fatalf("Run lost the original shutdown timeout: %v", runErr)
	}
	for range 3 {
		if err := s.Cleanup(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSchedulerRejectsRepeatedRunWithoutStoppingCurrentRun(t *testing.T) {
	_, client := newTestRedis(t)
	s, err := NewScheduler(schedulerConfig(), client, "once", periodicTask())
	if err != nil {
		t.Fatal(err)
	}
	inner := newControlledScheduler()
	s.newRunner = func() (schedulerRunner, error) { return inner, nil }
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var runErr error
	go func() { defer close(done); runErr = s.Run(ctx) }()
	t.Cleanup(func() { cancel(); inner.finish(); awaitSchedulerSignal(t, done) })
	awaitSchedulerSignal(t, inner.started)
	if err := s.Run(t.Context()); err == nil {
		t.Fatal("a second Run unexpectedly started")
	}
	if got := inner.shutdowns.Load(); got != 0 {
		t.Fatalf("second Run stopped the active scheduler %d times", got)
	}
	cancel()
	inner.finish()
	awaitSchedulerSignal(t, done)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if err := s.Run(t.Context()); err == nil {
		t.Fatal("a stopped scheduler unexpectedly restarted")
	}
}

func TestSchedulerFailuresPreserveCauseAndCleanup(t *testing.T) {
	for _, phase := range []string{"build", "register", "start"} {
		t.Run(phase, func(t *testing.T) {
			_, client := newTestRedis(t)
			cfg := schedulerConfig()
			cfg.ShutdownTimeout = 10 * time.Millisecond
			s, err := NewScheduler(cfg, client, "errors", periodicTask())
			if err != nil {
				t.Fatal(err)
			}
			cause := errors.New("injected " + phase + " failure")
			inner := newControlledScheduler()
			t.Cleanup(inner.finish)
			s.newRunner = func() (schedulerRunner, error) {
				switch phase {
				case "build":
					return nil, cause
				case "register":
					inner.registerErr = cause
				case "start":
					inner.startErr = cause
				}
				return inner, nil
			}
			done := make(chan struct{})
			var runErr error
			go func() { defer close(done); runErr = s.Run(t.Context()) }()
			if phase != "build" {
				awaitSchedulerSignal(t, inner.waitingAgain)
				select {
				case <-done:
					t.Fatal("failed scheduler returned before releasing its resources")
				default:
				}
				inner.finish()
			}
			awaitSchedulerSignal(t, done)
			if !errors.Is(runErr, cause) {
				t.Fatalf("operation failure was lost: %v", runErr)
			}
			if phase != "build" && !errors.Is(runErr, context.DeadlineExceeded) {
				t.Fatalf("shutdown timeout was lost: %v", runErr)
			}
		})
	}
}

func TestPeriodicRegistrationPreservesExplicitOptions(t *testing.T) {
	srv, client := newTestRedis(t)
	entry := periodicTask()
	entry.Spec = "@every 1s"
	const uniqueTTL = 7 * time.Second
	entry.Options = []asynqx.TaskOption{
		asynqx.WithTaskQueue("explicit-options"), asynqx.WithTaskUnique(uniqueTTL),
	}
	s, err := NewScheduler(schedulerConfig(), client, "explicit-options", entry)
	if err != nil {
		t.Fatal(err)
	}
	enqueued := make(chan error, 1)
	var once sync.Once
	inner, err := asynqx.NewScheduler(asynqx.WithRedisInstance(client), asynqx.WithSchedulerPostEnqueueFunc(func(_ *asynq.TaskInfo, err error) {
		once.Do(func() { enqueued <- err })
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 3*time.Second)
		defer cancel()
		if err := inner.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	if err := s.register(t.Context(), inner); err != nil {
		t.Fatal(err)
	}
	if err := inner.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-enqueued:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("registered periodic task did not enqueue")
	}
	var uniqueKeys int
	for _, key := range srv.Keys() {
		if strings.HasPrefix(key, "asynq:{explicit-options}:unique:") {
			uniqueKeys++
			if ttl := srv.TTL(key); ttl != uniqueTTL {
				t.Errorf("registered unique TTL=%s want=%s", ttl, uniqueTTL)
			}
		}
	}
	if uniqueKeys != 1 {
		t.Fatalf("explicit queue/unique options were not preserved: keys=%v", srv.Keys())
	}
}
