// Package router 装配 Gin engine：全局中间件、系统探针与各业务模块的路由。
package router

import (
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/middleware"
	"github.com/gtkit/gin-api/internal/pkg/metrics"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
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
	// SetMode 必须在 gin.New() **之前**：gin.New() 的第一件事就是按当时的 mode
	// 决定构造期调试输出，构造后再设置对那部分输出无效。
	gin.SetMode(ginModeFromEnv(envName))

	engine := gin.New()
	if err := configureEngine(engine, opt); err != nil {
		return nil, err
	}

	if err := useGlobalMiddleware(engine, opt); err != nil {
		return nil, err
	}

	// 未匹配路由与未匹配方法的处置。
	//
	// 注册时机不敏感：gin 的 NoRoute/NoMethod 与 Use 都会重建 404/405 处理链，
	// 两者的先后不影响"全局中间件对未匹配请求同样生效"这一结果。
	engine.NoRoute(noRoute)
	engine.NoMethod(noMethod)

	registerSystemRoutes(engine, opt)

	v1 := engine.Group("/api/v1")
	if opt.RegisterModules != nil {
		opt.RegisterModules(v1)
	}

	return engine, nil
}

// ginModeFromEnv 把运行环境映射到 gin 运行模式。
//
// 只处理生产环境是不够的：dev/test 会落在 gin 默认的 DebugMode，
// 而 DebugMode 下 gin 自己往 stderr 打路由表与警告，绕过项目的结构化日志。
func ginModeFromEnv(envName string) string {
	switch config.Env(strings.ToLower(strings.TrimSpace(envName))) {
	case config.EnvProd:
		return gin.ReleaseMode
	case config.EnvTest:
		return gin.TestMode
	case config.EnvDev:
		return gin.DebugMode
	default:
		return gin.DebugMode
	}
}

// configureEngine 是 engine 可配置面的**唯一**决策点。
//
// 这里逐项列出 gin.Engine 的全部可配置字段——包括决定"保持 gin 默认"的那些。
// 清单本身就是防遗漏机制：此前只有可信代理与运行模式被想到并写下，其余十余项
// 从未被提及，于是"漏了哪一项"无法通过阅读这段代码发现，而实际漏掉的四项
// （405 不可能产生、404 脱离统一信封、隐式尾斜杠重定向、*gin.Context 丢 deadline）
// 全都有真实后果。新增字段时在此追加一行定性，不要散落到别处。
//
// 各项定性（gin v1.12.0，默认值取自 gin.New()）：
//
//	RedirectTrailingSlash   true  → false：API 不做隐式重定向。非 GET 方法走 307，
//	                        客户端会带原请求体重发，一次写操作可能落到并非作者
//	                        本意的路径上；隐式纠正也让"路径不存在"不再确定。
//	RedirectFixedPath       false 保持：大小写不敏感的路径修正会与网关/WAF 的
//	                        路径规则分叉（网关按 /Admin 放行、应用按 /admin 匹配）。
//	HandleMethodNotAllowed  false → true：否则 405 永不产生，方法不匹配伪装成 404，
//	                        且与 CORS 预检回 405+Allow 的结论自相矛盾。
//	ForwardedByClientIP     true  保持：与 SetTrustedProxies 配对生效。
//	AppEngine               —     不触碰：已废弃字段。
//	UseRawPath              false 保持：用已解码的 URL.Path 匹配，与既有路由声明一致。
//	UseEscapedPath          false 保持：同上。
//	UnescapePathValues      true  保持：路径参数解码后交给 handler，是既有行为。
//	RemoveExtraSlash        false 保持：同 RedirectFixedPath，归一化会与上游规则分叉。
//	RemoteIPHeaders         配置驱动：opt 为空时保持 gin 默认。
//	TrustedPlatform         ""    保持：设置它等于无条件信任某云厂商的 IP 头，
//	                        本仓走显式可信代理列表。
//	MaxMultipartMemory      32MB  保持：仓库无 multipart 路由，全局 body 上限已封顶
//	                        该场景；为尚不存在的上传路由预设一个值没有依据。接入
//	                        上传时按该路由的实际上限单独定值。
//	UseH2C                  false 保持：h2c 是明文 HTTP/2，本仓服务 TLS 或置于反代之后。
//	ContextWithFallback     false → true：*gin.Context 自身满足 context.Context，
//	                        下游写 service.Do(c, …) 能通过编译，但默认实现会让
//	                        Deadline/Done/Err/Value 返回零值——路由级超时对这种
//	                        写法完全失效，且无任何编译期或运行期信号。
func configureEngine(engine *gin.Engine, opt Options) error {
	// 可信代理必须显式设置。Gin 不设置时的默认是**信任所有代理**，
	// 那会让任何客户端都能用转发头伪造自己的 IP，而限流与审计都按 IP 走。
	if err := engine.SetTrustedProxies(opt.TrustedProxies); err != nil {
		return err
	}
	if len(opt.RemoteIPHeaders) > 0 {
		engine.RemoteIPHeaders = opt.RemoteIPHeaders
	}

	engine.RedirectTrailingSlash = false
	engine.HandleMethodNotAllowed = true
	engine.ContextWithFallback = true

	return nil
}

// noRoute 处置未匹配的路由，走统一响应信封。
//
// 不按路径形态分流（例如让含点的路径落回框架默认响应）：那会产生两种 404 形态，
// 而合法的路径参数可以含点，那些请求会拿到与 API 不一致的响应体。
func noRoute(c *gin.Context) {
	resp.NotFoundError(c, "unknown route")
}

// noMethod 处置已注册路径上的未注册方法，走统一响应信封。
//
// Allow 头不在此计算：gin 在进入本处理器**之前**已按 RFC 7231 §6.5.5 写好，
// 这里再算一遍等于维护第二份同源事实。
func noMethod(c *gin.Context) {
	resp.NotAllowedMethod(c, "method not allowed")
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

// RateLimitStager 按策略标识取限流中间件，并**按作用域**分成认证前、认证后两组。
//
// 模块只声明"这条路由受哪些策略约束"，不决定每条挂在认证之前还是之后。
type RateLimitStager func(policyIDs ...string) (preAuth, postAuth []gin.HandlerFunc)

// NewRateLimitStager 构造分组函数。
//
// 阶段由 Policy.Scope 推导，模块不参与：不需要身份的作用域（IP/全局）必须排在
// 认证**之前**——它挡的是"还没证明自己是谁"的流量，排在认证之后等于让每个被限流
// 的请求先完成一次完整的令牌校验，而那正是攻击者想消耗的资源；需要身份的作用域
// 必须排在认证**之后**，否则取不到身份，中间件把"取不到主体"判为装配错误并拒绝，
// 于是该路由的每个请求都是 429。
//
// 为什么不让模块自己分组：那会多出一处可能与作用域矛盾的信息，而矛盾时以哪边
// 为准没有好答案。更糟的是这段推导若在每个模块各存一份，每份都能独立写错，
// 而代码生成器会把其中一份复制给所有新模块——一次写错因此会扩散。
//
// 找不到策略时跳过：策略集合由部署配置决定，一个只跑单机的开发环境完全可以不配。
// 未接限流注册表时返回的函数恒返回两个空切片，调用方无需判空。
func NewRateLimitStager(opt Options) RateLimitStager {
	if opt.RateLimits == nil || opt.RateLimitKeys == nil {
		return func(...string) (preAuth, postAuth []gin.HandlerFunc) { return nil, nil }
	}

	return func(policyIDs ...string) (preAuth, postAuth []gin.HandlerFunc) {
		for _, id := range policyIDs {
			limiter, ok := opt.RateLimits.Lookup(id)
			if !ok {
				continue
			}
			mw := middleware.RateLimit(limiter, opt.RateLimitKeys, opt.Metrics)
			if limiter.Policy().Scope == ratelimit.ScopeUser {
				postAuth = append(postAuth, mw)
				continue
			}
			preAuth = append(preAuth, mw)
		}
		return preAuth, postAuth
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
