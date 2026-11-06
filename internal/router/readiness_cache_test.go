package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestReadinessCacheCoalescesConcurrentWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		release := make(chan struct{})
		probe := &readinessProbe{now: time.Now, limiter: newDepFailureLogLimiter(), deps: []dependency{
			{name: "database", required: true, ping: func(ctx context.Context) error {
				calls.Add(1)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}},
		}}
		var group sync.WaitGroup
		for range 100 {
			group.Go(func() {
				result, err := probe.check(t.Context())
				if err != nil || result.requiredFailed {
					t.Errorf("readiness = %+v, %v", result, err)
				}
			})
		}
		synctest.Wait()
		if got := calls.Load(); got != 1 {
			t.Fatalf("concurrent dependency batches = %d, want 1", got)
		}
		close(release)
		group.Wait()
		if probe.inFlight != nil {
			t.Fatal("completed batch remained registered")
		}
	})
}

func TestReadinessCacheTTLAndIndependentSnapshots(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		newProbe := func() *readinessProbe {
			return &readinessProbe{now: time.Now, limiter: newDepFailureLogLimiter(), deps: []dependency{
				{name: "database", required: true, ping: func(context.Context) error {
					calls.Add(1)
					return errors.New("test dependency unavailable")
				}},
			}}
		}
		probe := newProbe()
		first, err := probe.check(t.Context())
		if err != nil || !first.requiredFailed {
			t.Fatalf("first result = %+v, %v", first, err)
		}
		first.checks["database"] = DependencyCheck{Status: statusOK}
		second, err := probe.check(t.Context())
		if err != nil || second.checks["database"].Status != statusUnavailable || calls.Load() != 1 {
			t.Fatalf("failure cache was rerun or mutated: %+v, %v, calls=%d", second, err, calls.Load())
		}
		if _, err := newProbe().check(t.Context()); err != nil || calls.Load() != 2 {
			t.Fatalf("another instance shared the cache: %v, calls=%d", err, calls.Load())
		}
		synctest.Sleep(readyCacheTTL)
		if _, err := probe.check(t.Context()); err != nil || calls.Load() != 3 {
			t.Fatalf("cache did not refresh at TTL: %v, calls=%d", err, calls.Load())
		}
	})
}

func TestReadinessCacheCancellationKeepsSharedBatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		type traceKey struct{}
		var calls atomic.Int32
		release := make(chan struct{})
		probe := &readinessProbe{now: time.Now, limiter: newDepFailureLogLimiter(), deps: []dependency{
			{name: "database", required: true, ping: func(ctx context.Context) error {
				calls.Add(1)
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) != readyGlobalDeadline || ctx.Value(traceKey{}) != "trace" {
					t.Error("batch lost its independent budget or request value")
				}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}},
		}}
		ctx, cancel := context.WithCancel(context.WithValue(t.Context(), traceKey{}, "trace"))
		first := make(chan error, 1)
		go func() { _, err := probe.check(ctx); first <- err }()
		synctest.Wait()
		cancel()
		if err := <-first; !errors.Is(err, context.Canceled) {
			t.Fatalf("first waiter = %v, want canceled", err)
		}
		second := make(chan error, 1)
		go func() { _, err := probe.check(t.Context()); second <- err }()
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatal("canceling a waiter restarted the shared batch")
		}
		close(release)
		if err := <-second; err != nil {
			t.Fatalf("remaining waiter failed: %v", err)
		}
		synctest.Wait()
	})
}

func TestReadinessCacheRejectsCanceledRequestAndPanic(t *testing.T) {
	var calls atomic.Int32
	probe := &readinessProbe{now: time.Now, limiter: newDepFailureLogLimiter(), deps: []dependency{
		{name: "database", required: true, ping: func(context.Context) error { calls.Add(1); panic("test") }},
	}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := probe.check(ctx); !errors.Is(err, context.Canceled) || calls.Load() != 0 {
		t.Fatalf("canceled request started a batch: %v, calls=%d", err, calls.Load())
	}
	result, err := probe.check(t.Context())
	if err != nil || !result.requiredFailed || result.checks["database"].Error != dependencyFailureMessage {
		t.Fatalf("panic became healthy: %+v, %v", result, err)
	}
}

func TestReadinessCacheKeepsProcessStateLive(t *testing.T) {
	state := &stubStatus{}
	engine := newTestEngineWith(t, Options{HealthChecker: &stubChecker{}, ProcessStatus: state})
	request := func(want int) {
		t.Helper()
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
		if w.Code != want {
			t.Fatalf("readiness = %d, want %d: %s", w.Code, want, w.Body.String())
		}
	}
	request(http.StatusOK)
	state.failed = []string{"worker"}
	request(http.StatusServiceUnavailable)
	state.failed = nil
	state.draining = true
	request(http.StatusServiceUnavailable)
}
