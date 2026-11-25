package middleware

import (
	"encoding/hex"
	"errors"
	"net/http"
	"syscall"

	resp "github.com/gtkit/gin-api/internal/pkg/response"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/goerr"
	"github.com/gtkit/logger/v2"
	"go.uber.org/zap"
)

const (
	// recoveryResponseCode 是对客户端暴露的统一错误码。
	// panic 是否带 stack、是否在日志里记录更多上下文，属于服务端实现细节，
	// 不应该改变客户端看到的 error code。
	recoveryResponseCode = int32(goerr.ErrInternalServer)

	// 下面这组 code 仅用于日志分类和运维排查，不直接返回给客户端。
	// 继续沿用项目已有的 100100xx 号段，避免再出现孤立的 102/103/104。
	recoveryLogCodePanicRecovered   = int32(10010016)
	recoveryLogCodeClientClosedConn = int32(10010017)
	recoveryLogCodeResponseStarted  = int32(10010018)
)

const recoveryPublicMessage = "服务器内部错误"

// GinRecovery records a bounded diagnostic and preserves any committed response.
func GinRecovery(includeStack bool) gin.HandlerFunc {
	stackLevel := zap.FatalLevel + 1
	if includeStack {
		stackLevel = zap.WarnLevel
	}
	stackOption := zap.AddStacktrace(stackLevel)

	return func(c *gin.Context) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}

			err, _ := recovered.(error)
			started := c.Writer != nil && c.Writer.Written()
			level, code := zap.ErrorLevel, recoveryLogCodePanicRecovered
			class, message := "panic", "[Recovery] panic recovered"
			switch {
			case isBrokenConnectionError(err):
				level, code = zap.WarnLevel, recoveryLogCodeClientClosedConn
				class, message = "client_disconnected", "[Recovery] client connection closed"
			case started:
				code = recoveryLogCodeResponseStarted
				class, message = "response_started", "[Recovery] panic recovered after response started"
			}

			fields := []zap.Field{
				zap.Int32("recovery_code", code),
				zap.String("error_class", class),
				zap.String("method", recoveryMethod(c.Request)),
				zap.String("route", c.FullPath()),
				zap.Bool("response_started", started),
			}
			id := logger.RequestIDFromContext(requestCtx(c))
			if len(id) == RequestIDLength {
				var decoded [RequestIDLength / 2]byte
				if _, err := hex.Decode(decoded[:], []byte(id)); err == nil {
					fields = append(fields, zap.String("request_id", id))
				}
			}
			logger.Default().Zap().WithOptions(stackOption).Log(level, message, fields...)

			if code == recoveryLogCodePanicRecovered {
				abortWithRecoveryResponse(c)
				return
			}
			c.Abort()
		}()

		c.Next()
	}
}

func recoveryMethod(r *http.Request) string {
	if r == nil {
		return ""
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace:
		return r.Method
	default:
		return ""
	}
}

func isBrokenConnectionError(err error) bool {
	return errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, http.ErrAbortHandler)
}

// abortWithRecoveryResponse 返回项目统一错误结构，避免 recovery 自己拼一套特殊 JSON。
func abortWithRecoveryResponse(c *gin.Context) {
	resp.WriteJSON(c, http.StatusInternalServerError, resp.NewResponse(recoveryResponseCode, recoveryPublicMessage))
}

// requestPath 安全提取当前请求路径，避免在异常场景下再因为空指针触发二次 panic。
func requestPath(c *gin.Context) string {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return ""
	}

	return c.Request.URL.Path
}
