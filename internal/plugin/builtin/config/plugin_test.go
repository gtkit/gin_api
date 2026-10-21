package config

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gtkit/gin-api/internal/core"
	schema "github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/plugin"
)

func validatedFixture(t *testing.T) *schema.Config {
	t.Helper()
	cfg, err := loadFromReader(strings.NewReader(minimalConfigYAML()))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestConfigurationPluginLoadsOnlyDuringInit(t *testing.T) {
	cfg := validatedFixture(t)
	var calls atomic.Int32
	p := New(func(ctx context.Context) (*schema.Config, error) { calls.Add(1); return cfg, ctx.Err() })
	if calls.Load() != 0 {
		t.Fatal("constructor loaded configuration")
	}
	if got, err := p.Value(); got != nil || !errors.Is(err, plugin.ErrConfigurationUnavailable) {
		t.Fatalf("Value before Init = %v, %v", got, err)
	}
	if err := p.Start(t.Context()); !errors.Is(err, plugin.ErrConfigurationUnavailable) {
		t.Fatalf("Start before Init = %v", err)
	}
	r := core.NewPluginRegistry()
	if err := r.Register(p); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("registration loaded configuration")
	}
	if err := r.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, err := p.Value(); err != nil || got != cfg {
		t.Fatalf("Value after Init = %p, %v", got, err)
	}
	if err := r.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Errorf("loads = %d, want 1", calls.Load())
	}
}

func TestConfigurationPluginDefaultLoader(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APP_ROOT", root)
	writeJWTCommandConfig(t, root, minimalConfigYAML())
	p := New(nil)
	if err := p.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, err := p.Value(); err != nil || got.App.Port != "8008" {
		t.Fatalf("Value = %v, %v", got, err)
	}
}

func TestConfigurationPluginRejectsInvalidResults(t *testing.T) {
	root := errors.New("loader failed")
	for _, tt := range []struct {
		name     string
		loader   Loader
		canceled bool
		want     error
	}{
		{name: "loader failure", loader: func(context.Context) (*schema.Config, error) { return nil, root }, want: root},
		{name: "nil snapshot", loader: func(context.Context) (*schema.Config, error) { return nil, nil }, want: core.ErrPluginContractViolation},
		{name: "invalid snapshot", loader: func(context.Context) (*schema.Config, error) { return &schema.Config{}, nil }},
		{name: "canceled before load", loader: func(context.Context) (*schema.Config, error) {
			t.Error("canceled Init invoked loader")
			return nil, nil
		}, canceled: true, want: context.Canceled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tt.canceled {
				cancel()
			}
			p := New(tt.loader)
			err := p.Init(ctx)
			if err == nil || (tt.want != nil && !errors.Is(err, tt.want)) {
				t.Fatalf("Init = %v, want failure %v", err, tt.want)
			}
			if again := p.Init(t.Context()); !errors.Is(again, err) {
				t.Errorf("Init did not return cached failure: %v", again)
			}
			if got, valueErr := p.Value(); got != nil || !errors.Is(valueErr, plugin.ErrConfigurationUnavailable) || (tt.want != nil && !errors.Is(valueErr, tt.want)) {
				t.Errorf("Value after failure = %v, %v", got, valueErr)
			}
			if stopErr := p.Stop(t.Context()); stopErr != nil {
				t.Fatal(stopErr)
			}
		})
	}
}

func TestConfigurationPluginConcurrentInitAndSnapshotPublication(t *testing.T) {
	cfg := validatedFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	p := New(func(context.Context) (*schema.Config, error) {
		calls.Add(1)
		close(entered)
		<-release
		return cfg, nil
	})
	var callers sync.WaitGroup
	callers.Go(func() {
		if err := p.Init(t.Context()); err != nil {
			t.Error(err)
		}
	})
	<-entered
	for range 32 {
		callers.Go(func() {
			if err := p.Init(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	if got, err := p.Value(); got != nil || !errors.Is(err, plugin.ErrConfigurationUnavailable) {
		t.Errorf("partially published snapshot = %v, %v", got, err)
	}
	close(release)
	callers.Wait()
	if calls.Load() != 1 {
		t.Errorf("loads = %d, want 1", calls.Load())
	}
	var readers sync.WaitGroup
	for range 32 {
		readers.Go(func() {
			if got, err := p.Value(); got != cfg || err != nil {
				t.Errorf("published snapshot = %p, %v", got, err)
			}
		})
	}
	readers.Wait()
}

func TestConfigurationPluginRejectsLateSuccess(t *testing.T) {
	cfg := validatedFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	p := New(func(context.Context) (*schema.Config, error) { cancel(); return cfg, nil })
	if err := p.Init(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Init = %v", err)
	}
	if got, err := p.Value(); got != nil || !errors.Is(err, plugin.ErrConfigurationUnavailable) {
		t.Fatalf("late snapshot published: %p, %v", got, err)
	}
}
