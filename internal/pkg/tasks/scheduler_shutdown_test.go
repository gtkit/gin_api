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
	"github.com/gtkit/gin-api/internal/pkg/singleton"
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
	return &controlledScheduler{started: make(chan struct{}), stopping: make(chan struct{}), waitingAgain: make(chan struct{}), drained: make(chan struct{})}
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

func TestSchedulerShutdownTimeoutRetainsRoundAndLease(t *testing.T) {
	for _, loseLease := range []bool{false, true} {
		name := "parent cancellation"
		if loseLease {
			name = "lease renewal failure"
		}
		t.Run(name, func(t *testing.T) {
			srv, client := newTestRedis(t)
			cfg := schedulerConfig()
			cfg.ShutdownTimeout = 20 * time.Millisecond
			cfg.Scheduler.LeaseTTL = 90 * time.Millisecond
			inner := newControlledScheduler()
			failures := make(chan error, 8)
			lost := make(chan struct{})
			var observed, builds atomic.Int32
			s, err := NewScheduler(cfg, client, "shutdown-contract", SchedulerObserver{
				RoundFailed: func(_ string, err error) { observed.Add(1); failures <- err },
				LeaseLost:   func(string, error) { close(lost) },
			}, idempotentTask())
			if err != nil {
				t.Fatal(err)
			}
			s.newRunner = func() (schedulerRunner, error) { builds.Add(1); return inner, nil }
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			var runErr error
			go func() { defer close(done); runErr = s.Run(ctx) }()
			t.Cleanup(func() { cancel(); srv.SetError(""); inner.finish(); awaitSchedulerSignal(t, done) })
			awaitSchedulerSignal(t, inner.started)
			if loseLease {
				srv.SetError("ERR injected renewal failure")
				awaitSchedulerSignal(t, lost)
			} else {
				cancel()
			}
			select {
			case failure := <-failures:
				if !errors.Is(failure, context.DeadlineExceeded) {
					t.Fatalf("shutdown failure = %v", failure)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("first shutdown timeout was not observed")
			}
			awaitSchedulerSignal(t, inner.waitingAgain)
			select {
			case <-done:
				t.Fatal("Run returned while the underlying scheduler was still draining")
			default:
			}
			if builds.Load() != 1 {
				t.Fatalf("built %d instances before the first one exited", builds.Load())
			}
			lockKey := "tasks:singleton:{" + cfg.Scheduler.EffectiveLeaseName() + "}:lock"
			if !srv.Exists(lockKey) {
				t.Fatal("original lease was released before the underlying instance stopped")
			}
			s.mu.Lock()
			owned := s.current
			s.mu.Unlock()
			cleanupCtx, cleanupCancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
			cleanupErr := s.Cleanup(cleanupCtx)
			cleanupCancel()
			if !errors.Is(cleanupErr, context.DeadlineExceeded) {
				t.Fatalf("Cleanup during drain = %v", cleanupErr)
			}
			s.mu.Lock()
			stillOwned := s.current == owned && owned != nil
			s.mu.Unlock()
			if !stillOwned {
				t.Fatal("Cleanup timeout discarded the current instance")
			}
			srv.SetError("")
			cancel()
			inner.finish()
			awaitSchedulerSignal(t, done)
			if !errors.Is(runErr, context.DeadlineExceeded) || observed.Load() != 1 {
				t.Fatalf("late exit lost or repeated the timeout: err=%v observations=%d", runErr, observed.Load())
			}
			if srv.Exists(lockKey) || builds.Load() != 1 || inner.shutdowns.Load() < 2 {
				t.Fatalf("lease/instance tracking drifted: lease=%t builds=%d shutdowns=%d", srv.Exists(lockKey), builds.Load(), inner.shutdowns.Load())
			}
			for range 3 {
				if err := s.Cleanup(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestSchedulerLateCleanupCannotForgetNewGeneration(t *testing.T) {
	_, client := newTestRedis(t)
	s, err := NewScheduler(schedulerConfig(), client, "cleanup-generation", SchedulerObserver{}, idempotentTask())
	if err != nil {
		t.Fatal(err)
	}
	old := newControlledScheduler()
	t.Cleanup(old.finish)
	s.hold(old)
	cleaned := make(chan error, 1)
	go func() { cleaned <- s.Cleanup(t.Context()) }()
	awaitSchedulerSignal(t, old.stopping)
	next := newControlledScheduler()
	next.finish()
	want := s.hold(next)
	old.finish()
	if err := <-cleaned; err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	got := s.current
	s.mu.Unlock()
	if got != want {
		t.Fatal("late Cleanup removed the next generation")
	}
	if err := s.Cleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerRoundPreservesOperationAndReleaseFailures(t *testing.T) {
	for _, phase := range []string{"build", "register", "start"} {
		t.Run(phase, func(t *testing.T) {
			srv, client := newTestRedis(t)
			cause := errors.New("injected operation failure")
			var failures []error
			s, err := NewScheduler(schedulerConfig(), client, "round-errors", SchedulerObserver{
				RoundFailed: func(_ string, err error) { failures = append(failures, err) },
			}, idempotentTask())
			if err != nil {
				t.Fatal(err)
			}
			inner := newControlledScheduler()
			inner.finish()
			inner.registerHook = func() { srv.SetError("ERR injected release failure") }
			s.newRunner = func() (schedulerRunner, error) {
				if phase == "build" {
					srv.SetError("ERR injected release failure")
					return nil, cause
				}
				if phase == "register" {
					inner.registerErr = cause
				} else {
					inner.startErr = cause
				}
				return inner, nil
			}
			acquired, err := s.leadOnce(t.Context())
			if !acquired || !errors.Is(err, cause) || len(failures) != 2 {
				t.Fatalf("acquired=%t err=%v failures=%v", acquired, err, failures)
			}
			if !errors.Is(err, failures[1]) {
				t.Fatalf("release failure was not retained: %v", err)
			}
		})
	}
}

func TestPeriodicTaskGuaranteeWhitelist(t *testing.T) {
	t.Parallel()
	for _, guarantee := range []singleton.Guarantee{singleton.GuaranteeUnspecified, singleton.GuaranteeIdempotent, singleton.GuaranteeFenced, 255} {
		task := idempotentTask()
		task.Guarantee = guarantee
		err := task.validate()
		if (err == nil) != (guarantee == singleton.GuaranteeIdempotent) {
			t.Fatalf("guarantee=%d err=%v", guarantee, err)
		}
	}
}

func TestSchedulerRestartsOnlyAfterOldInstanceReallyExits(t *testing.T) {
	srv, client := newTestRedis(t)
	cfg := schedulerConfig()
	cfg.ShutdownTimeout = 10 * time.Millisecond
	first, next := newControlledScheduler(), newControlledScheduler()
	lost := make(chan struct{})
	var builds atomic.Int32
	s, err := NewScheduler(cfg, client, "restart-contract", SchedulerObserver{
		LeaseLost: func(string, error) { close(lost) },
	}, idempotentTask())
	if err != nil {
		t.Fatal(err)
	}
	s.newRunner = func() (schedulerRunner, error) {
		if builds.Add(1) == 1 {
			return first, nil
		}
		return next, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var runErr error
	go func() { defer close(done); runErr = s.Run(ctx) }()
	t.Cleanup(func() { cancel(); first.finish(); next.finish(); awaitSchedulerSignal(t, done) })
	awaitSchedulerSignal(t, first.started)
	srv.Del("tasks:singleton:{" + cfg.Scheduler.EffectiveLeaseName() + "}:lock")
	awaitSchedulerSignal(t, lost)
	awaitSchedulerSignal(t, first.waitingAgain)
	if builds.Load() != 1 {
		t.Fatalf("old instance is still draining but %d instances were built", builds.Load())
	}
	first.finish()
	awaitSchedulerSignal(t, next.started)
	if builds.Load() != 2 {
		t.Fatalf("expected exactly one replacement after old exit, built %d", builds.Load())
	}
	cancel()
	next.finish()
	awaitSchedulerSignal(t, done)
	if runErr != nil {
		t.Fatalf("complete replacement shutdown returned %v", runErr)
	}
}

func TestPeriodicRegistrationPreservesQueueAndUniqueTTL(t *testing.T) {
	srv, client := newTestRedis(t)
	cfg := schedulerConfig()
	cfg.Scheduler.UniqueTTL = 7 * time.Second
	entry := idempotentTask()
	entry.Spec = "@every 1s"
	entry.Options = []asynqx.TaskOption{asynqx.WithTaskQueue("unique-contract")}
	s, err := NewScheduler(cfg, client, "unique-contract", SchedulerObserver{}, entry)
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
		if strings.HasPrefix(key, "asynq:{unique-contract}:unique:") {
			uniqueKeys++
			if ttl := srv.TTL(key); ttl != cfg.Scheduler.UniqueTTL {
				t.Errorf("registered unique TTL=%s want=%s", ttl, cfg.Scheduler.UniqueTTL)
			}
		}
	}
	if uniqueKeys != 1 {
		t.Fatalf("queue/unique options were not preserved: keys=%v", srv.Keys())
	}
}
