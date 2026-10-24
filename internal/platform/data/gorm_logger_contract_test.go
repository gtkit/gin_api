package data

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/logger/v2"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestGormDisabledBackendDoesNotMaterializeQuery(t *testing.T) {
	native, _ := newNativeTestLogger(t, "fatal")
	for _, tc := range []struct {
		name    string
		elapsed time.Duration
		err     error
	}{
		{"ordinary", 0, nil}, {"slow", time.Second, nil}, {"error", 0, errors.New("query failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			adapter := NewGormLogger(&config.Config{Log: config.LogConfig{SQL: 1}})
			fc := func() (string, int64) { calls++; return "SELECT ?", 1 }
			adapter.Trace(t.Context(), time.Now().Add(-tc.elapsed), fc, tc.err)
			if calls != 0 {
				t.Fatalf("formatted suppressed SQL %d times", calls)
			}
			if err := native.SetLevel("info"); err != nil {
				t.Fatal(err)
			}
			adapter.Trace(t.Context(), time.Now().Add(-tc.elapsed), fc, tc.err)
			if calls != 1 {
				t.Fatalf("dynamic level did not enable query: %d", calls)
			}
			if err := native.SetLevel("fatal"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGormLoggerUsesNativeOutput(t *testing.T) {
	for _, tc := range []struct {
		name               string
		level              gormlogger.LogLevel
		elapsed            time.Duration
		queryErr           error
		rows               int64
		message, wantLevel string
	}{
		{"silent", gormlogger.Silent, time.Second, errors.New("hidden"), 1, "", ""},
		{"fast warn", gormlogger.Warn, 0, nil, 1, "", ""},
		{"not found", gormlogger.Warn, 0, gorm.ErrRecordNotFound, 0, "", ""},
		{"error", gormlogger.Warn, 0, errors.New("query failure"), -1, "gorm query error", "ERROR"},
		{"slow", gormlogger.Warn, time.Second, nil, 2, "gorm slow query", "WARN"},
		{"slow not found", gormlogger.Warn, time.Second, gorm.ErrRecordNotFound, 0, "gorm slow query", "WARN"},
		{"verbose", gormlogger.Info, 0, nil, 1, "gorm query", "INFO"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, read := newNativeTestLogger(t, "debug")
			ctx := logger.ContextWithRequestID(t.Context(), "sql-request")
			called := 0
			NewGormLogger(&config.Config{}).LogMode(tc.level).Trace(ctx, time.Now().Add(-tc.elapsed), func() (string, int64) { called++; return "SELECT ?", tc.rows }, tc.queryErr)
			entries := read()
			if tc.message == "" {
				if called != 0 || len(entries) != 0 {
					t.Fatal("formatted a suppressed query")
				}
				return
			}
			if called != 1 || len(entries) != 1 {
				t.Fatalf("fc=%d events=%d", called, len(entries))
			}
			event := entries[0]
			if event["msg"] != tc.message || event["level"] != tc.wantLevel {
				t.Fatalf("event=%v", event)
			}
			source, ok := event["source"].(string)
			if !ok || !strings.Contains(source, "gorm_logger_contract_test.go:") || event["sql"] != "SELECT ?" || event["trace_id"] != "sql-request" || event["request_id"] != "sql-request" {
				t.Fatalf("fields=%v", event)
			}
			_, hasRows := event["rows"]
			if hasRows != (tc.rows >= 0) {
				t.Fatal("changed unknown row semantics")
			}
		})
	}
	_, read := newNativeTestLogger(t, "debug")
	verbose := NewGormLogger(&config.Config{Log: config.LogConfig{SQL: 1}})
	silent := verbose.LogMode(gormlogger.Silent)
	silent.Info(t.Context(), "hidden")
	verbose.Info(t.Context(), "info %d", 1)
	verbose.Warn(t.Context(), "warn %d", 2)
	verbose.Error(t.Context(), "error %d", 3)
	entries := read()
	if len(entries) != 3 || entries[0]["msg"] != "info 1" || entries[1]["msg"] != "warn 2" || entries[2]["msg"] != "error 3" {
		t.Fatalf("printf forwarding=%v", entries)
	}
}

func TestSQLSlowThresholdIsAnIndependentValue(t *testing.T) {
	for _, threshold := range []*int64{nil, new(int64(0)), new(int64(1500))} {
		_, read := newNativeTestLogger(t, "debug")
		cfg := &config.Config{Log: config.LogConfig{SQLSlowThresholdMS: threshold}}
		adapter := NewGormLogger(cfg)
		if threshold != nil {
			*threshold = 1
		}
		calls := 0
		adapter.Trace(t.Context(), time.Now().Add(-time.Second), func() (string, int64) { calls++; return "SELECT ?", 1 }, nil)
		wantSlow := threshold == nil
		entries := read()
		if (len(entries) == 1) != wantSlow || (calls == 1) != wantSlow {
			t.Fatalf("threshold snapshot: entries=%v calls=%d", entries, calls)
		}
		adapter.LogMode(gormlogger.Silent).Trace(t.Context(), time.Now(), func() (string, int64) { t.Error("silent evaluated SQL"); return "", 0 }, errors.New("hidden"))
		before := len(read())
		adapter.Trace(t.Context(), time.Now(), func() (string, int64) { return "SELECT ?", 1 }, errors.New("visible error"))
		entries = read()
		if len(entries) != before+1 || entries[before]["level"] != "ERROR" {
			t.Fatalf("error log disabled: %v", entries)
		}
	}
}
