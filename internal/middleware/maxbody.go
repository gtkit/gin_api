package middleware

import (
	"net/http"

	resp "github.com/gtkit/gin-api/internal/pkg/response"

	"github.com/gin-gonic/gin"
)

// MaxBytesLimitConfig 中间件配置.
type MaxBytesLimitConfig struct {
	// MaxBytes 最大允许的请求体大小（字节）
	MaxBytes int64
	// ErrorMessage 超出限制时的错误消息
	ErrorMessage string
	// CloseConnection 达到限制后是否关闭连接
	CloseConnection bool
}

// DefaultMaxBytesLimitConfig 默认配置.
var DefaultMaxBytesLimitConfig = MaxBytesLimitConfig{
	MaxBytes:        10 << 20, // 10MB
	ErrorMessage:    "请求体过大",
	CloseConnection: true,
}

// MaxBytesLimit 创建限制请求体大小的中间件。
//
// 超限的三个暴露点与处理方式：
//  1. 声明了 Content-Length 且超限：本中间件在进入 handler 前直接 413；
//  2. GinLogger 开启请求体日志时 io.ReadAll 撞限：logger 侧识别 *http.MaxBytesError → 413；
//  3. handler 内 ShouldBind 系列撞限（含 chunked 请求）：resp.BindStructErr/BindErr
//     识别 *http.MaxBytesError → 413。
//
// http.MaxBytesReader 超限时从 Read 返回 *http.MaxBytesError（从不 panic），
// 故这里不做 recover，也不在 handler 之后补检——那时响应可能已写出。
func MaxBytesLimit(config MaxBytesLimitConfig) gin.HandlerFunc {
	// 使用默认值填充缺失的配置
	if config.MaxBytes <= 0 {
		config.MaxBytes = DefaultMaxBytesLimitConfig.MaxBytes
	}
	if config.ErrorMessage == "" {
		config.ErrorMessage = DefaultMaxBytesLimitConfig.ErrorMessage
	}

	return func(c *gin.Context) {
		// 对于没有请求体的方法，直接跳过
		if c.Request.Body == nil || c.Request.Body == http.NoBody {
			c.Next()

			return
		}

		// 声明长度预检：超限请求在进入 handler 前拒绝，handler 不执行。
		// chunked 请求（ContentLength=-1）不经此路径，由读取时的 MaxBytesReader 兜底。
		if c.Request.ContentLength > config.MaxBytes {
			if config.CloseConnection {
				c.Header("Connection", "close")
			}
			resp.PayloadTooLargeErr(c, "文件太大，最大支持 "+resp.FormatBytes(config.MaxBytes), config.MaxBytes)

			return
		}

		// 真实字节上限：读取超过 MaxBytes 时 Read 返回 *http.MaxBytesError，
		// 由 logger / 绑定路径映射为 413。
		// MaxBytesReader.Close 已经关闭底层 body，无需再次包装并重复关闭。
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, config.MaxBytes)

		c.Next()
	}
}
