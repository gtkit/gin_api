package dao

import (
	"fmt"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
)

func BenchmarkGormLogHotPath(b *testing.B) {
	for _, enabled := range []bool{false, true} {
		name := "filtered"
		if enabled {
			name = "enabled"
		}
		b.Run(name, func(b *testing.B) {
			level := "fatal"
			if enabled {
				level = "info"
			}
			newNativeTestLogger(b, level)
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
