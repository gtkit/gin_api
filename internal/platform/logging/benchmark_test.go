package logging

import (
	"context"
	"os"
	"testing"
	"time"

	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/logger/v2"
)

// BenchmarkLogHotPath uses the real SDK encoder/caller and a /dev/null console
// sink. It measures forwarding/encoding, not file rotation or disk throughput.
func BenchmarkLogHotPath(b *testing.B) {
	fields := []applog.Field{
		applog.String("name", "worker"), applog.Int("count", 42),
		applog.Duration("elapsed", time.Millisecond), applog.Any("ok", true),
		applog.Int64("rows", 7), applog.String("sql", "SELECT ?"),
	}
	large := make([]applog.Field, 16)
	for i := range large {
		large[i] = applog.Int("value", i)
	}
	for _, tc := range []struct {
		name      string
		threshold string
		entry     applog.LogEntry
		requestID bool
	}{
		{"filtered", "info", applog.LogEntry{Level: applog.LevelDebug, Message: "value %d", Format: true, Args: []any{42}, CallerSkip: 3}, false},
		{"filtered_fields", "info", applog.LogEntry{Level: applog.LevelDebug, Fields: fields, CallerSkip: 1}, false},
		{"enabled", "info", applog.LogEntry{Level: applog.LevelInfo, Message: "event", CallerSkip: 3}, false},
		{"field1", "info", applog.LogEntry{Level: applog.LevelInfo, Message: "event", Fields: fields[:1], CallerSkip: 3}, false},
		{"fields6", "info", applog.LogEntry{Level: applog.LevelInfo, Message: "event", Fields: fields, CallerSkip: 3}, false},
		{"fields16", "info", applog.LogEntry{Level: applog.LevelInfo, Message: "event", Fields: large, CallerSkip: 3}, false},
		{"gorm_caller1", "info", applog.LogEntry{Level: applog.LevelInfo, Message: "event", Fields: fields, CallerSkip: 1}, false},
		{"gorm_caller2", "info", applog.LogEntry{Level: applog.LevelInfo, Message: "event", Fields: fields, CallerSkip: 2}, false},
		{"access", "info", applog.LogEntry{Level: applog.LevelInfo, Message: "/api", Fields: fields, Stream: applog.AccessStream}, false},
		{"request_id", "info", applog.LogEntry{Level: applog.LevelInfo, Message: "event", Fields: fields, CallerSkip: 3}, true},
	} {
		for _, parallel := range []bool{false, true} {
			mode := "serial"
			if parallel {
				mode = "parallel"
			}
			b.Run(tc.name+"/"+mode, func(b *testing.B) {
				benchmarkOwner(b, tc.threshold)
				ctx := b.Context()
				if tc.requestID {
					ctx = applog.ContextWithRequestID(ctx, "benchmark-request")
				}
				b.ReportAllocs()
				if parallel {
					b.ResetTimer()
					b.RunParallel(func(pb *testing.PB) {
						for pb.Next() {
							applog.Log(ctx, tc.entry)
						}
					})
					return
				}
				for b.Loop() {
					applog.Log(ctx, tc.entry)
				}
			})
		}
	}
}

func benchmarkOwner(b *testing.B, level string) {
	b.Helper()
	output, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := output.Close(); err != nil {
			b.Errorf("output.Close(): %v", err)
		}
	})
	factory := func(...logger.Option) (*logger.Logger, error) {
		// Construction captures stdout; restore it before starting any benchmark
		// workers. No benchmark mutates stdout while workers are running.
		previous := os.Stdout
		os.Stdout = output
		defer func() { os.Stdout = previous }()
		return logger.New(logger.WithConsole(true), logger.WithFile(false), logger.WithLevel(level), logger.WithReplaceGlobals(false), logger.WithOutJSON(true))
	}
	p, err := Open(b.Context(), Options{Config: &config.Config{}, MainFactory: factory, AccessFactory: factory})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := p.Close(context.WithoutCancel(b.Context())); err != nil {
			b.Error(err)
		}
	})
}
