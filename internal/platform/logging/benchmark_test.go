package logging

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/logger/v2"
	"go.uber.org/zap"
)

// BenchmarkLogHotPath measures native SDK encoding to /dev/null, not disk throughput.
func BenchmarkLogHotPath(b *testing.B) {
	fields := []zap.Field{zap.String("name", "worker"), zap.Int("count", 42), zap.Duration("elapsed", time.Millisecond), zap.Bool("ok", true), zap.Int64("rows", 7), zap.String("sql", "SELECT ?")}
	for _, name := range []string{"filtered", "enabled", "fields6", "access", "request_id"} {
		b.Run(name, func(b *testing.B) {
			owner := benchmarkOwner(b, "info")
			ctx := b.Context()
			if name == "request_id" {
				ctx = logger.ContextWithRequestID(ctx, "benchmark-request")
			}
			access := owner.AccessLogger()
			b.ReportAllocs()
			for b.Loop() {
				switch name {
				case "filtered":
					logger.Debugf("value %d", 42)
				case "enabled":
					logger.Info("event")
				case "access":
					access.InfoCtx(ctx, "/api", fields...)
				default:
					logger.InfoCtx(ctx, "event", fields...)
				}
			}
		})
	}
}

func benchmarkOwner(b *testing.B, level string) *Owner {
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
	return p
}
