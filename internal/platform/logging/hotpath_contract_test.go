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
	"testing/synctest"
	"time"

	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/json/v2"
	"github.com/gtkit/logger/v2"
	"go.uber.org/zap"
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
	applog.Debugf("filtered %s", value)
	p.Log(panicValueContext{t.Context()}, applog.LogEntry{Level: applog.LevelInfo, Message: "filtered", Fields: []applog.Field{applog.Any("value", value)}})
	if value.calls.Load() != 0 || p.Enabled(applog.LevelInfo, applog.MainStream) {
		t.Fatal("suppressed values were materialized")
	}
	setSDKLevel(t, p.main, "debug")
	if !p.Enabled(applog.LevelDebug, applog.MainStream) || p.Enabled(applog.LevelDebug, applog.AccessStream) {
		t.Fatal("cached a static level or mixed main/access levels")
	}
	applog.Debugf("enabled %s", value)
	if value.calls.Load() != 1 {
		t.Fatal("dynamic level did not enable formatting")
	}
	if err := p.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if p.Enabled(applog.LevelError, applog.MainStream) || !strings.Contains(readLogs(t, dir), "enabled value") {
		t.Fatal("dynamic output/closed capability mismatch")
	}
}

func TestAlertSurvivesOrdinaryErrorFiltering(t *testing.T) {
	alerts := new(messages)
	p, _ := newJSONOwner(t, "fatal", alerts)
	if applog.Enabled(applog.LevelError, applog.MainStream) {
		t.Fatal("test did not suppress ordinary errors")
	}
	applog.HErrorf("must deliver %d", 7)
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

func TestCachedCallerFieldsRequestIDAndStreams(t *testing.T) {
	p, dir := newJSONOwner(t, "debug", nil)
	ctx := applog.ContextWithRequestID(t.Context(), "caller-request")
	_, file, facadeLine, _ := runtime.Caller(0)
	applog.Info("facade", applog.String("name", "first"))
	_, _, directLine, _ := runtime.Caller(0)
	p.Log(ctx, applog.LogEntry{Level: applog.LevelInfo, Message: "direct", Fields: []applog.Field{{}, applog.String("name", "second"), applog.String("secret", "private-value")}})
	_, _, forwardedLine, _ := runtime.Caller(0)
	applog.Log(ctx, applog.LogEntry{Level: applog.LevelInfo, Message: "forwarded", CallerSkip: 1})
	_, _, accessLine, _ := runtime.Caller(0)
	applog.Log(ctx, applog.LogEntry{Level: applog.LevelInfo, Message: "access", Stream: applog.AccessStream})
	gormLogger := data.NewGormLogger(&config.Config{Log: config.LogConfig{SQL: 1}})
	_, _, traceLine, _ := runtime.Caller(0)
	gormLogger.Trace(ctx, time.Now(), func() (string, int64) { return "SELECT ?", 1 }, nil)
	_, _, infoLine, _ := runtime.Caller(0)
	gormLogger.Info(ctx, "gorm info %d", 1)
	p.Log(ctx, applog.LogEntry{Level: applog.LevelWarn, Message: "channel", Channel: "contract", Fields: []applog.Field{applog.Int("count", 9)}})
	large := make([]applog.Field, pooledFields+1)
	for i := range large {
		large[i] = applog.Int(fmt.Sprintf("field%d", i), i)
	}
	p.Log(ctx, applog.LogEntry{Level: applog.LevelInfo, Message: "large", Fields: large})
	p.Log(ctx, applog.LogEntry{Level: applog.LevelInfo, Message: "empty"})
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

func TestFieldBuffersReleaseAllReferences(t *testing.T) {
	p := new(Owner)
	buffer := new(fieldBuffer)
	for i := range buffer.fields {
		buffer.fields[i] = zap.Any("value", map[string]string{"retained": "request"})
	}
	p.releaseFields(buffer)
	for _, field := range buffer.fields {
		if field != (zap.Field{}) {
			t.Fatal("pooled buffer retains request data")
		}
	}
}

func TestPanicReleasesAdmittedWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, _ := newJSONOwner(t, "info", nil)
		recovered := false
		func() {
			defer func() { recovered = recover() != nil }()
			p.Log(panicValueContext{t.Context()}, applog.LogEntry{Level: applog.LevelInfo, Fields: []applog.Field{applog.String("value", "request")}})
		}()
		if !recovered {
			t.Fatal("panic fixture did not enter admitted write")
		}
		drained := make(chan struct{})
		var waiter sync.WaitGroup
		waiter.Go(func() { p.writes.Wait(); close(drained) })
		synctest.Wait()
		select {
		case <-drained:
		default:
			// Let the intentionally broken counterexample clean up its resources.
			p.writes.Done()
			waiter.Wait()
			t.Fatal("panic retained an admitted write")
		}
		waiter.Wait()
		if err := p.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestStaleSnapshotCannotWriteAfterClose(t *testing.T) {
	p, _ := newJSONOwner(t, "info", nil)
	snapshot := p.current.Load()
	if err := p.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !snapshot.closing.Load() {
		t.Fatal("old snapshot stayed open")
	}
	// Alert bypasses the advisory Enabled check and must still be rejected by
	// the authoritative gate before touching this panic context or the SDK.
	p.Log(panicValueContext{t.Context()}, applog.LogEntry{Level: applog.LevelInfo, Alert: true})
}

type replacementLogger struct{ calls atomic.Int32 }

func (r *replacementLogger) Log(context.Context, applog.LogEntry) { r.calls.Add(1) }

func TestClosingOwnerPreservesNewBinding(t *testing.T) {
	p, _ := newJSONOwner(t, "info", nil)
	block := &blockingStringer{entered: make(chan struct{}), release: make(chan struct{})}
	var work sync.WaitGroup
	work.Go(func() {
		p.Log(t.Context(), applog.LogEntry{Level: applog.LevelInfo, Fields: []applog.Field{applog.Any("blocked", block)}})
	})
	<-block.entered
	work.Go(func() {
		if err := p.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("p.Close(context.WithoutCancel(t.Context())): %v", err)
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for !p.current.Load().closing.Load() {
		if time.Now().After(deadline) {
			close(block.release)
			work.Wait()
			t.Fatal("owner never closed admission")
		}
		runtime.Gosched()
	}
	// Release is idempotent even if Stop's release overlaps this one.
	p.release()
	next := new(replacementLogger)
	release, err := applog.Install(next, nil)
	if err != nil {
		close(block.release)
		work.Wait()
		t.Fatal(err)
	}
	defer release()
	applog.Info("new owner before old drain")
	close(block.release)
	work.Wait()
	p.release()
	applog.Info("new owner after old drain")
	if next.calls.Load() != 2 || !p.current.Load().closing.Load() {
		t.Fatal("old owner removed new binding or reopened admission")
	}
}

func TestConcurrentLogLevelChangesAndStop(t *testing.T) {
	p, _ := newJSONOwner(t, "info", nil)
	start := make(chan struct{})
	var writers sync.WaitGroup
	for range 16 {
		writers.Go(func() {
			<-start
			for range 50 {
				p.Log(t.Context(), applog.LogEntry{Level: applog.LevelInfo, Message: "concurrent", Fields: []applog.Field{applog.Int("count", 1)}})
			}
		})
	}
	writers.Go(func() {
		<-start
		for range 50 {
			setSDKLevel(t, p.main, "error")
			setSDKLevel(t, p.main, "info")
		}
	})
	close(start)
	if err := p.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	writers.Wait()
	if !p.current.Load().closing.Load() {
		t.Fatal("admission stayed open after Stop")
	}
}

func TestInstallConflictPreservesExistingOwner(t *testing.T) {
	existing := new(replacementLogger)
	release, err := applog.Install(existing, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	factory := func(...logger.Option) (*logger.Logger, error) { return newConsoleLogger(), nil }
	p, err := Open(t.Context(), Options{Config: &config.Config{}, MainFactory: factory, AccessFactory: factory})
	if p != nil || !errors.Is(err, applog.ErrLoggerInstalled) {
		t.Fatalf("owner=%v conflicting open=%v", p, err)
	}
	applog.Info("existing owner still installed")
	if existing.calls.Load() != 1 {
		t.Fatal("failed Open removed existing owner")
	}
}
