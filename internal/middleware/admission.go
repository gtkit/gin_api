package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/pkg/metrics"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
)

// Admission bounds concurrent matched business requests without a waiting queue.
// A slot is released only when the handler returns, including panic unwinding.
func Admission(limit int, recorder *metrics.Recorder) gin.HandlerFunc {
	if limit <= 0 {
		panic("middleware: admission limit must be positive")
	}
	slots := make(chan struct{}, limit)
	return func(c *gin.Context) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
			c.Next()
		default:
			c.Abort()
			recorder.RecordAdmissionRejection(c.Request.Context(), c.Request.Method, requestScheme(c.Request), c.FullPath())
			resp.CapacityUnavailable(c)
		}
	}
}

// requestScheme uses the transport seen by this server, never forwarding headers.
func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}
