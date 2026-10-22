package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	applog "github.com/gtkit/gin-api/internal/pkg/log"
)

type benchmarkAccessSink struct{ enabled bool }

func (s benchmarkAccessSink) Enabled(applog.Level, applog.Stream) bool { return s.enabled }
func (benchmarkAccessSink) Log(context.Context, applog.LogEntry)       {}

func BenchmarkAccessLogHotPath(b *testing.B) {
	gin.SetMode(gin.TestMode)
	for _, enabled := range []bool{false, true} {
		name := "filtered"
		if enabled {
			name = "enabled"
		}
		b.Run(name, func(b *testing.B) {
			release, err := applog.Install(benchmarkAccessSink{enabled}, nil)
			if err != nil {
				b.Fatal(err)
			}
			defer release()
			engine := gin.New()
			engine.Use(GinLogger(AccessLogOptions{}))
			engine.GET("/benchmark", func(c *gin.Context) { c.Status(http.StatusNoContent) })
			request := httptest.NewRequestWithContext(b.Context(), http.MethodGet, "/benchmark?token=secret&name=worker", nil)
			b.ReportAllocs()
			for b.Loop() {
				engine.ServeHTTP(httptest.NewRecorder(), request)
			}
		})
	}
}
