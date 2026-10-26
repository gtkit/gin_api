// Package router 装配 Gin engine：全局中间件、系统探针与各业务模块的路由。
package router

import (
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/middleware"
	"github.com/gtkit/logger/v2"

	"github.com/gtkit/gin-api/internal/pkg/metrics"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
	"github.com/gtkit/gin-api/internal/shared/env"
	"github.com/gtkit/gin-api/pkg/ratelimit"
)

// Options 是装配 engine 所需的进程级配置与依赖。
type Options struct {
	// AccessLogger is borrowed from the process log owner; the router never closes it.
	AccessLogger *logger.Logger
	// AllowedOrigins 为空时不挂载 CORS。
	AllowedOrigins []string
	// LogRequestBody 和 LogResponseBody 控制访问日志的正文采集。
	LogRequestBody, LogResponseBody bool
	// MaxBodyBytes 是整个请求链共享的正文上限。
	MaxBodyBytes int64
	// RequestIDFactory 可替换请求 ID 构造函数；nil 使用默认安全随机源。
	RequestIDFactory func() (*middleware.RequestIDGenerator, error)
	// TrustedProxies 是可信代理的 CIDR/IP 列表。**空 = 不信任任何代理**，
	// 此时 ClientIP 只取真实 TCP peer 地址，客户端伪造的转发头一律忽略。
	TrustedProxies []string
	// RemoteIPHeaders 是允许用于提取客户端 IP 的转发头，仅在请求来自可信代理时才被采信。
	//
	// **空 = 不采信任何转发头**（客户端 IP 取 TCP 连接对端），不回落框架默认值。
	//
	// 限定 TCP 是准确的：gin 对 unix socket 监听会把「可信」置真而对端 IP 保持 nil，
	// 此时留空会让 ClientIP() 返回字面量 "<nil>"。本仓只 net.Listen("tcp", …)，
	// 因此不可达；但母版的注释不该写成无条件断言。
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
	// Metrics 是在遥测 provider 就绪后创建的最终仪表。
	Metrics *metrics.Recorder
	// TracingEnabled 决定是否挂 OTel 的 HTTP 埋点中间件。
	//
	// 与 Metrics 的判空不同，这里必须显式开关：otelgin 即便在 no-op provider 下
	// 也会为每个请求建 span 对象并做属性拼装，那是白花的开销。
	TracingEnabled bool
	// ServiceName 是 trace 里的服务名，仅在 TracingEnabled 时使用。
	ServiceName string
	// RegisterModules 在 /api/v1 组上注册各业务模块的路由。
	//
	// 由 bootstrap 提供：模块的依赖（service、认证中间件）是进程级装配的
	// 产物，router 包不认识它们。
	RegisterModules func(v1 *gin.RouterGroup)
}

// New 创建 Gin engine 并装配全局中间件、系统探针与业务路由。
//
// 装配顺序就是 gin 的惯例：先 engine 级 Use，再分组注册。
func New(environment env.Env, opt Options) (*gin.Engine, error) {
	// SetMode 必须在 gin.New() **之前**：gin.New() 的第一件事就是按当时的 mode
	// 决定构造期调试输出，构造后再设置对那部分输出无效。
	gin.SetMode(ginModeFromEnv(environment))

	engine := gin.New()
	if err := configureEngine(engine, opt); err != nil {
		return nil, err
	}

	chain, err := newGlobalMiddleware(opt)
	if err != nil {
		return nil, fmt.Errorf("build global HTTP middleware: %w", err)
	}
	engine.Use(chain...)

	// 未匹配路由与未匹配方法的处置。
	//
	// 注册时机不敏感：gin 的 NoRoute/NoMethod 与 Use 都会重建 404/405 处理链，
	// 两者的先后不影响"全局中间件对未匹配请求同样生效"这一结果。
	engine.NoRoute(noRoute)
	engine.NoMethod(noMethod)

	registerSystemRoutes(engine, opt)

	v1 := engine.Group("/api/v1", middleware.Timeout(30*time.Second, 2*time.Second))
	if opt.RegisterModules != nil {
		opt.RegisterModules(v1)
	}

	return engine, nil
}

// ginModeFromEnv 把运行环境映射到 gin 运行模式。
//
// 环境值由配置层严格校验，此处不再归一化；未知值保留 Gin 的 DebugMode。
func ginModeFromEnv(environment env.Env) string {
	switch environment {
	case env.Prod:
		return gin.ReleaseMode
	case env.Test:
		return gin.TestMode
	case env.Dev:
		return gin.DebugMode
	default:
		return gin.DebugMode
	}
}

// configureEngine 集中配置可信代理、转发头、路由匹配与 context 传播。
// 禁用尾斜杠隐式重定向，启用 405，并让 *gin.Context 转发请求的取消与 deadline；
// 其余字段沿用 Gin 默认值，不做额外路径归一化。
func configureEngine(engine *gin.Engine, opt Options) error {
	// 可信代理必须显式设置。Gin 不设置时的默认是**信任所有代理**，
	// 那会让任何客户端都能用转发头伪造自己的 IP，而限流与审计都按 IP 走。
	if err := engine.SetTrustedProxies(opt.TrustedProxies); err != nil {
		return err
	}
	// 无条件赋值：nil 和空切片都表示只取连接对端，不能保留默认转发头。
	engine.RemoteIPHeaders = opt.RemoteIPHeaders

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

// RateLimitStager 按策略标识取限流中间件，并**按作用域**分成认证前、认证后两组。
// route 是引用发生处的路由标识（如 "/user"），装配失败时用它指认现场。
//
// 模块只声明"这条路由受哪些策略约束"，不决定每条挂在认证之前还是之后。
type RateLimitStager func(route string, policyIDs ...string) (preAuth, postAuth []gin.HandlerFunc)

// PreIdentityRateLimiter 供**不保证身份**的路由（凭据/令牌端点、公开接口）
// 取限流中间件。身份作用域的策略在这类路由上无处可挂——请求发生在拿到身份
// 之前，静默丢弃会让一条看起来配了限流的路由实际裸奔，因此该引用按装配
// 错误收集并让启动失败（rate-limiting spec：用户作用域的策略不得用于
// 不保证身份的路由）。
type PreIdentityRateLimiter func(route string, policyIDs ...string) []gin.HandlerFunc

// NewRateLimitStager 构造两种取用函数与装配错误的判定函数。
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
// 非法引用**不跳过**：解析不到的标识（含注册表缺失时的一切引用）、以及
// pre-identity 路由上的身份作用域策略，都记入收集器，由装配方在全部路由
// 注册完成后调用判定函数统一失败。任一情形若只告警，那条路由会完全没有
// 限流，而描述符里明明写着一个标识，配置漏项、拼写错误或作用域误配就此
// 静默失效。没有任何路由引用策略时，空注册表可正常启动——下游删掉限流
// 引用的项目不被强制配置策略。
//
// 收集器不加锁：取用函数只在 New 的单线程装配窗口内被各模块调用。
func NewRateLimitStager(opt Options) (RateLimitStager, PreIdentityRateLimiter, func() error) {
	var invalid []string // "route -> id [reason]"，保持引用出现顺序

	record := func(route, id, reason string) {
		invalid = append(invalid, route+" -> "+id+" ["+reason+"]")
	}
	resolve := func(route, id string) (ratelimit.Limiter, bool) {
		if opt.RateLimits == nil || opt.RateLimitKeys == nil {
			record(route, id, "not registered")
			return nil, false
		}
		limiter, ok := opt.RateLimits.Lookup(id)
		if !ok {
			record(route, id, "not registered")
			return nil, false
		}
		return limiter, true
	}

	stager := func(route string, policyIDs ...string) (preAuth, postAuth []gin.HandlerFunc) {
		for _, id := range policyIDs {
			limiter, ok := resolve(route, id)
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

	preIdentity := func(route string, policyIDs ...string) []gin.HandlerFunc {
		var mws []gin.HandlerFunc
		for _, id := range policyIDs {
			limiter, ok := resolve(route, id)
			if !ok {
				continue
			}
			if limiter.Policy().Scope == ratelimit.ScopeUser {
				record(route, id, "identity-scoped policy on a route that cannot guarantee an identity")
				continue
			}
			mws = append(mws, middleware.RateLimit(limiter, opt.RateLimitKeys, opt.Metrics))
		}
		return mws
	}

	verify := func() error {
		if len(invalid) == 0 {
			return nil
		}
		var registered []string
		if opt.RateLimits != nil {
			registered = opt.RateLimits.IDs()
		}
		return fmt.Errorf("invalid rate limit policy references (route -> policy [reason]): %s; "+
			"registered policies: %v; define the missing policies under ratelimit.policies "+
			"(see config/env.yml.example), fix the policy scope, or remove the references "+
			"from the route registrations",
			strings.Join(invalid, ", "), registered)
	}

	return stager, preIdentity, verify
}

// registerSystemRoutes 注册存活与就绪探针。
//
// 它们不在 /api/v1 下：探针是运维接口，与业务 API 的版本无关，
// 编排系统也不该因为业务版本升级而改探针地址。
func registerSystemRoutes(engine *gin.Engine, opt Options) {
	engine.GET("/health", healthHandler)
	engine.GET("/ready", readyHandler(opt.HealthChecker, opt.ProcessStatus, opt.RedisRequired))
}
