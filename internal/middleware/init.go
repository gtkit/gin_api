package middleware

import (
	"fmt"

	"github.com/gin-gonic/gin"
)

// InitMiddleware 按**固定顺序**装配全局中间件。
//
// 顺序是契约，不是实现细节。每一处相对位置都有具体理由：
//
//	Recovery          最外层。先注册 = 最外层，其 defer recover() 才能兜住
//	                  后续所有中间件与业务 handler 的 panic。
//	RequestID         紧随其后。它之后的每一条日志、每一个错误响应都要带上 ID；
//	                  排在 Recovery 之后是因为生成器本身也可能 panic（不该逃出 Recovery），
//	                  排在其余全部之前是因为 panic 日志同样需要 ID。
//	AccessLogger      要覆盖到后续中间件产生的响应（413/403/404/405 都必须有一条访问记录），
//	                  因此必须排在它们之前。
//	SecurityHeaders   必须在任何可能写响应体的中间件之前：body 一开始写，
//	                  响应头就已发出，之后设置 header 不生效且**不会报错**。
//	ErrorHandler      统一把 c.Errors 转成响应格式。
//	CORS              先于 MaxBytesLimit：跨源预检必须拿到 CORS 头，
//	                  而预检请求没有 body，不该被体积限制影响。
//	MaxBytesLimit     最后，紧贴业务 handler 与 body 日志——这样即便开启 body 日志，
//	                  logger 的读取也被同一上限约束。
//
// 返回 error 而不是 panic：RequestID 生成器要读进程随机源，失败是一个
// 必须让启动失败的条件，而 panic 会绕过已建立资源的清理。
func InitMiddleware(
	r *gin.Engine, allowedOrigins []string, accessLog AccessLogOptions, maxBodyBytes int64,
) error {
	generator, err := NewRequestIDGenerator()
	if err != nil {
		return fmt.Errorf("init request id generator: %w", err)
	}
	requestID, err := RequestID(generator)
	if err != nil {
		return fmt.Errorf("init request id middleware: %w", err)
	}

	r.Use(GinRecovery(true))
	r.Use(requestID)
	r.Use(GinLogger(accessLog))
	r.Use(SecurityHeaders())
	r.Use(ErrorHandler())
	if len(allowedOrigins) > 0 {
		r.Use(Cors(allowedOrigins))
	}
	r.Use(MaxBytesLimit(MaxBytesLimitConfig{MaxBytes: maxBodyBytes}))

	return nil
}
