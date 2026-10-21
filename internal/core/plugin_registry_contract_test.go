package core

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestPluginRegistryStartFailureCleansAllInitialized(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var events []string
	root := errors.New("B start failed")
	closeA := errors.New("A close failed")
	closeC := errors.New("C close failed")
	a := &testPlugin{name: "a", mu: &mu, events: &events, stopErr: closeA}
	b := &testPlugin{name: "b", mu: &mu, events: &events, startErr: root}
	c := &testPlugin{name: "c", mu: &mu, events: &events, stopErr: closeC}
	r := NewPluginRegistry()
	for _, p := range []*testPlugin{a, b, c} {
		if err := r.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	err := r.Start(t.Context())
	for _, want := range []error{root, closeA, closeC} {
		if !errors.Is(err, want) {
			t.Errorf("Start = %v, missing %v", err, want)
		}
	}
	want := []string{"init:a", "init:b", "init:c", "start:a", "start:b", "stop:c", "stop:b", "stop:a"}
	if !slices.Equal(events, want) {
		t.Errorf("events = %v, want %v", events, want)
	}
	stopErr := r.Stop(t.Context())
	if !errors.Is(stopErr, closeA) || !errors.Is(stopErr, closeC) {
		t.Errorf("Stop = %v, want both cached cleanup errors", stopErr)
	}
	for _, p := range []*testPlugin{a, b, c} {
		if got := p.stopCall.Load(); got != 1 {
			t.Errorf("%s Stop calls = %d, want 1", p.name, got)
		}
	}
}

func TestPluginRegistryCancellationJoinsCleanupErrors(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"init", "start"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			closeErr := errors.New("close failed")
			a := &testPlugin{name: "a", stopErr: closeErr}
			cancelCallback := func(context.Context) error { cancel(); return nil }
			if phase == "init" {
				a.initFn = cancelCallback
			} else {
				a.startFn = cancelCallback
			}
			b := &testPlugin{name: "b"}
			r := NewPluginRegistry()
			if err := r.Register(a); err != nil {
				t.Fatal(err)
			}
			if err := r.Register(b); err != nil {
				t.Fatal(err)
			}
			err := r.Start(ctx)
			if !errors.Is(err, context.Canceled) || !errors.Is(err, closeErr) {
				t.Errorf("Start = %v, want cancellation and cleanup error", err)
			}
			wantB := int32(0)
			if phase == "start" {
				wantB = 1
			}
			if a.stopCall.Load() != 1 || b.stopCall.Load() != wantB {
				t.Errorf("Stop calls a=%d b=%d, want 1/%d", a.stopCall.Load(), b.stopCall.Load(), wantB)
			}
		})
	}
}

func TestPluginRegistryRollbackDetachesStartupCancellation(t *testing.T) {
	t.Parallel()
	type contextKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), contextKey{}, "trace"))
	defer cancel()
	p := &testPlugin{name: "partial", initFn: func(context.Context) error { cancel(); return context.Canceled }}
	p.stopFn = func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			t.Errorf("rollback inherited startup cancellation: %v", err)
		}
		if got := ctx.Value(contextKey{}); got != "trace" {
			t.Errorf("rollback value = %v, want trace", got)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("rollback has no deadline")
		}
		return nil
	}
	r := NewPluginRegistry()
	if err := r.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := r.Init(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Init = %v, want cancellation", err)
	}
	if err := r.Stop(t.Context()); err != nil {
		t.Errorf("Stop after rollback = %v", err)
	}
	if p.stopCall.Load() != 1 {
		t.Errorf("Stop calls = %d, want 1", p.stopCall.Load())
	}
}

func registryWithPlugins(t *testing.T, plugins ...*testPlugin) *PluginRegistry {
	t.Helper()
	r := NewPluginRegistry()
	for _, p := range plugins {
		if err := r.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func pausePlugin(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestPluginRegistryRegistrationMetadataIsFrozen(t *testing.T) {
	t.Parallel()
	root := errors.New("start failed")
	a := &testPlugin{name: " a "}
	b := &testPlugin{name: "b", startErr: root}
	deps := []string{" a "}
	r := registryWithPlugins(t, a)
	if err := r.Register(b, deps...); err != nil {
		t.Fatal(err)
	}
	deps[0] = "not-registered"
	a.name = "changed"
	if err := r.Start(t.Context()); !errors.Is(err, root) {
		t.Fatalf("Start = %v, want original start failure", err)
	}
	if a.nameCall.Load() != 1 || b.nameCall.Load() != 1 {
		t.Errorf("Name calls = %d/%d, want one snapshot each", a.nameCall.Load(), b.nameCall.Load())
	}
}

func TestPluginRegistryGraphValidationAndStableReadyOrder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		deps  [][]string
		want  error
		order []string
	}{
		{name: "self cycle", deps: [][]string{{"a"}}, want: ErrPluginDependencyCycle},
		{name: "three node cycle", deps: [][]string{{"b"}, {"c"}, {"a"}}, want: ErrPluginDependencyCycle},
		{name: "blocked tail before cycle", deps: [][]string{{"b"}, {"c"}, {"b"}}, want: ErrPluginDependencyCycle},
		{name: "disconnected cycle", deps: [][]string{nil, {"c"}, {"b"}}, want: ErrPluginDependencyCycle},
		{name: "missing behind valid dependency", deps: [][]string{nil, {"a", "missing"}}, want: ErrPluginDependencyMissing},
		{name: "newly ready precedes later registration", deps: [][]string{{"b"}, nil, nil}, order: []string{"b", "a", "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var events []string
			r := NewPluginRegistry()
			for i, deps := range tt.deps {
				p := &testPlugin{name: string(rune('a' + i)), mu: &mu, events: &events}
				if err := r.Register(p, deps...); err != nil {
					t.Fatal(err)
				}
			}
			err := r.Init(t.Context())
			if tt.want != nil {
				if !errors.Is(err, tt.want) {
					t.Errorf("Init = %v, want %v", err, tt.want)
				}
				if len(events) != 0 {
					t.Errorf("callbacks before graph validation = %v", events)
				}
				if err := r.Start(t.Context()); !errors.Is(err, tt.want) {
					t.Errorf("Start = %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				want := make([]string, 0, len(tt.order))
				for _, name := range tt.order {
					want = append(want, "init:"+name)
				}
				if !slices.Equal(events, want) {
					t.Errorf("order = %v, want %v", events, want)
				}
			}
			if err := r.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPluginRegistryDeepGraphDoesNotRecurse(t *testing.T) {
	t.Parallel()
	const count = 4096
	for _, cyclic := range []bool{false, true} {
		t.Run(strconv.FormatBool(cyclic), func(t *testing.T) {
			r := NewPluginRegistry()
			for i := range count {
				var deps []string
				if i+1 < count {
					deps = []string{strconv.Itoa(i + 1)}
				} else if cyclic {
					deps = []string{"0"}
				}
				if err := r.Register(&testPlugin{name: strconv.Itoa(i)}, deps...); err != nil {
					t.Fatal(err)
				}
			}
			err := r.Init(t.Context())
			if cyclic {
				if !errors.Is(err, ErrPluginDependencyCycle) {
					t.Fatalf("Init = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if err := r.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPluginRegistryStartupBudgets(t *testing.T) {
	tests := []struct {
		name                                    string
		initDelay, startDelay, gap, parentLimit time.Duration
		count                                   int
		ignoreCancellation                      bool
		wantElapsed                             time.Duration
	}{
		{name: "single init callback", initDelay: 20 * time.Second, count: 1, wantElapsed: 10 * time.Second},
		{name: "single start callback", startDelay: 20 * time.Second, count: 1, wantElapsed: 10 * time.Second},
		{name: "parent tightens startup", initDelay: 20 * time.Second, parentLimit: 3 * time.Second, count: 1, wantElapsed: 3 * time.Second},
		{name: "shared init and start total", initDelay: 6 * time.Second, startDelay: 6 * time.Second, count: 3, wantElapsed: 30 * time.Second},
		{name: "explicit start retains original deadline", initDelay: 9 * time.Second, gap: 25 * time.Second, count: 1, wantElapsed: 34 * time.Second},
		{name: "late nil still fails", initDelay: 11 * time.Second, count: 1, ignoreCancellation: true, wantElapsed: 11 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := t.Context()
				if tt.parentLimit > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, tt.parentLimit)
					defer cancel()
				}
				r := NewPluginRegistry()
				plugins := make([]*testPlugin, 0, tt.count)
				for i := range tt.count {
					p := &testPlugin{name: strconv.Itoa(i)}
					p.initFn = func(ctx context.Context) error {
						if tt.ignoreCancellation {
							time.Sleep(tt.initDelay)
							return nil
						}
						return pausePlugin(ctx, tt.initDelay)
					}
					p.startFn = func(ctx context.Context) error { return pausePlugin(ctx, tt.startDelay) }
					plugins = append(plugins, p)
					if err := r.Register(p); err != nil {
						t.Fatal(err)
					}
				}
				began := time.Now()
				var err error
				if tt.gap > 0 {
					if err = r.Init(ctx); err != nil {
						t.Fatal(err)
					}
					time.Sleep(tt.gap)
				}
				err = r.Start(ctx)
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("Start = %v, want deadline", err)
				}
				if elapsed := time.Since(began); elapsed != tt.wantElapsed {
					t.Errorf("elapsed = %v, want %v", elapsed, tt.wantElapsed)
				}
				for _, p := range plugins {
					if p.stopCall.Load() != 1 {
						t.Errorf("%s Stop calls = %d", p.name, p.stopCall.Load())
					}
				}
				if err := r.Stop(t.Context()); err != nil {
					t.Errorf("successful rollback Stop = %v", err)
				}
			})
		})
	}
}

func TestPluginRegistryCleanupBudgetsAndForce(t *testing.T) {
	tests := []struct {
		name                     string
		rollback                 bool
		parentLimit, cancelAfter time.Duration
		forced, alreadyForced    bool
		want                     error
		wantElapsed              time.Duration
	}{
		{name: "stop aggregate thirty seconds", want: context.DeadlineExceeded, wantElapsed: 30 * time.Second},
		{name: "rollback aggregate thirty seconds", rollback: true, want: context.DeadlineExceeded, wantElapsed: 30 * time.Second},
		{name: "stop respects shorter parent", parentLimit: 3 * time.Second, want: context.DeadlineExceeded, wantElapsed: 3 * time.Second},
		{name: "stop respects parent cancellation", cancelAfter: 2 * time.Second, want: context.Canceled, wantElapsed: 2 * time.Second},
		{name: "stop forced", forced: true, cancelAfter: 2 * time.Second, want: context.Canceled, wantElapsed: 2 * time.Second},
		{name: "rollback forced", rollback: true, forced: true, cancelAfter: 2 * time.Second, want: context.Canceled, wantElapsed: 2 * time.Second},
		{name: "stop already forced", forced: true, alreadyForced: true, want: context.Canceled},
		{name: "rollback already forced", rollback: true, forced: true, alreadyForced: true, want: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				root := errors.New("partial init failed")
				stop := func(ctx context.Context) error { <-ctx.Done(); return nil }
				a := &testPlugin{name: "a", stopFn: stop}
				b := &testPlugin{name: "b", stopFn: stop}
				if tt.rollback {
					b.initErr = root
				}
				r := registryWithPlugins(t, a, b)
				var forced chan struct{}
				if tt.forced {
					forced = make(chan struct{})
					if err := r.SetForceSignal(forced); err != nil {
						t.Fatal(err)
					}
					if tt.alreadyForced {
						close(forced)
					}
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if tt.parentLimit > 0 {
					var finish context.CancelFunc
					ctx, finish = context.WithTimeout(ctx, tt.parentLimit)
					defer finish()
				}
				if !tt.rollback {
					if err := r.Start(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				var canceller sync.WaitGroup
				if tt.cancelAfter > 0 {
					canceller.Go(func() {
						time.Sleep(tt.cancelAfter)
						if tt.forced {
							close(forced)
						} else {
							cancel()
						}
					})
				}
				began := time.Now()
				var err error
				if tt.rollback {
					err = r.Init(ctx)
				} else {
					err = r.Stop(ctx)
				}
				canceller.Wait()
				if !errors.Is(err, tt.want) {
					t.Errorf("cleanup = %v, want %v", err, tt.want)
				}
				if tt.rollback && !errors.Is(err, root) {
					t.Errorf("cleanup lost init root: %v", err)
				}
				if elapsed := time.Since(began); elapsed != tt.wantElapsed {
					t.Errorf("elapsed = %v, want %v", elapsed, tt.wantElapsed)
				}
				if a.stopCall.Load() != 1 || b.stopCall.Load() != 1 {
					t.Errorf("Stop counts = %d/%d", a.stopCall.Load(), b.stopCall.Load())
				}
				if again := r.Stop(t.Context()); !errors.Is(again, tt.want) {
					t.Errorf("cached cleanup = %v, want %v", again, tt.want)
				}
				if a.stopCall.Load() != 1 || b.stopCall.Load() != 1 {
					t.Error("repeated cleanup")
				}
			})
		})
	}
}

func TestPluginRegistryConcurrentStartupSharesResult(t *testing.T) {
	tests := []struct {
		name         string
		explicitInit bool
		failurePhase string
	}{
		{name: "implicit success"},
		{name: "implicit init failure", failurePhase: "init"},
		{name: "implicit start failure", failurePhase: "start"},
		{name: "explicit success", explicitInit: true},
		{name: "explicit init failure", explicitInit: true, failurePhase: "init"},
		{name: "explicit start failure", explicitInit: true, failurePhase: "start"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				root := errors.New("owner failed")
				entered, release := make(chan struct{}), make(chan struct{})
				p := &testPlugin{name: "one", initFn: func(context.Context) error {
					close(entered)
					<-release
					if tt.failurePhase == "init" {
						return root
					}
					return nil
				}}
				if tt.failurePhase == "start" {
					p.startErr = root
				}
				r := registryWithPlugins(t, p)
				owner := make(chan error, 1)
				var owners sync.WaitGroup
				owners.Go(func() {
					if tt.explicitInit {
						owner <- r.Init(t.Context())
					} else {
						owner <- r.Start(t.Context())
					}
				})
				<-entered
				const joiners = 32
				results := make(chan error, joiners)
				var joins sync.WaitGroup
				for range joiners {
					joins.Go(func() { results <- r.Start(t.Context()) })
				}
				initResult := make(chan error, 1)
				joins.Go(func() { initResult <- r.Init(t.Context()) })
				synctest.Wait()
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				for _, wait := range []func(context.Context) error{r.Init, r.Start, r.Stop} {
					if err := wait(ctx); !errors.Is(err, context.Canceled) {
						t.Errorf("canceled waiter = %v", err)
					}
				}
				if p.stopCall.Load() != 0 {
					t.Error("waiter canceled owner resources")
				}
				close(release)
				owners.Wait()
				joins.Wait()
				wantOwner := error(nil)
				if tt.failurePhase != "" && (!tt.explicitInit || tt.failurePhase == "init") {
					wantOwner = root
				}
				if err := <-owner; !errors.Is(err, wantOwner) {
					t.Errorf("owner = %v, want %v", err, wantOwner)
				}
				wantStart := error(nil)
				if tt.failurePhase != "" {
					wantStart = root
				}
				for range joiners {
					if err := <-results; !errors.Is(err, wantStart) {
						t.Errorf("Start joiner = %v, want %v", err, wantStart)
					}
				}
				wantInit := error(nil)
				if tt.failurePhase == "init" {
					wantInit = root
				}
				if err := <-initResult; !errors.Is(err, wantInit) {
					t.Errorf("Init joiner = %v, want %v", err, wantInit)
				}
				if err := r.Stop(t.Context()); err != nil {
					t.Fatal(err)
				}
				wantStartCalls := int32(1)
				if tt.failurePhase == "init" {
					wantStartCalls = 0
				}
				if p.initCall.Load() != 1 || p.startCall.Load() != wantStartCalls || p.stopCall.Load() != 1 {
					t.Errorf("calls Init/Start/Stop = %d/%d/%d", p.initCall.Load(), p.startCall.Load(), p.stopCall.Load())
				}
			})
		})
	}
}

func TestPluginRegistryStartWaitingForExternalInitCanCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		p := &testPlugin{name: "one", initFn: func(context.Context) error { close(entered); <-release; return nil }}
		r := registryWithPlugins(t, p)
		initDone := make(chan error, 1)
		var initOwner sync.WaitGroup
		initOwner.Go(func() { initDone <- r.Init(t.Context()) })
		<-entered
		ctx, cancel := context.WithCancel(t.Context())
		startDone := make(chan error, 1)
		var startOwner sync.WaitGroup
		startOwner.Go(func() { startDone <- r.Start(ctx) })
		synctest.Wait()
		cancel()
		if err := <-startDone; !errors.Is(err, context.Canceled) {
			t.Errorf("Start waiting Init = %v", err)
		}
		startOwner.Wait()
		if p.stopCall.Load() != 0 {
			t.Error("canceled Start took over external Init cleanup")
		}
		close(release)
		initOwner.Wait()
		if err := <-initDone; err != nil {
			t.Fatal(err)
		}
		if err := r.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := r.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		if p.initCall.Load() != 1 || p.startCall.Load() != 1 || p.stopCall.Load() != 1 {
			t.Error("duplicate/missing lifecycle callback")
		}
	})
}

func TestPluginRegistryStopWaitsWithoutConcurrentCleanup(t *testing.T) {
	for _, phase := range []string{"init", "start"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				root := errors.New("startup failed after wait")
				var active atomic.Bool
				callback := func(context.Context) error {
					active.Store(true)
					close(entered)
					<-release
					active.Store(false)
					return root
				}
				p := &testPlugin{name: "one", stopFn: func(context.Context) error {
					if active.Load() {
						t.Error("Stop overlapped startup callback")
					}
					return nil
				}}
				if phase == "init" {
					p.initFn = callback
				} else {
					p.startFn = callback
				}
				r := registryWithPlugins(t, p)
				startDone, stopDone := make(chan error, 1), make(chan error, 1)
				var owners sync.WaitGroup
				owners.Go(func() { startDone <- r.Start(t.Context()) })
				<-entered
				owners.Go(func() { stopDone <- r.Stop(t.Context()) })
				synctest.Wait()
				if p.stopCall.Load() != 0 {
					t.Error("Stop ran before startup finished")
				}
				close(release)
				owners.Wait()
				if err := <-startDone; !errors.Is(err, root) {
					t.Errorf("Start = %v", err)
				}
				if err := <-stopDone; err != nil {
					t.Errorf("Stop = %v", err)
				}
				if p.stopCall.Load() != 1 {
					t.Errorf("Stop calls = %d, want 1", p.stopCall.Load())
				}
			})
		})
	}
}

func TestPluginRegistryStopBudgetIncludesWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		p := &testPlugin{name: "one", initFn: func(context.Context) error { close(entered); <-release; return nil }, stopFn: func(ctx context.Context) error { <-ctx.Done(); return nil }}
		r := registryWithPlugins(t, p)
		startDone, stopDone := make(chan error, 1), make(chan error, 1)
		var owners sync.WaitGroup
		owners.Go(func() { startDone <- r.Start(t.Context()) })
		<-entered
		began := time.Now()
		owners.Go(func() { stopDone <- r.Stop(t.Context()) })
		synctest.Wait()
		time.Sleep(5 * time.Second)
		close(release)
		if err := <-startDone; err != nil {
			t.Fatal(err)
		}
		if err := <-stopDone; !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Stop = %v", err)
		}
		owners.Wait()
		if elapsed := time.Since(began); elapsed != 30*time.Second {
			t.Errorf("Stop elapsed = %v, want 30s including waiting", elapsed)
		}
		if p.stopCall.Load() != 1 {
			t.Errorf("Stop calls = %d", p.stopCall.Load())
		}
	})
}

func TestPluginRegistryStopWaitCanBeForced(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		p := &testPlugin{name: "one", initFn: func(context.Context) error { close(entered); <-release; return nil }}
		r := registryWithPlugins(t, p)
		forced := make(chan struct{})
		if err := r.SetForceSignal(forced); err != nil {
			t.Fatal(err)
		}
		startDone, stopDone := make(chan error, 1), make(chan error, 1)
		var owners sync.WaitGroup
		owners.Go(func() { startDone <- r.Start(t.Context()) })
		<-entered
		owners.Go(func() { stopDone <- r.Stop(t.Context()) })
		synctest.Wait()
		close(forced)
		if err := <-stopDone; !errors.Is(err, context.Canceled) {
			t.Errorf("forced wait = %v", err)
		}
		if p.stopCall.Load() != 0 {
			t.Error("forced waiter started concurrent cleanup")
		}
		close(release)
		owners.Wait()
		if err := <-startDone; err != nil {
			t.Fatal(err)
		}
		if err := r.Stop(t.Context()); !errors.Is(err, context.Canceled) {
			t.Errorf("forced Stop = %v", err)
		}
	})
}

func TestPluginRegistryConcurrentStopSharesErrorAndAllowsWaitCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		root := errors.New("close failed")
		p := &testPlugin{name: "one", stopFn: func(context.Context) error { close(entered); <-release; return root }}
		r := registryWithPlugins(t, p)
		if err := r.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		ownerDone := make(chan error, 1)
		var owners sync.WaitGroup
		owners.Go(func() { ownerDone <- r.Stop(t.Context()) })
		<-entered
		const joiners = 32
		results := make(chan error, joiners)
		for range joiners {
			owners.Go(func() { results <- r.Stop(t.Context()) })
		}
		synctest.Wait()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := r.Stop(ctx); !errors.Is(err, context.Canceled) {
			t.Errorf("Stop waiter = %v", err)
		}
		close(release)
		owners.Wait()
		if err := <-ownerDone; !errors.Is(err, root) {
			t.Errorf("owner Stop = %v", err)
		}
		for range joiners {
			if err := <-results; !errors.Is(err, root) {
				t.Errorf("joined Stop = %v", err)
			}
		}
		if p.stopCall.Load() != 1 {
			t.Errorf("Stop calls = %d, want 1", p.stopCall.Load())
		}
	})
}

func TestPluginRegistryPanicStillCleansAndPreservesErrors(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"init", "start", "stop"} {
		t.Run(phase, func(t *testing.T) {
			closeErr := errors.New("A close failed")
			a, b, c := &testPlugin{name: "a", stopErr: closeErr}, &testPlugin{name: "b"}, &testPlugin{name: "c"}
			panics := func(context.Context) error { panic("plugin bug") }
			switch phase {
			case "init":
				b.initFn = panics
			case "start":
				b.startFn = panics
			case "stop":
				b.stopFn = panics
			}
			r := registryWithPlugins(t, a, b, c)
			err := r.Start(t.Context())
			if phase == "stop" {
				if err != nil {
					t.Fatal(err)
				}
				err = r.Stop(t.Context())
			}
			if !errors.Is(err, ErrPluginContractViolation) || !errors.Is(err, closeErr) {
				t.Errorf("panic result = %v", err)
			}
			wantC := int32(1)
			if phase == "init" {
				wantC = 0
			}
			if a.stopCall.Load() != 1 || b.stopCall.Load() != 1 || c.stopCall.Load() != wantC {
				t.Errorf("Stop counts = %d/%d/%d", a.stopCall.Load(), b.stopCall.Load(), c.stopCall.Load())
			}
			if again := r.Stop(t.Context()); !errors.Is(again, closeErr) {
				t.Errorf("cached error = %v", again)
			}
		})
	}
}

func TestPluginRegistryLifecycleBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		prepare func(*PluginRegistry, *testing.T)
	}{
		{name: "initialized", prepare: func(r *PluginRegistry, t *testing.T) {
			if err := r.Init(t.Context()); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "started", prepare: func(r *PluginRegistry, t *testing.T) {
			if err := r.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "stopped before init", prepare: func(r *PluginRegistry, t *testing.T) {
			if err := r.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r PluginRegistry
			if err := r.Register(&testPlugin{name: "one"}); err != nil {
				t.Fatal(err)
			}
			tt.prepare(&r, t)
			if err := r.Register(&testPlugin{name: "late"}); !errors.Is(err, ErrPluginRegistryLifecycle) {
				t.Errorf("Register = %v", err)
			}
			if err := r.SetForceSignal(make(chan struct{})); !errors.Is(err, ErrPluginRegistryLifecycle) {
				t.Errorf("SetForceSignal = %v", err)
			}
			if err := r.Init(t.Context()); !errors.Is(err, ErrPluginRegistryLifecycle) {
				t.Errorf("Init = %v", err)
			}
			if tt.name != "initialized" {
				if err := r.Start(t.Context()); !errors.Is(err, ErrPluginRegistryLifecycle) {
					t.Errorf("Start = %v", err)
				}
			}
			if err := r.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := r.Start(t.Context()); !errors.Is(err, ErrPluginRegistryLifecycle) {
				t.Errorf("restart = %v", err)
			}
		})
	}
	var r *PluginRegistry
	if err := r.Register(&testPlugin{name: "one"}); !errors.Is(err, ErrPluginContractViolation) {
		t.Errorf("nil Register = %v", err)
	}
	if err := r.SetForceSignal(nil); !errors.Is(err, ErrPluginContractViolation) {
		t.Errorf("nil SetForceSignal = %v", err)
	}
	for _, operation := range []func(context.Context) error{r.Init, r.Start, r.Stop} {
		if err := operation(t.Context()); err != nil {
			t.Errorf("nil lifecycle = %v", err)
		}
	}
	if err := NewPluginRegistry().Register(nil); !errors.Is(err, ErrPluginContractViolation) {
		t.Errorf("nil plugin = %v", err)
	}
	if err := NewPluginRegistry().Start(t.Context()); err != nil {
		t.Errorf("empty Start = %v", err)
	}
}

func TestPluginRegistryFailureResultsAreCached(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"init", "start"} {
		t.Run(phase, func(t *testing.T) {
			root, closeErr := errors.New("callback failed"), errors.New("close failed")
			p := &testPlugin{name: "one", stopErr: closeErr}
			if phase == "init" {
				p.initErr = root
			} else {
				p.startErr = root
			}
			r := registryWithPlugins(t, p)
			if err := r.Start(t.Context()); !errors.Is(err, root) || !errors.Is(err, closeErr) {
				t.Fatalf("first Start = %v", err)
			}
			for _, operation := range []func(context.Context) error{r.Init, r.Start} {
				if err := operation(t.Context()); !errors.Is(err, root) || !errors.Is(err, closeErr) {
					t.Errorf("cached failure = %v", err)
				}
			}
			if err := r.Stop(t.Context()); !errors.Is(err, closeErr) {
				t.Errorf("Stop = %v", err)
			}
			if p.initCall.Load() != 1 || p.stopCall.Load() != 1 {
				t.Error("failed registry retried callbacks")
			}
		})
	}
}

func TestPluginRegistryStopWaitingForExplicitInitCanCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		p := &testPlugin{name: "one", initFn: func(context.Context) error { close(entered); <-release; return nil }}
		r := registryWithPlugins(t, p)
		initDone := make(chan error, 1)
		var owner sync.WaitGroup
		owner.Go(func() { initDone <- r.Init(t.Context()) })
		<-entered
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		if err := r.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Stop waiting explicit Init = %v", err)
		}
		if p.stopCall.Load() != 0 {
			t.Error("wait timeout cleaned a still-running Init")
		}
		close(release)
		owner.Wait()
		if err := <-initDone; err != nil {
			t.Fatal(err)
		}
		if err := r.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		if p.stopCall.Load() != 1 {
			t.Errorf("Stop count = %d", p.stopCall.Load())
		}
	})
}

func TestPluginRegistryForceObserverExitsOnSuccessfulCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := registryWithPlugins(t, &testPlugin{name: "one"})
		if err := r.SetForceSignal(make(chan struct{})); err != nil {
			t.Fatal(err)
		}
		if err := r.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := r.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		// An observer left parked on the unclosed signal deadlocks this bubble.
	})
}

func TestPluginRegistryConcurrentRegistration(t *testing.T) {
	t.Parallel()
	for _, duplicate := range []bool{false, true} {
		t.Run(strconv.FormatBool(duplicate), func(t *testing.T) {
			r := NewPluginRegistry()
			var success atomic.Int32
			var registrations sync.WaitGroup
			for i := range 32 {
				registrations.Go(func() {
					name := strconv.Itoa(i)
					if duplicate {
						name = "same"
					}
					err := r.Register(&testPlugin{name: name})
					if err == nil {
						success.Add(1)
					} else if !duplicate || !errors.Is(err, ErrPluginContractViolation) {
						t.Errorf("Register = %v", err)
					}
				})
			}
			registrations.Wait()
			want := int32(32)
			if duplicate {
				want = 1
			}
			if success.Load() != want {
				t.Errorf("successful registrations = %d, want %d", success.Load(), want)
			}
			if err := r.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := r.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPluginRegistryStopCannotDuplicateInFlightRollback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cleanupEntered, release := make(chan struct{}), make(chan struct{})
		root, closeErr := errors.New("init failed"), errors.New("close failed")
		p := &testPlugin{name: "partial", initErr: root, stopFn: func(context.Context) error {
			close(cleanupEntered)
			<-release
			return closeErr
		}}
		r := registryWithPlugins(t, p)
		startDone := make(chan error, 1)
		var owner sync.WaitGroup
		owner.Go(func() { startDone <- r.Start(t.Context()) })
		<-cleanupEntered
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		if err := r.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Stop while rollback = %v", err)
		}
		if p.stopCall.Load() != 1 {
			t.Errorf("concurrent cleanup count = %d", p.stopCall.Load())
		}
		close(release)
		owner.Wait()
		if err := <-startDone; !errors.Is(err, root) || !errors.Is(err, closeErr) {
			t.Errorf("Start result = %v", err)
		}
		if err := r.Stop(t.Context()); !errors.Is(err, closeErr) {
			t.Errorf("cached rollback result = %v", err)
		}
		if p.stopCall.Load() != 1 {
			t.Error("Stop repeated failed cleanup")
		}
	})
}

func TestPluginRegistryStartupDeadlineDoesNotConsumeRollbackBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &testPlugin{name: "partial", initFn: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}
		p.stopFn = func(ctx context.Context) error {
			if ctx.Err() != nil {
				t.Errorf("rollback already canceled: %v", ctx.Err())
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) != 30*time.Second {
				t.Errorf("rollback remaining = %v, has deadline = %v", time.Until(deadline), ok)
			}
			<-ctx.Done()
			return ctx.Err()
		}
		r := registryWithPlugins(t, p)
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		began := time.Now()
		if err := r.Start(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Start = %v", err)
		}
		if elapsed := time.Since(began); elapsed != 33*time.Second {
			t.Errorf("elapsed = %v, want 3s startup + 30s rollback", elapsed)
		}
		if err := r.Stop(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("cached rollback timeout = %v", err)
		}
		if p.stopCall.Load() != 1 {
			t.Errorf("Stop calls = %d", p.stopCall.Load())
		}
	})
}
