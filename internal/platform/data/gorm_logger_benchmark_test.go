package dao

import (
	"context"
	"fmt"
	"testing"
	"time"

	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/platform/config"
)

type benchmarkQuerySink struct{ enabled bool }

func (s benchmarkQuerySink) Enabled(applog.Level, applog.Stream) bool { return s.enabled }
func (benchmarkQuerySink) Log(context.Context, applog.LogEntry)       {}

func BenchmarkGormLogHotPath(b *testing.B) {
	for _, enabled := range []bool{false, true} {
		name := "filtered"
		if enabled {
			name = "enabled"
		}
		b.Run(name, func(b *testing.B) {
			release, err := applog.Install(benchmarkQuerySink{enabled}, nil)
			if err != nil {
				b.Fatal(err)
			}
			defer release()
			logger := NewGormLogger(&config.Config{Log: config.LogConfig{SQL: 1}})
			ctx := b.Context()
			fc := func() (string, int64) { return fmt.Sprintf("SELECT %s WHERE id = ?", "value"), 1 }
			b.ReportAllocs()
			for b.Loop() {
				logger.Trace(ctx, time.Now(), fc, nil)
			}
		})
	}
}
