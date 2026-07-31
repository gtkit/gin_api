package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/goerr"

	"github.com/gin-contrib/timeout"

	resp "github.com/gtkit/gin-api/internal/pkg/response"
)

const (
	DefaultTimeout = 10 * time.Second
)

// TimeoutMiddleware 返回指定超时时长的中间件。
// 不传参数则使用 DefaultTimeout（10s）。
//
// 注意：不要用于 SSE / WebSocket 等长连接路由。
func TimeoutMiddleware(d ...time.Duration) gin.HandlerFunc {
	t := DefaultTimeout
	if len(d) > 0 && d[0] > 0 {
		t = d[0]
	}
	return timeout.New(
		timeout.WithTimeout(t),
		timeout.WithResponse(timeoutResponse),
	)
}

func timeoutResponse(c *gin.Context) {
	resp.Error(c, goerr.New(nil, goerr.StatusTimeout(), "http 请求超时"))
}
