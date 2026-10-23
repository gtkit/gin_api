package logging

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/json/v2"
	"github.com/gtkit/logger/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func newJSONOwner(t *testing.T, level string, alerts logger.Messager) (*Owner, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{Log: config.LogConfig{Path: filepath.Join(dir, "app"), Level: level, FileStdout: 1}}
	factory := func(opts ...logger.Option) (*logger.Logger, error) {
		return logger.New(append(opts, logger.WithOutJSON(true), logger.WithReplaceGlobals(false), logger.WithMessager(alerts), logger.WithRedactKeys("secret"))...)
	}
	p := mustOpen(t, Options{Config: cfg, MainFactory: factory, AccessFactory: factory})
	t.Cleanup(func() {
		if err := p.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("p.Close(context.WithoutCancel(t.Context())): %v", err)
		}
	})
	return p, dir
}

func setSDKLevel(t *testing.T, instance *logger.Logger, level string) {
	t.Helper()
	if err := instance.SetLevel(level); err != nil {
		t.Fatal(err)
	}
}

type countedValue struct{ calls atomic.Int32 }

func (v *countedValue) String() string { v.calls.Add(1); return "value" }

type panicValueContext struct{ context.Context }

func (panicValueContext) Value(any) any { panic("context value failure") }

func TestFilteringPrecedesValuesAndTracksDynamicStreams(t *testing.T) {
	p, dir := newJSONOwner(t, "error", nil)
	value := new(countedValue)
	logger.Debugf("filtered %s", value)
	logger.InfoCtx(panicValueContext{t.Context()}, "filtered", zap.Any("value", value))
	if value.calls.Load() != 0 || p.main.Zap().Core().Enabled(zapcore.InfoLevel) {
		t.Fatal("suppressed values were materialized")
	}
	setSDKLevel(t, p.main, "debug")
	if !p.main.Zap().Core().Enabled(zapcore.DebugLevel) || p.access.Zap().Core().Enabled(zapcore.DebugLevel) {
		t.Fatal("cached a static level or mixed main/access levels")
	}
	logger.Debugf("enabled %s", value)
	if value.calls.Load() != 1 {
		t.Fatal("dynamic level did not enable formatting")
	}
	if err := p.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readLogs(t, dir), "enabled value") {
		t.Fatal("dynamic output/closed capability mismatch")
	}
}

func TestAlertSurvivesOrdinaryErrorFiltering(t *testing.T) {
	alerts := new(messages)
	p, _ := newJSONOwner(t, "fatal", alerts)
	if logger.Default().Zap().Core().Enabled(zapcore.ErrorLevel) {
		t.Fatal("test did not suppress ordinary errors")
	}
	logger.HError(fmt.Sprintf("must deliver %d", 7))
	if err := p.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(alerts.values) != 1 || alerts.values[0] != "must deliver 7" {
		t.Fatalf("alerts=%q", alerts.values)
	}
}

func jsonRecords(t *testing.T, dir string) map[string]map[string]any {
	t.Helper()
	entries := make(map[string]map[string]any)
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for line := range strings.Lines(string(contents)) {
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				return err
			}
			message, ok := record["msg"].(string)
			if !ok {
				t.Fatalf("log field has type %T", record["msg"])
			}
			record["output_file"] = filepath.Base(path)
			entries[message] = record
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestNativeCallerFieldsRequestIDAndStreams(t *testing.T) {
	p, dir := newJSONOwner(t, "debug", nil)
	ctx := logger.ContextWithRequestID(t.Context(), "caller-request")
	_, file, facadeLine, _ := runtime.Caller(0)
	logger.Info("facade", zap.String("name", "first"))
	_, _, directLine, _ := runtime.Caller(0)
	logger.InfoCtx(ctx, "direct", zap.Skip(), zap.String("name", "second"), zap.String("secret", "private-value"))
	_, _, forwardedLine, _ := runtime.Caller(0)
	logger.InfoCtx(ctx, "forwarded")
	_, _, accessLine, _ := runtime.Caller(0)
	p.AccessLogger().InfoCtx(ctx, "access")
	gormLogger := data.NewGormLogger(&config.Config{Log: config.LogConfig{SQL: 1}})
	_, _, traceLine, _ := runtime.Caller(0)
	gormLogger.Trace(ctx, time.Now(), func() (string, int64) { return "SELECT ?", 1 }, nil)
	_, _, infoLine, _ := runtime.Caller(0)
	gormLogger.Info(ctx, "gorm info %d", 1)
	logger.Default().Channel("contract").WarnCtx(ctx, "channel", zap.Int("count", 9))
	large := make([]zap.Field, 9)
	for i := range large {
		large[i] = zap.Int(fmt.Sprintf("field%d", i), i)
	}
	logger.InfoCtx(ctx, "large", large...)
	logger.InfoCtx(ctx, "empty")
	if err := p.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	records := jsonRecords(t, dir)
	for _, tc := range []struct {
		message string
		line    int
	}{
		{"facade", facadeLine + 1}, {"direct", directLine + 1},
		{"forwarded", forwardedLine + 1}, {"access", accessLine + 1},
	} {
		record := records[tc.message]
		caller, ok := record["caller"].(string)
		if !ok {
			t.Fatalf("log field has type %T", record["caller"])
		}
		if !strings.HasSuffix(caller, fmt.Sprintf("%s:%d", filepath.Base(file), tc.line)) {
			t.Errorf("%s caller=%q want line=%d", tc.message, caller, tc.line)
		}
	}
	// Preserve the existing GORM contract: zap caller is the adapter frame;
	// source identifies the user's query call site.
	for _, tc := range []struct {
		message string
		line    int
	}{{"gorm query", traceLine + 1}, {"gorm info 1", infoLine + 1}} {
		record := records[tc.message]
		caller, ok := record["caller"].(string)
		if !ok {
			t.Fatalf("log field has type %T", record["caller"])
		}
		source, ok := record["source"].(string)
		if !ok {
			t.Fatalf("log field has type %T", record["source"])
		}
		if !strings.HasPrefix(caller, "data/init.go:") || !strings.HasSuffix(source, fmt.Sprintf("%s:%d", filepath.Base(file), tc.line)) {
			t.Errorf("%s caller=%q source=%q", tc.message, caller, source)
		}
	}
	for _, name := range []string{"direct", "forwarded", "access", "gorm query", "gorm info 1", "channel", "large", "empty"} {
		if records[name]["request_id"] != "caller-request" {
			t.Errorf("%s lost request ID: %v", name, records[name])
		}
	}
	if records["direct"]["name"] != "second" || strings.Contains(readLogs(t, dir), "private-value") {
		t.Fatal("field conversion or SDK redaction changed")
	}
	if records["channel"]["channel"] != "contract" || records["channel"]["count"] != float64(9) {
		t.Fatal("channel fields changed")
	}
	if output, ok := records["access"]["output_file"].(string); !ok || !strings.Contains(output, "app_access") {
		t.Fatalf("access routed to %q", output)
	}
	if records["large"]["field8"] != float64(8) || records["empty"]["name"] != nil {
		t.Fatal("oversized fields truncated or old pooled fields leaked")
	}
	if records["gorm query"]["sql"] != "SELECT ?" || records["gorm query"]["trace_id"] != "caller-request" {
		t.Fatal("SQL parameter/request correlation changed")
	}
}

func TestConcurrentNativeWritesAndDynamicLevelsFinishBeforeClose(t *testing.T) {
	p, _ := newJSONOwner(t, "info", nil)
	var writers sync.WaitGroup
	for range 16 {
		writers.Go(func() {
			for range 50 {
				logger.InfoCtx(t.Context(), "concurrent", zap.Int("count", 1))
			}
		})
	}
	writers.Go(func() {
		for range 50 {
			setSDKLevel(t, p.main, "error")
			setSDKLevel(t, p.main, "info")
		}
	})
	writers.Wait()
	if err := p.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestDuplicateOpenPreservesOwnerAndDefault(t *testing.T) {
	p, _ := newJSONOwner(t, "info", nil)
	main := logger.Default()
	constructions := 0
	factory := func(...logger.Option) (*logger.Logger, error) { constructions++; return newConsoleLogger(), nil }
	other, err := Open(t.Context(), Options{Config: &config.Config{}, MainFactory: factory, AccessFactory: factory})
	if other != nil || !errors.Is(err, core.ErrComponentState) || constructions != 0 || logger.Default() != main {
		t.Fatalf("duplicate open: owner=%v err=%v constructions=%d", other, err, constructions)
	}
	if err := p.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCloseDoesNotReplaceSuccessorDefault(t *testing.T) {
	p, _ := newJSONOwner(t, "info", nil)
	previous := p.previous
	next := newConsoleLogger()
	logger.SetDefault(next)
	defer func() { logger.SetDefault(previous); next.Sync() }()
	if err := p.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if logger.Default() != next {
		t.Fatal("old owner replaced new default")
	}
}
