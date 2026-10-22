package httpmiddleware

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/middleware"
	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/plugin"
	"go.opentelemetry.io/otel"
)

type configuration struct {
	cfg    *config.Config
	err    error
	reads  int
	cancel context.CancelFunc
}

func (c *configuration) Value() (*config.Config, error) {
	c.reads++
	if c.cancel != nil {
		c.cancel()
	}
	return c.cfg, c.err
}
func finalDeps(t *testing.T) plugin.MiddlewareDeps {
	t.Helper()
	recorder, err := metrics.New(otel.Meter("middleware-contract"))
	if err != nil {
		t.Fatal(err)
	}
	return plugin.MiddlewareDeps{Metrics: recorder, ServiceName: "contract"}
}
func TestInitAndBuildContracts(t *testing.T) {
	root := errors.New("config failed")
	for _, tc := range []struct {
		name                     string
		provider                 *configuration
		cancelBefore, lateCancel bool
		want                     error
	}{
		{name: "missing"}, {name: "failed", provider: &configuration{err: root}, want: root}, {name: "nil config", provider: &configuration{}, want: core.ErrPluginContractViolation}, {name: "canceled", cancelBefore: true, want: context.Canceled}, {name: "late cancellation", provider: &configuration{cfg: &config.Config{}}, lateCancel: true, want: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			opts := Options{}
			if tc.provider != nil {
				opts.Configuration = tc.provider
				if tc.lateCancel {
					tc.provider.cancel = cancel
				}
			}
			if tc.cancelBefore {
				cancel()
			}
			p := New(opts)
			if p.Name() != "http-middleware" {
				t.Fatal(p.Name())
			}
			if _, err := p.Build(finalDeps(t)); err == nil {
				t.Fatal("build before start")
			}
			if err := p.Start(t.Context()); err == nil {
				t.Fatal("start before init")
			}
			err := p.Init(ctx)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("init=%v", err)
			}
			if p.Init(t.Context()) != err {
				t.Fatal("init error not cached")
			}
			if p.Start(t.Context()) == nil {
				t.Fatal("start after failure")
			}
			if err := p.Stop(t.Context()); err != nil {
				t.Errorf("p.Stop(t.Context()): %v", err)
			}
			if err := p.Stop(t.Context()); err != nil {
				t.Errorf("p.Stop(t.Context()): %v", err)
			}
		})
	}
	p := New(Options{})
	if err := p.Stop(t.Context()); err != nil {
		t.Errorf("p.Stop(t.Context()): %v", err)
	}
	if !errors.Is(p.Init(t.Context()), core.ErrPluginContractViolation) {
		t.Fatal("init after stop")
	}
}
func TestBuildExactOrderAndFrozenConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name          string
		cors, tracing bool
	}{
		{name: "base"}, {name: "CORS", cors: true}, {name: "tracing", tracing: true}, {name: "all", cors: true, tracing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			if tc.cors {
				cfg.CORS.AllowedOrigins = []string{"https://example.com"}
			}
			provider := &configuration{cfg: cfg}
			factoryCalls := 0
			p := New(Options{Configuration: provider, RequestIDFactory: func() (*middleware.RequestIDGenerator, error) {
				factoryCalls++
				return middleware.NewRequestIDGenerator()
			}})
			if provider.reads != 0 || factoryCalls != 0 {
				t.Fatal("construction performed work")
			}
			if err := p.Init(t.Context()); err != nil {
				t.Fatal(err)
			}
			if factoryCalls != 0 {
				t.Fatal("Init captured handlers before final instruments")
			}
			if tc.cors {
				cfg.CORS.AllowedOrigins[0] = "https://changed.invalid"
			}
			if err := p.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			deps := finalDeps(t)
			deps.TracingEnabled = tc.tracing
			chain, err := p.Build(deps)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"GinRecovery", "RequestID"}
			if tc.tracing {
				want = append(want, "Middleware")
			}
			want = append(want, "GinLogger", "Metrics", "GinRecovery", "SecurityHeaders", "ErrorHandler")
			if tc.cors {
				want = append(want, "Cors")
			}
			want = append(want, "MaxBytesLimit")
			got := make([]string, 0, len(chain))
			for _, handler := range chain {
				full := runtime.FuncForPC(reflect.ValueOf(handler).Pointer()).Name()
				parts := strings.Split(full, ".")
				for i, part := range parts {
					if strings.HasPrefix(part, "func") && i > 0 {
						got = append(got, parts[i-1])
						break
					}
				}
			}
			if !slices.Equal(got, want) {
				t.Fatalf("chain=%v want=%v", got, want)
			}
			if factoryCalls != 1 || provider.reads != 1 {
				t.Fatal("unexpected construction count")
			}
			if tc.cors && p.origins[0] != "https://example.com" {
				t.Fatal("retained mutable configuration slice")
			}
			if _, err := p.Build(deps); !errors.Is(err, plugin.ErrMiddlewareAlreadyBuilt) {
				t.Fatalf("duplicate Build=%v", err)
			}
			if err := p.Stop(t.Context()); err != nil {
				t.Errorf("p.Stop(t.Context()): %v", err)
			}
			if _, err := p.Build(deps); !errors.Is(err, core.ErrPluginContractViolation) {
				t.Fatal("build after stop")
			}
		})
	}
}
func TestBuildFailuresAreTerminalAndPreserveRoot(t *testing.T) {
	root := errors.New("random source failed")
	for _, tc := range []struct {
		name            string
		factory         RequestIDFactory
		missingRecorder bool
		want            error
	}{
		{name: "missing recorder", missingRecorder: true},
		{name: "factory error", factory: func() (*middleware.RequestIDGenerator, error) { return nil, root }, want: root},
		{name: "nil generator", factory: func() (*middleware.RequestIDGenerator, error) { return nil, nil }, want: core.ErrPluginContractViolation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := New(Options{Configuration: &configuration{cfg: &config.Config{}}, RequestIDFactory: tc.factory})
			if err := p.Init(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := p.Build(finalDeps(t)); err == nil {
				t.Fatal("Build before Start")
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if !errors.Is(p.Start(ctx), context.Canceled) {
				t.Fatal("start swallowed cancellation")
			}
			if err := p.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			deps := finalDeps(t)
			if tc.missingRecorder {
				deps.Metrics = nil
			}
			_, err := p.Build(deps)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatal(err)
			}
			_, again := p.Build(finalDeps(t))
			if !errors.Is(again, plugin.ErrMiddlewareAlreadyBuilt) || (tc.want != nil && !errors.Is(again, tc.want)) {
				t.Fatal(again)
			}
			if err := p.Stop(t.Context()); err != nil {
				t.Errorf("p.Stop(t.Context()): %v", err)
			}
		})
	}
}
func TestConcurrentBuildOnlyConstructsOnce(t *testing.T) {
	var constructions, success, duplicates atomic.Int32
	p := New(Options{Configuration: &configuration{cfg: &config.Config{}}, RequestIDFactory: func() (*middleware.RequestIDGenerator, error) {
		constructions.Add(1)
		return middleware.NewRequestIDGenerator()
	}})
	if err := p.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	deps := finalDeps(t)
	var group sync.WaitGroup
	for range 32 {
		group.Go(func() {
			_, err := p.Build(deps)
			switch {
			case err == nil:
				success.Add(1)
			case errors.Is(err, plugin.ErrMiddlewareAlreadyBuilt):
				duplicates.Add(1)
			default:
				t.Error(err)
			}
		})
	}
	group.Wait()
	if success.Load() != 1 || duplicates.Load() != 31 || constructions.Load() != 1 {
		t.Fatalf("success=%d duplicate=%d factory=%d", success.Load(), duplicates.Load(), constructions.Load())
	}
	if err := p.Stop(t.Context()); err != nil {
		t.Errorf("p.Stop(t.Context()): %v", err)
	}
}
