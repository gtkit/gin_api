package logging

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/core"
	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/platform/config"
	sdk "github.com/gtkit/logger/v2"
)

type failedConfiguration struct{ err error }

func (f failedConfiguration) Value() (*config.Config, error) { return nil, f.err }
func TestPluginLifecycleAndFailures(t *testing.T) {
	failure := errors.New("constructor failed")
	for _, tc := range []struct {
		name    string
		options Options
		want    error
	}{
		{"missing config", Options{}, nil},
		{"config failed", Options{Configuration: failedConfiguration{failure}}, failure},
		{"config nil", Options{Configuration: failedConfiguration{}}, core.ErrPluginContractViolation},
		{"main nil", Options{Configuration: fixedConfiguration{&config.Config{}}, MainFactory: func(...sdk.Option) (*sdk.Logger, error) { return nil, nil }}, core.ErrPluginContractViolation},
		{"main partial", Options{Configuration: fixedConfiguration{&config.Config{}}, MainFactory: func(...sdk.Option) (*sdk.Logger, error) { return newConsoleLogger(), failure }}, failure},
		{"access failed", Options{Configuration: fixedConfiguration{&config.Config{}}, MainFactory: func(...sdk.Option) (*sdk.Logger, error) { return newConsoleLogger(), nil }, AccessFactory: func(...sdk.Option) (*sdk.Logger, error) { return newConsoleLogger(), failure }}, failure},
		{"access nil", Options{Configuration: fixedConfiguration{&config.Config{}}, MainFactory: func(...sdk.Option) (*sdk.Logger, error) { return newConsoleLogger(), nil }, AccessFactory: func(...sdk.Option) (*sdk.Logger, error) { return nil, nil }}, core.ErrPluginContractViolation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := New(tc.options)
			if p.Name() != "logging" {
				t.Fatal(p.Name())
			}
			if err := p.Start(t.Context()); err == nil {
				t.Fatal("started before init")
			}
			err := p.Init(t.Context())
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("init=%v", err)
			}
			if again := p.Init(t.Context()); again != err {
				t.Fatalf("uncached init=%v", again)
			}
			if err := p.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := p.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := p.Start(t.Context()); err == nil {
				t.Fatal("start after failure")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p := New(Options{})
	if !errors.Is(p.Init(ctx), context.Canceled) {
		t.Fatal("ignored init cancellation")
	}
	_ = p.Stop(t.Context())
	stopped := New(Options{})
	_ = stopped.Stop(t.Context())
	if !errors.Is(stopped.Init(t.Context()), core.ErrPluginContractViolation) {
		t.Fatal("init after stop")
	}
}
func TestPluginWritesStreamsFieldsAndAlerts(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Log: config.LogConfig{Path: filepath.Join(dir, "app"), Level: "debug", FileStdout: 1}}
	alerts := &messages{}
	p := New(Options{Configuration: fixedConfiguration{cfg}, MainFactory: func(opts ...sdk.Option) (*sdk.Logger, error) {
		return sdk.New(append(opts, sdk.WithMessager(alerts), sdk.WithReplaceGlobals(false))...)
	}})
	if err := p.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	if err := p.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx := applog.ContextWithRequestID(t.Context(), "request-123")
	applog.ErrorCtx(ctx, "ordinary error", applog.String("key", "value"), applog.Err(errors.New("problem")))
	p.Log(ctx, applog.LogEntry{Level: applog.LevelInfo, Message: "access-event", Stream: applog.AccessStream})
	p.Log(ctx, applog.LogEntry{Level: applog.LevelWarn, Message: "channel-event", Channel: "contract"})
	applog.Debug("debug-event")
	applog.Infof("info %d", 2)
	applog.Warn("warn-event")
	applog.HErrorf("alert %d", 1)
	if err := p.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(alerts.values) != 1 || alerts.values[0] != "alert 1" {
		t.Fatalf("alerts=%q", alerts.values)
	}
	all := readLogs(t, dir)
	for _, value := range []string{"ordinary error", "key", "value", "problem", "request-123", "access-event", "channel-event", "debug-event", "info 2", "warn-event"} {
		if !strings.Contains(all, value) {
			t.Errorf("missing %q in %s", value, all)
		}
	}
	p.Log(ctx, applog.LogEntry{Message: "late-write"})
	if got := readLogs(t, dir); got != all {
		t.Fatal("late write reached stopped SDK")
	}
}

type messages struct{ values []string }

func (m *messages) Send(value string)      { m.values = append(m.values, value) }
func (m *messages) SendTo(_, value string) { m.Send(value) }
func readLogs(t *testing.T, dir string) string {
	t.Helper()
	var out strings.Builder
	if err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		out.Write(data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

type blockingStringer struct {
	entered, release chan struct{}
	once             sync.Once
}

func (b *blockingStringer) String() string {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return "unblocked"
}
func TestStopWaitsForAdmittedWritesAndClosesOnce(t *testing.T) {
	p := New(Options{Configuration: fixedConfiguration{&config.Config{Log: config.LogConfig{ConsoleStdout: 1}}}})
	if err := p.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	block := &blockingStringer{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		p.Log(t.Context(), applog.LogEntry{Level: applog.LevelInfo, Message: "inflight", Fields: []applog.Field{applog.Any("blocked", block)}})
		close(done)
	}()
	<-block.entered
	stopped := make(chan error, 1)
	go func() { stopped <- p.Stop(context.Background()) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		p.gate.Lock()
		closing := p.closing
		p.gate.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			close(block.release)
			t.Fatal("stop did not close admission")
		}
		runtime.Gosched()
	}
	select {
	case err := <-stopped:
		close(block.release)
		t.Fatalf("stop passed live write: %v", err)
	default:
	}
	p.Log(t.Context(), applog.LogEntry{Message: "rejected"})
	close(block.release)
	<-done
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			if err := p.Stop(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
}

func TestInitCancellationAfterConstructionReleasesOwnedLogger(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	p := New(Options{Configuration: fixedConfiguration{&config.Config{}}, MainFactory: func(...sdk.Option) (*sdk.Logger, error) { cancel(); return newConsoleLogger(), nil }, AccessFactory: func(...sdk.Option) (*sdk.Logger, error) { return newConsoleLogger(), nil }})
	if err := p.Init(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("init=%v", err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	if err := p.Start(t.Context()); err == nil {
		t.Fatal("canceled Init left its resources published and startable")
	}
}
