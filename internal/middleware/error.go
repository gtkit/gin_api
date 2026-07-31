package middleware

import (
	"context"
	"errors"
	"net/http"

	resp "github.com/gtkit/gin-api/internal/pkg/response"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/goerr"
	logger "github.com/gtkit/logger/v2"
	"go.uber.org/zap"
)

// ErrorHandler captures errors and returns a consistent JSON error response.
func ErrorHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Step 1: 先让后续 handler / middleware 执行。
		// ErrorHandler 作为兜底层，必须在请求处理结束后再统一检查上下文中的错误。
		c.Next()

		// Step 2: 如果当前请求链路没有收集到错误，就直接结束。
		if len(c.Errors) == 0 {
			return
		}

		// Step 3: 使用最后一个错误作为最终响应依据。
		// Gin 的 c.Errors 是一个错误列表，最后一个错误通常最接近最终失败原因。
		err := c.Errors.Last().Err

		// Step 4: 如果响应已经写出，只记日志不回包。
		// 这类场景常见于流式响应、文件下载、或前面的 handler 已经通过 resp.Err 完成回包。
		if c.Writer != nil && c.Writer.Written() {
			logContextError(c, err)
			return
		}

		// Step 5: 响应未写出，走统一响应（resp.Err 内部会记日志），这里不再重复记录。
		if appErr, ok := errors.AsType[goerr.Error](err); ok {
			resp.Err(c, appErr)
			return
		}
		resp.Err(c, err)
	}
}

// logContextError 统一记录 ErrorHandler 捕获到的错误。
// 对于 5xx 错误使用 error 级别，4xx / 业务错误使用 warn，便于告警时做级别区分。
func logContextError(c *gin.Context, err error) {
	fields := []zap.Field{
		zap.String("path", requestPath(c)),
		zap.Error(err),
	}

	// Step 1: 对请求体超限这种明确的客户端错误单独降级为 warn，
	// 避免它们被误判成服务故障噪音。超限统一以标准库错误类型表示
	// （http.MaxBytesReader 超限时 Read 返回 *http.MaxBytesError）。
	if maxBytesErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
		fields = append(fields,
			zap.Int("http_status", http.StatusRequestEntityTooLarge),
			zap.Int32("error_code", resp.PayloadTooLargeCode),
			zap.Int64("max_bytes", maxBytesErr.Limit),
		)
		logger.WarnCtx(requestCtx(c), "[ErrorHandler] request failed", fields...)
		return
	}

	// Step 2: 如果错误本身带有 goerr 语义，就按它的 HTTP 状态码区分日志级别。
	var appErr goerr.Error
	if errors.As(err, &appErr) {
		fields = append(fields,
			zap.Int("http_status", appErr.HTTPCode()),
			zap.Int("error_code", int(appErr.Code())),
		)
		if isServerSideAppError(appErr) {
			logger.ErrorCtx(requestCtx(c), "[ErrorHandler] request failed", fields...)
			return
		}

		logger.WarnCtx(requestCtx(c), "[ErrorHandler] request failed", fields...)
		return
	}

	// Step 3: 其余未知错误默认按服务端错误记录。
	logger.ErrorCtx(requestCtx(c), "[ErrorHandler] request failed", fields...)
}

func isServerSideAppError(err goerr.Error) bool {
	if err == nil {
		return true
	}
	if err.HTTPCode() >= http.StatusInternalServerError {
		return true
	}

	code := err.Code()
	if code == goerr.ErrInternalServer {
		return true
	}

	return code >= goerr.Code(10010100) && code <= goerr.Code(10010699)
}

// requestCtx 取请求 context（含 requestId）供日志 *Ctx 方法提取；请求缺失时退回 Background。
func requestCtx(c *gin.Context) context.Context {
	if c != nil && c.Request != nil {
		return c.Request.Context()
	}
	return context.Background()
}
