package router

import (
	"errors"
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/middleware"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

// newGlobalMiddleware 在最终遥测仪表就绪后一次构造 engine 使用的固定链。
func newGlobalMiddleware(opt Options) ([]gin.HandlerFunc, error) {
	if opt.Metrics == nil {
		return nil, errors.New("HTTP middleware requires final metrics recorder")
	}
	factory := opt.RequestIDFactory
	if factory == nil {
		factory = middleware.NewRequestIDGenerator
	}
	generator, err := factory()
	if err != nil {
		return nil, fmt.Errorf("create HTTP request ID generator: %w", err)
	}
	if generator == nil {
		return nil, errors.New("create HTTP request ID generator: nil generator")
	}
	chain := make([]gin.HandlerFunc, 0, 10)
	chain = append(chain, middleware.GinRecovery(true), middleware.RequestID(generator))
	if opt.TracingEnabled {
		chain = append(chain, otelgin.Middleware(opt.ServiceName))
	}
	chain = append(chain,
		middleware.GinLogger(middleware.AccessLogOptions{LogRequestBody: opt.LogRequestBody, LogResponseBody: opt.LogResponseBody}),
		middleware.Metrics(opt.Metrics),
		middleware.GinRecovery(true),
		middleware.SecurityHeaders(),
		middleware.ErrorHandler(),
	)
	if len(opt.AllowedOrigins) > 0 {
		chain = append(chain, middleware.Cors(opt.AllowedOrigins))
	}
	chain = append(chain, middleware.MaxBytesLimit(middleware.MaxBytesLimitConfig{MaxBytes: opt.MaxBodyBytes}))
	return chain, nil
}
