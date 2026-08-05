// Package router 装配 Gin engine：全局中间件、系统探针与各业务模块的路由。
package router

import (
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/middleware"
	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/pkg/ratelimit"
)

// Options 是装配 engine 所需的进程级配置与依赖。
type Options struct {
	AllowedOrigins []string
	// LogRequestBody / LogResponseBody 控制访问日志是否记录（脱敏后的）请求/响应体。
	LogRequestBody  bool
	LogResponseBody bool
	// MaxBodyBytes 是全局请求体上限（字节）；<=0 时中间件兜底默认 10MB。
	//
	// 它是**兜底**，覆盖 404/405 这类不进任何路由组的请求——那些路径同样
	// 会被灌大 body。路由级的 JSONBody 只表达"这条比全局更严"。
	MaxBodyBytes int64
	// TrustedProxies 是可信代理的 CIDR/IP 列表。**空 = 不信任任何代理**，
	// 此时 ClientIP 只取真实 TCP peer 地址，客户端伪造的转发头一律忽略。
	TrustedProxies []string
	// RemoteIPHeaders 是允许用于提取客户端 IP 的转发头，仅在请求来自可信代理时才被采信。
	RemoteIPHeaders []string
	// HealthChecker 供系统探针路由使用。为 nil 时 /ready 一律报未就绪——
	// 装配错误必须表现为"未就绪"，而不是让错配的实例被纳入流量。
	HealthChecker HealthChecker
	// ProcessStatus 提供进程自身的就绪信息（draining 相位、已降级的能力）。
	ProcessStatus ProcessStatus
	// RedisRequired 决定 Redis 故障映射到 503 还是 200+degraded。
	RedisRequired bool
	// RateLimits 是限流策略注册表；为 nil 时不限流。
	RateLimits *ratelimit.Registry
	// RateLimitKeys 生成限流键。有 RateLimits 就必须有它。
	RateLimitKeys *ratelimit.KeyBuilder
	// Metrics 记录请求量、时延与状态码；为 nil 时不记录。
	Metrics *metrics.Recorder
	// RegisterModules 在 /api/v1 组上注册各业务模块的路由。
	//
	// 由 bootstrap 提供：模块的依赖（service、认证中间件）是进程级装配的
	// 产物，router 包不认识它们。
	RegisterModules func(v1 *gin.RouterGroup)
}

// New 创建 Gin engine 并装配全局中间件、系统探针与业务路由。
//
// 装配顺序就是 gin 的惯例：先 engine 级 Use，再分组注册。
func New(envName string, opt Options) (*gin.Engine, error) {
	if config.Env(envName) == config.EnvProd {
		gin.SetMode(gin.ReleaseMode)
	}

	engine := gin.New()

	// 可信代理必须显式设置。Gin 不设置时的默认是**信任所有代理**，
	// 那会让任何客户端都能用转发头伪造自己的 IP，而限流与审计都按 IP 走。
	if err := engine.SetTrustedProxies(opt.TrustedProxies); err != nil {
		return nil, err
	}
	if len(opt.RemoteIPHeaders) > 0 {
		engine.RemoteIPHeaders = opt.RemoteIPHeaders
	}

	if err := useGlobalMiddleware(engine, opt); err != nil {
		return nil, err
	}

	registerSystemRoutes(engine, opt)

	v1 := engine.Group("/api/v1")
	if opt.RegisterModules != nil {
		opt.RegisterModules(v1)
	}

	return engine, nil
}

// useGlobalMiddleware 按**固定顺序**装配全局中间件。
//
// 顺序是契约，不是实现细节。每一处相对位置都有具体理由：
//
//	Recovery          最外层。先注册 = 最外层，其 defer recover() 才能兜住
//	                  后续所有中间件与业务 handler 的 panic。
//	RequestID         紧随其后。它之后的每一条日志、每一个错误响应都要带上 ID。
//	Logger            要覆盖到后续中间件产生的响应（413/403/404/405 都必须有
//	                  一条访问记录），因此必须排在它们之前。
//	Metrics           同理：它要统计的同样是全部请求，包括被后续中间件直接
//	                  拒掉的与未匹配路由的。
//	SecurityHeaders   必须在任何可能写响应体的中间件之前：body 一开始写，
//	                  响应头就已发出，之后设置 header 不生效且**不会报错**。
//	ErrorHandler      统一把 c.Errors 转成响应格式。
//	Cors              先于 MaxBytesLimit：跨源预检必须拿到 CORS 头，
//	                  而预检请求没有 body，不该被体积限制影响。
//	MaxBytesLimit     最后，紧贴业务路由——这样即便开启 body 日志，
//	                  logger 的读取也被同一上限约束。
func useGlobalMiddleware(engine *gin.Engine, opt Options) error {
	generator, err := middleware.NewRequestIDGenerator()
	if err != nil {
		return err
	}

	engine.Use(middleware.GinRecovery(true))
	engine.Use(middleware.RequestID(generator))
	engine.Use(middleware.GinLogger(middleware.AccessLogOptions{
		LogRequestBody:  opt.LogRequestBody,
		LogResponseBody: opt.LogResponseBody,
	}))
	engine.Use(middleware.Metrics(opt.Metrics))
	engine.Use(middleware.SecurityHeaders())
	engine.Use(middleware.ErrorHandler())
	if len(opt.AllowedOrigins) > 0 {
		engine.Use(middleware.Cors(opt.AllowedOrigins, allowedMethodsOf(engine)))
	}
	engine.Use(middleware.MaxBytesLimit(middleware.MaxBytesLimitConfig{MaxBytes: opt.MaxBodyBytes}))

	return nil
}

// allowedMethodsOf 返回一个查询函数：给定路径，回答它允许哪些方法。
//
// 预检必须回答"这个路径上这个方法真的能用吗"。无条件回 204 的后果是
// **预检成功、实际请求失败**：浏览器据此放行，随后拿到 404 或 405，
// 而开发者看到的现象是"CORS 明明通过了却还是失败"。
//
// 闭包在**请求期**读 engine.Routes()：路由此时已全部注册完毕，
// 而中间件是在注册之前挂上去的。
func allowedMethodsOf(engine *gin.Engine) middleware.AllowedMethodsFunc {
	return func(path string) string {
		var methods []string
		for _, r := range engine.Routes() {
			if r.Path == path {
				methods = append(methods, r.Method)
			}
		}
		return joinMethods(methods)
	}
}

// RateLimitByID 返回一个按策略 ID 取限流中间件的函数，供模块注册时使用。
//
// 找不到策略时返回 nil，调用方跳过——限流策略集合由部署配置决定，
// 一个只跑单机的开发环境完全可以不配某条策略。
func RateLimitByID(opt Options) func(string) gin.HandlerFunc {
	if opt.RateLimits == nil || opt.RateLimitKeys == nil {
		return nil
	}
	return func(id string) gin.HandlerFunc {
		limiter, ok := opt.RateLimits.Lookup(id)
		if !ok {
			return nil
		}
		return middleware.RateLimit(limiter, opt.RateLimitKeys, opt.Metrics)
	}
}

// joinMethods 把方法列表拼成 Allow 头的值，顺序稳定。
func joinMethods(methods []string) string {
	if len(methods) == 0 {
		return ""
	}
	slices.Sort(methods)
	return strings.Join(slices.Compact(methods), ", ")
}

// registerSystemRoutes 注册存活与就绪探针。
//
// 它们不在 /api/v1 下：探针是运维接口，与业务 API 的版本无关，
// 编排系统也不该因为业务版本升级而改探针地址。
func registerSystemRoutes(engine *gin.Engine, opt Options) {
	engine.GET("/health", healthHandler)
	engine.GET("/ready", readyHandler(opt.HealthChecker, opt.ProcessStatus, opt.RedisRequired))
}
