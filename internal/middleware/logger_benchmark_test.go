package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func BenchmarkAccessLogHotPath(b *testing.B) {
	gin.SetMode(gin.TestMode)
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
			access, _ := newNativeTestLogger(b, level)
			engine := gin.New()
			engine.Use(GinLogger(access, AccessLogOptions{}))
			engine.GET("/benchmark", func(c *gin.Context) { c.Status(http.StatusNoContent) })
			request := httptest.NewRequestWithContext(b.Context(), http.MethodGet, "/benchmark?token=secret&name=worker", nil)
			b.ReportAllocs()
			for b.Loop() {
				engine.ServeHTTP(httptest.NewRecorder(), request)
			}
		})
	}
}
