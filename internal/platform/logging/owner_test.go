package logging

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/gtkit/gin-api/internal/core"
	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/logger/v2"
)

func TestOpenFailures(t *testing.T) {
	failure := errors.New("constructor failed")
	factory := func(...logger.Option) (*logger.Logger, error) { return newConsoleLogger(), nil }
	for _, tc := range []struct {
		name    string
		options Options
		want    error
	}{
		{"missing config", Options{}, core.ErrComponentState},
		{"main nil", Options{Config: &config.Config{}, MainFactory: func(...logger.Option) (*logger.Logger, error) { return nil, nil }}, core.ErrComponentState},
		{"main partial", Options{Config: &config.Config{}, MainFactory: func(...logger.Option) (*logger.Logger, error) { return newConsoleLogger(), failure }}, failure},
		{"access failed", Options{Config: &config.Config{}, MainFactory: factory, AccessFactory: func(...logger.Option) (*logger.Logger, error) { return newConsoleLogger(), failure }}, failure},
		{"access nil", Options{Config: &config.Config{}, MainFactory: factory, AccessFactory: func(...logger.Option) (*logger.Logger, error) { return nil, nil }}, core.ErrComponentState},
		{"main panic", Options{Config: &config.Config{}, MainFactory: func(...logger.Option) (*logger.Logger, error) { panic("factory") }}, core.ErrComponentState},
		{"access panic", Options{Config: &config.Config{}, MainFactory: factory, AccessFactory: func(...logger.Option) (*logger.Logger, error) { panic("factory") }}, core.ErrComponentState},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := logger.Default()
			p, err := Open(t.Context(), tc.options)
			if p != nil || !errors.Is(err, tc.want) {
				t.Fatalf("owner=%v err=%v", p, err)
			}
			if logger.Default() != previous {
				t.Fatal("failed Open replaced default logger")
			}
			// Partial cleanup must also release the facade before a later successful Open.
			healthy, err := Open(t.Context(), Options{Config: &config.Config{}, MainFactory: factory, AccessFactory: factory})
			if err != nil {
				t.Fatal(err)
			}
			if err := healthy.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p, err := Open(ctx, Options{Config: &config.Config{}})
	if p != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("owner=%v err=%v", p, err)
	}
}

func TestOwnerWritesStreamsFieldsAndAlerts(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Log: config.LogConfig{Path: filepath.Join(dir, "app"), Level: "debug", FileStdout: 1}}
	alerts := &messages{}
	p := mustOpen(t, Options{Config: cfg, MainFactory: func(opts ...logger.Option) (*logger.Logger, error) {
		return logger.New(append(opts, logger.WithMessager(alerts), logger.WithReplaceGlobals(false))...)
	}})
	t.Cleanup(func() {
		if err := p.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("p.Close(context.WithoutCancel(t.Context())): %v", err)
		}
	})
	ctx := applog.ContextWithRequestID(t.Context(), "request-123")
	applog.ErrorCtx(ctx, "ordinary error", applog.String("key", "value"), applog.Err(errors.New("problem")))
	p.Log(ctx, applog.LogEntry{Level: applog.LevelInfo, Message: "access-event", Stream: applog.AccessStream})
	p.Log(ctx, applog.LogEntry{Level: applog.LevelWarn, Message: "channel-event", Channel: "contract"})
	applog.Debug("debug-event")
	applog.Infof("info %d", 2)
	applog.Warn("warn-event")
	applog.HErrorf("alert %d", 1)
	if err := p.Close(t.Context()); err != nil {
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
		if err != nil {
			return err
		}
		_, err = out.Write(data)
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
	synctest.Test(t, func(t *testing.T) {
		p := mustOpen(t, Options{Config: &config.Config{Log: config.LogConfig{ConsoleStdout: 1}}})
		block := &blockingStringer{entered: make(chan struct{}), release: make(chan struct{})}
		var work sync.WaitGroup
		work.Go(func() {
			p.Log(t.Context(), applog.LogEntry{Level: applog.LevelInfo, Message: "inflight", Fields: []applog.Field{applog.Any("blocked", block)}})
		})
		<-block.entered
		stopped := make(chan error, 1)
		work.Go(func() { stopped <- p.Close(context.WithoutCancel(t.Context())) })
		synctest.Wait()
		select {
		case err := <-stopped:
			close(block.release)
			work.Wait()
			t.Fatalf("stop passed live write: %v", err)
		default:
		}
		if !p.current.Load().closing.Load() {
			close(block.release)
			work.Wait()
			t.Fatal("stop did not close admission")
		}
		p.Log(t.Context(), applog.LogEntry{Message: "rejected"})
		close(block.release)
		work.Wait()
		if err := <-stopped; err != nil {
			t.Fatal(err)
		}
		var group sync.WaitGroup
		for range 16 {
			group.Go(func() {
				if err := p.Close(context.WithoutCancel(t.Context())); err != nil {
					t.Error(err)
				}
			})
		}
		group.Wait()
	})
}

func TestOpenCancellationAfterConstructionReleasesOwnedLogger(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	previous := logger.Default()
	p, err := Open(ctx, Options{Config: &config.Config{}, MainFactory: func(...logger.Option) (*logger.Logger, error) { cancel(); return newConsoleLogger(), nil }, AccessFactory: func(...logger.Option) (*logger.Logger, error) { return newConsoleLogger(), nil }})
	if p != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("owner=%v err=%v", p, err)
	}
	if logger.Default() != previous {
		t.Fatal("canceled construction leaked default logger")
	}
}
func mustOpen(t *testing.T, options Options) *Owner {
	t.Helper()
	p, err := Open(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
