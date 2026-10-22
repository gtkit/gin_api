package dao

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/platform/config"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type queryLogCapture struct{ entries []applog.LogEntry }

func TestGormDisabledBackendDoesNotMaterializeQuery(t *testing.T) {
	release, err := applog.Install(benchmarkQuerySink{enabled: false}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, tc := range []struct {
		name    string
		elapsed time.Duration
		err     error
	}{
		{"ordinary", 0, nil},
		{"slow", time.Second, nil},
		{"error", 0, errors.New("query failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			logger := NewGormLogger(&config.Config{Log: config.LogConfig{SQL: 1}})
			logger.Trace(t.Context(), time.Now().Add(-tc.elapsed), func() (string, int64) {
				calls++
				return "SELECT ?", 1
			}, tc.err)
			if calls != 0 {
				t.Fatalf("formatted suppressed SQL %d times", calls)
			}
		})
	}
}

func (c *queryLogCapture) Log(_ context.Context, e applog.LogEntry) { c.entries = append(c.entries, e) }
func TestGormLoggerUsesSelectedImplementation(t *testing.T) {
	sink := &queryLogCapture{}
	release, err := applog.Install(sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx := applog.ContextWithRequestID(t.Context(), "sql-request")
	for _, tc := range []struct {
		name      string
		level     gormlogger.LogLevel
		elapsed   time.Duration
		queryErr  error
		rows      int64
		message   string
		wantLevel applog.Level
	}{
		{"silent", gormlogger.Silent, time.Second, errors.New("hidden"), 1, "", 0},
		{"fast warn", gormlogger.Warn, 0, nil, 1, "", 0},
		{"not found", gormlogger.Warn, 0, gorm.ErrRecordNotFound, 0, "", 0},
		{"error", gormlogger.Warn, 0, errors.New("query failure"), -1, "gorm query error", applog.LevelError},
		{"slow", gormlogger.Warn, time.Second, nil, 2, "gorm slow query", applog.LevelWarn},
		{"slow not found", gormlogger.Warn, time.Second, gorm.ErrRecordNotFound, 0, "gorm slow query", applog.LevelWarn},
		{"verbose", gormlogger.Info, 0, nil, 1, "gorm query", applog.LevelInfo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink.entries = nil
			called := 0
			NewGormLogger(&config.Config{}).LogMode(tc.level).Trace(ctx, time.Now().Add(-tc.elapsed), func() (string, int64) { called++; return "SELECT ?", tc.rows }, tc.queryErr)
			if tc.message == "" {
				if called != 0 || len(sink.entries) != 0 {
					t.Fatal("formatted a suppressed query")
				}
				return
			}
			if called != 1 || len(sink.entries) != 1 {
				t.Fatalf("fc=%d events=%d", called, len(sink.entries))
			}
			event := sink.entries[0]
			if event.Message != tc.message || event.Level != tc.wantLevel || event.Alert {
				t.Fatalf("event=%+v", event)
			}
			fields := map[string]any{}
			for _, field := range event.Fields {
				fields[field.Key] = field.Value
			}
			source, ok := fields["source"].(string)
			if !ok {
				t.Fatalf("source has type %T", fields["source"])
			}
			if fields["sql"] != "SELECT ?" || fields["trace_id"] != "sql-request" || !strings.Contains(source, "gorm_logger_contract_test.go:") {
				t.Fatalf("fields=%v", fields)
			}
			_, hasRows := fields["rows"]
			if hasRows != (tc.rows >= 0) {
				t.Fatal("changed unknown row semantics")
			}
		})
	}
	verbose := NewGormLogger(&config.Config{Log: config.LogConfig{SQL: 1}})
	silent := verbose.LogMode(gormlogger.Silent)
	sink.entries = nil
	silent.Info(ctx, "hidden")
	verbose.Info(ctx, "info %d", 1)
	verbose.Warn(ctx, "warn %d", 2)
	verbose.Error(ctx, "error %d", 3)
	if len(sink.entries) != 3 || sink.entries[0].Text() != "info 1" || sink.entries[1].Text() != "warn 2" || sink.entries[2].Text() != "error 3" {
		t.Fatalf("printf forwarding=%+v", sink.entries)
	}
}

func TestSQLSlowThresholdIsAnIndependentValue(t *testing.T) {
	sink := &queryLogCapture{}
	release, err := applog.Install(sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, threshold := range []*int64{nil, new(int64(0)), new(int64(1500))} {
		cfg := &config.Config{Log: config.LogConfig{SQLSlowThresholdMS: threshold}}
		logger := NewGormLogger(cfg)
		if threshold != nil {
			*threshold = 1
		} // Changing the source pointer cannot change a built logger.
		sink.entries = nil
		calls := 0
		logger.Trace(t.Context(), time.Now().Add(-time.Second), func() (string, int64) { calls++; return "SELECT ?", 1 }, nil)
		wantSlow := threshold == nil
		if (len(sink.entries) == 1) != wantSlow || (calls == 1) != wantSlow {
			t.Fatalf("threshold snapshot failed: entries=%v calls=%d", sink.entries, calls)
		}
		silent := logger.LogMode(gormlogger.Silent)
		silent.Trace(t.Context(), time.Now(), func() (string, int64) { t.Error("silent evaluated SQL"); return "", 0 }, errors.New("hidden"))
		sink.entries = nil
		logger.Trace(t.Context(), time.Now(), func() (string, int64) { return "SELECT ?", 1 }, errors.New("visible error"))
		if len(sink.entries) != 1 || sink.entries[0].Level != applog.LevelError {
			t.Fatalf("error log disabled: %v", sink.entries)
		}
	}
}
