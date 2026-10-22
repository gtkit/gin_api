package core

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
)

var _ Plugin = (*testPlugin)(nil)

type testPlugin struct {
	name string

	mu     *sync.Mutex
	events *[]string

	initErr   error
	startErr  error
	stopErr   error
	initFn    func(context.Context) error
	startFn   func(context.Context) error
	stopFn    func(context.Context) error
	nameCall  atomic.Int32
	initCall  atomic.Int32
	startCall atomic.Int32
	stopCall  atomic.Int32
}

func (p *testPlugin) Name() string {
	p.nameCall.Add(1)
	return p.name
}

func (p *testPlugin) Init(ctx context.Context) error {
	p.initCall.Add(1)
	p.record("init:" + p.name)
	if p.initFn != nil {
		return p.initFn(ctx)
	}
	return p.initErr
}

func (p *testPlugin) Start(ctx context.Context) error {
	p.startCall.Add(1)
	p.record("start:" + p.name)
	if p.startFn != nil {
		return p.startFn(ctx)
	}
	return p.startErr
}

func (p *testPlugin) Stop(ctx context.Context) error {
	p.stopCall.Add(1)
	p.record("stop:" + p.name)
	if p.stopFn != nil {
		return p.stopFn(ctx)
	}
	return p.stopErr
}

func (p *testPlugin) record(event string) {
	if p.mu == nil || p.events == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	*p.events = append(*p.events, event)
}

func TestPluginRegistryRegisterValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(*PluginRegistry) error
		want  error
	}{
		{
			name: "empty name",
			setup: func(registry *PluginRegistry) error {
				return registry.Register(&testPlugin{name: "  "})
			},
			want: ErrPluginContractViolation,
		},
		{
			name: "typed nil",
			setup: func(registry *PluginRegistry) error {
				var plugin *testPlugin
				return registry.Register(plugin)
			},
			want: ErrPluginContractViolation,
		},
		{
			name: "duplicate name",
			setup: func(registry *PluginRegistry) error {
				if err := registry.Register(&testPlugin{name: "same"}); err != nil {
					return err
				}
				return registry.Register(&testPlugin{name: "same"})
			},
			want: ErrPluginContractViolation,
		},
		{
			name: "empty dependency",
			setup: func(registry *PluginRegistry) error {
				return registry.Register(&testPlugin{name: "main"}, " ")
			},
			want: ErrPluginContractViolation,
		},
		{
			name: "duplicate dependency",
			setup: func(registry *PluginRegistry) error {
				return registry.Register(&testPlugin{name: "main"}, "dep", "dep")
			},
			want: ErrPluginContractViolation,
		},
		{
			name: "register after lifecycle starts",
			setup: func(registry *PluginRegistry) error {
				if err := registry.Register(&testPlugin{name: "first"}); err != nil {
					return err
				}
				if err := registry.Init(t.Context()); err != nil {
					return err
				}
				return registry.Register(&testPlugin{name: "second"})
			},
			want: ErrPluginRegistryLifecycle,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewPluginRegistry()
			err := tt.setup(registry)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want errors.Is(_, %v)", err, tt.want)
			}
		})
	}
}

func TestPluginRegistryRejectsDependenciesBeforeCallbacks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		deps []string
		want error
	}{
		{name: "missing", deps: []string{"missing"}, want: ErrPluginDependencyMissing},
		{name: "cycle", deps: []string{"b"}, want: ErrPluginDependencyCycle},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				mu     sync.Mutex
				events []string
			)
			a := &testPlugin{name: "a", mu: &mu, events: &events}
			if err := NewPluginRegistry().Register(a, tt.deps...); err != nil {
				if tt.name == "missing" {
					t.Fatal(err)
				}
			}

			registry := NewPluginRegistry()
			if tt.name == "missing" {
				if err := registry.Register(a, tt.deps...); err != nil {
					t.Fatal(err)
				}
			} else {
				b := &testPlugin{name: "b", mu: &mu, events: &events}
				if err := registry.Register(a, "b"); err != nil {
					t.Fatal(err)
				}
				if err := registry.Register(b, "a"); err != nil {
					t.Fatal(err)
				}
			}

			err := registry.Init(t.Context())
			if !errors.Is(err, tt.want) {
				t.Fatalf("Init error = %v, want errors.Is(_, %v)", err, tt.want)
			}
			if len(events) != 0 {
				t.Fatalf("lifecycle events = %v, want none before dependency validation", events)
			}
		})
	}
}

func TestPluginRegistryLifecycleOrder(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		events []string
	)
	plugins := []*testPlugin{
		{name: "b", mu: &mu, events: &events},
		{name: "c", mu: &mu, events: &events},
		{name: "a", mu: &mu, events: &events},
	}
	registry := NewPluginRegistry()
	if err := registry.Register(plugins[0], "a"); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(plugins[1], "a"); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(plugins[2]); err != nil {
		t.Fatal(err)
	}

	if err := registry.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := registry.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	want := []string{
		"init:a", "init:b", "init:c",
		"start:a", "start:b", "start:c",
		"stop:c", "stop:b", "stop:a",
	}
	if !slices.Equal(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestPluginRegistryInitFailureRollsBackCurrentAndCompleted(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		events []string
	)
	initErr := errors.New("init failed")
	a := &testPlugin{name: "a", mu: &mu, events: &events}
	b := &testPlugin{name: "b", initErr: initErr, mu: &mu, events: &events}
	registry := NewPluginRegistry()
	if err := registry.Register(a); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(b, "a"); err != nil {
		t.Fatal(err)
	}

	err := registry.Init(t.Context())
	if !errors.Is(err, initErr) {
		t.Fatalf("Init error = %v, want init error", err)
	}
	want := []string{"init:a", "init:b", "stop:b", "stop:a"}
	if !slices.Equal(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestPluginRegistryStartFailureRollsBackCurrentAndStarted(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		events []string
	)
	startErr := errors.New("start failed")
	a := &testPlugin{name: "a", mu: &mu, events: &events}
	b := &testPlugin{name: "b", startErr: startErr, mu: &mu, events: &events}
	registry := NewPluginRegistry()
	if err := registry.Register(a); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(b, "a"); err != nil {
		t.Fatal(err)
	}

	err := registry.Start(t.Context())
	if !errors.Is(err, startErr) {
		t.Fatalf("Start error = %v, want start error", err)
	}
	want := []string{"init:a", "init:b", "start:a", "start:b", "stop:b", "stop:a"}
	if !slices.Equal(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestPluginRegistryStopIsIdempotentAndAggregatesErrors(t *testing.T) {
	t.Parallel()

	aErr := errors.New("a stop failed")
	bErr := errors.New("b stop failed")
	a := &testPlugin{name: "a", stopErr: aErr}
	b := &testPlugin{name: "b", stopErr: bErr}
	registry := NewPluginRegistry()
	if err := registry.Register(a); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(b, "a"); err != nil {
		t.Fatal(err)
	}
	if err := registry.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	err := registry.Stop(t.Context())
	if !errors.Is(err, aErr) || !errors.Is(err, bErr) {
		t.Fatalf("Stop error = %v, want both stop errors", err)
	}
	if errAgain := registry.Stop(t.Context()); !errors.Is(errAgain, aErr) || !errors.Is(errAgain, bErr) {
		t.Fatalf("second Stop error = %v, want cached stop errors", errAgain)
	}
	if got := a.stopCall.Load(); got != 1 {
		t.Fatalf("a Stop calls = %d, want 1", got)
	}
	if got := b.stopCall.Load(); got != 1 {
		t.Fatalf("b Stop calls = %d, want 1", got)
	}
}

func TestPluginRegistryConcurrentStopCallsPluginOnce(t *testing.T) {
	t.Parallel()

	a := &testPlugin{name: "a"}
	registry := NewPluginRegistry()
	if err := registry.Register(a); err != nil {
		t.Fatal(err)
	}
	if err := registry.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			if err := registry.Stop(t.Context()); err != nil {
				t.Errorf("Stop: %v", err)
			}
		})
	}
	wg.Wait()

	if got := a.stopCall.Load(); got != 1 {
		t.Fatalf("Stop calls = %d, want 1", got)
	}
}

func TestPluginRegistryCancellationStopsCompletedPlugins(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	a := &testPlugin{name: "a"}
	registry := NewPluginRegistry()
	if err := registry.Register(a); err != nil {
		t.Fatal(err)
	}

	err := registry.Init(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Init error = %v, want context.Canceled", err)
	}
	if got := a.stopCall.Load(); got != 0 {
		t.Fatalf("Stop calls = %d, want 0 when cancellation happens before Init", got)
	}
}
