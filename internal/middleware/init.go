package middleware

import (
	"github.com/gin-contrib/requestid"
	"github.com/gin-gonic/gin"
	logger "github.com/gtkit/logger/v2"
)

func InitMiddleware(r *gin.Engine, allowedOrigins []string, accessLog AccessLogOptions, maxBodyBytes int64) {
	// Recovery 必须置于最外层：先注册=最外层，其 defer recover() 才能兜住
	// 后续所有中间件（ErrorHandler/Cors/requestid/GinLogger）及业务 handler 的 panic。
	r.Use(GinRecovery(true))
	r.Use(ErrorHandler())
	if len(allowedOrigins) > 0 {
		r.Use(Cors(allowedOrigins))
	}
	r.Use(requestid.New())
	// 把 requestId 写入 request context：主 logger 的 *Ctx 方法与 GORM zlogger 的
	// TraceIDExtractor 从同一键提取，使业务错误日志、SQL 日志与访问日志可按同一 id 串联。
	r.Use(func(c *gin.Context) {
		if id := requestid.Get(c); id != "" {
			c.Request = c.Request.WithContext(logger.ContextWithRequestID(c.Request.Context(), id))
		}
		c.Next()
	})
	// 请求体大小上限须在 GinLogger 之前注册：这样即便开启 body 日志、
	// logger 的 io.ReadAll 也被 MaxBytesReader 同一上限约束，不会读入超大 body。
	r.Use(MaxBytesLimit(MaxBytesLimitConfig{MaxBytes: maxBodyBytes}))
	r.Use(GinLogger(accessLog))
}
