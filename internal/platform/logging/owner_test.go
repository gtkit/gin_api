package logging

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/logger/v2"
	"go.uber.org/zap"
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
	ctx := logger.ContextWithRequestID(t.Context(), "request-123")
	logger.ErrorCtx(ctx, "ordinary error", zap.String("key", "value"), zap.Error(errors.New("problem")))
	p.AccessLogger().InfoCtx(ctx, "access-event")
	logger.Default().Channel("contract").WarnCtx(ctx, "channel-event")
	logger.Debug("debug-event")
	logger.Infof("info %d", 2)
	logger.Warn("warn-event")
	logger.HError(fmt.Sprintf("alert %d", 1))
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

func TestDrainKeepsNativeInstancesUsableUntilClose(t *testing.T) {
	p, dir := newJSONOwner(t, "info", nil)
	main, access := logger.Default(), p.AccessLogger()
	logger.Info("before flush")
	access.Info("access before flush")
	if err := p.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if logger.Default() != main {
		t.Fatal("flush replaced the active default")
	}
	logger.Info("after flush")
	access.Info("access after flush")
	if err := p.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	records := jsonRecords(t, dir)
	for _, message := range []string{"before flush", "access before flush", "after flush", "access after flush"} {
		if records[message] == nil {
			t.Fatalf("missing %q after flush", message)
		}
	}
	var callers sync.WaitGroup
	for range 16 {
		callers.Go(func() {
			if err := p.Close(context.WithoutCancel(t.Context())); err != nil {
				t.Error(err)
			}
		})
	}
	callers.Wait()
	if logger.Default() == main {
		t.Fatal("safe close did not restore previous default")
	}
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

func TestNativeFallbackWritesOnlyStderr(t *testing.T) {
	const marker = "GIN_API_TEST_NATIVE_FALLBACK"
	if os.Getenv(marker) == "1" {
		logger.InfoCtx(logger.ContextWithRequestID(t.Context(), "startup-request"), "native startup diagnostic", zap.String("operation", "startup"))
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	command := exec.CommandContext(t.Context(), executable, "-test.run=^TestNativeFallbackWritesOnlyStderr$")
	command.Dir = dir
	command.Env = append(os.Environ(), marker+"=1")
	var stdout, stderr strings.Builder
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("fallback child: %v: %s", err, stderr.String())
	}
	for _, value := range []string{"native startup diagnostic", "startup-request", "operation"} {
		if !strings.Contains(stderr.String(), value) {
			t.Fatalf("missing %q in stderr: %s", value, stderr.String())
		}
	}
	if strings.Contains(stdout.String(), "native startup diagnostic") {
		t.Fatal("fallback wrote to stdout")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("fallback created files: %v", entries)
	}
}

func TestExplicitAlertFormatsOnceForOutputAndNotification(t *testing.T) {
	messages := new(messages)
	p, dir := newJSONOwner(t, "info", messages)
	value := new(countedValue)
	logger.HError(fmt.Sprintf("alert %s", value))
	if err := p.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if value.calls.Load() != 1 || len(messages.values) != 1 || messages.values[0] != "alert value" || !strings.Contains(readLogs(t, dir), "alert value") {
		t.Fatalf("format calls=%d messages=%v", value.calls.Load(), messages.values)
	}
}
