package router

import (
	"context"
	"fmt"

	"github.com/gtkit/gin-api/internal/appmeta"
	middleware "github.com/gtkit/gin-api/internal/middleware"
	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/router/routespec"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
	"github.com/gtkit/gin-api/pkg/ratelimit"

	"github.com/gin-gonic/gin"
)

type VersionedModules struct {
	V1 []Module
	V2 []Module
}

// RouterOptions 包含创建 Gin engine 时可选的额外配置。
type RouterOptions struct {
	AllowedOrigins []string
	// LogRequestBody / LogResponseBody 控制访问日志是否记录（脱敏后的）请求/响应体。
	// 默认 false，即不记录 body；由 bootstrap 从配置注入。
	LogRequestBody  bool
	LogResponseBody bool
	// MaxBodyBytes 全局请求体大小上限（字节）；<=0 时中间件兜底默认 10MB。
	MaxBodyBytes int64
	// TrustedProxies 是可信代理的 CIDR/IP 列表。**空 = 不信任任何代理**，
	// 此时 ClientIP 只取真实 TCP peer 地址，客户端伪造的转发头一律忽略。
	TrustedProxies []string
	// RemoteIPHeaders 是允许用于提取客户端 IP 的转发头，仅在请求来自可信代理时才被采信。
	RemoteIPHeaders []string
	// HealthChecker 供系统探针路由使用。为 nil 时 /ready 一律报未就绪——
	// 装配错误必须表现为"未就绪"，而不是让错配的实例被纳入流量。
	HealthChecker HealthChecker
	// ProcessStatus 提供进程自身的就绪信息（draining 相位、已降级/已失效的能力）。
	// 为 nil 时 readiness 只看依赖存活——那会让一个正在 draining 的实例
	// 或一个 required worker 已经死掉的实例继续报告 ready。
	ProcessStatus ProcessStatus
	// RedisRequired 声明 Redis 对本进程是否必需，与资源装配用的是同一份推导结果。
	// 它决定 Redis 故障映射到 503（required）还是 200+degraded（optional）。
	RedisRequired bool
	// AuthMiddleware 实现 AccessAuthenticated 与 AccessAuthorized 的认证段。
	// 声明了该模式却没提供它，会在注册期直接失败——绝不把要求鉴权的路由当公开路由服务。
	AuthMiddleware gin.HandlerFunc
	// OptionalAuthMiddleware 实现 AccessOptionalAuthenticated。
	OptionalAuthMiddleware gin.HandlerFunc
	// Authorizers 是具名授权实现的注册表，AccessAuthorized 按 ID 解析。
	// 注册表在 compile 前冻结，运行期不可变。
	Authorizers *sharedauth.AuthorizerRegistry
	// RateLimits 是限流策略注册表，RouteSpec.RateLimitPolicyIDs 按 ID 解析。
	//
	// **传 nil 不等于"不限流"**：业务路由硬引用默认策略，引用无法解析会在
	// 装配期失败。nil 只对完全不声明策略的路由集合成立（如部分单测）。
	// 为 nil 时所有路由不限流——这是母版的默认形态（没有配置任何策略）。
	RateLimits *ratelimit.Registry
	// RateLimitKeys 生成限流键。声明了策略却没有它即装配失败。
	RateLimitKeys *ratelimit.KeyBuilder

	// Metrics 记录请求量、时延与状态码；为 nil 时不记录。
	//
	// 允许为 nil 而不是要求必传：指标默认不接导出，测试与最小装配不该被迫
	// 构造一份仪表。这与限流不同——那里 nil 意味着"声明的策略静默失效"，
	// 是必须拦下的装配错误；这里 nil 只意味着"不记录"，没有任何东西被绕过。
	Metrics *metrics.Recorder
}

// InitRouterWithModules 创建 Gin engine 并注册所有业务模块路由。
func InitRouterWithModules(envName string, modules []Module, opts ...RouterOptions) (*gin.Engine, error) {
	return InitRouterWithVersionedModules(envName, VersionedModules{V1: modules}, opts...)
}

// InitRouterWithVersionedModules 创建支持多版本 API 分组的 Gin engine。
//
// 返回 error 而不是 panic：路由契约不合法是**装配错误**，应当让启动失败并给出
// 可定位的原因；panic 会崩在半配置好的 engine 上，且错误信息只有一个栈。
//
// 严格的两阶段：先把全部贡献编译并校验通过，再触碰生产 engine。
// 边注册边校验的话，Gin 在路径树冲突时会直接 panic，而此时 engine 已被写入一半。
func InitRouterWithVersionedModules(
	envName string, modules VersionedModules, opts ...RouterOptions,
) (*gin.Engine, error) {
	var opt RouterOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	contributions := []routespec.Contribution{
		{Source: "system", Prefix: "", Routes: systemRoutes(opt.HealthChecker, opt.ProcessStatus, opt.RedisRequired)},
	}
	contributions = append(contributions, moduleContributions(appmeta.APIV1Path, modules.V1)...)
	contributions = append(contributions, moduleContributions(appmeta.APIV2Path, modules.V2)...)

	compiled, err := routespec.Compile(contributions)
	if err != nil {
		return nil, describeCompileError(err)
	}
	index := routespec.BuildIndex(compiled)

	authorizers := opt.Authorizers
	if authorizers == nil {
		authorizers = sharedauth.NewAuthorizerRegistry()
	}
	// 冻结后运行期不可再注册或替换：路由集合与授权实现都是启动期事实。
	authorizers.Freeze()
	access := accessMiddleware{
		authenticate:         opt.AuthMiddleware,
		authenticateOptional: opt.OptionalAuthMiddleware,
		authorizers:          authorizers,
		rateLimits:           opt.RateLimits,
		rateLimitKeys:        opt.RateLimitKeys,
		metrics:              opt.Metrics,
		maxBodyBytes:         opt.MaxBodyBytes,
	}
	if err := validateAccessSupport(compiled, access); err != nil {
		return nil, describeCompileError(err)
	}
	if err := validateRateLimitSupport(compiled, opt); err != nil {
		return nil, describeCompileError(err)
	}

	// —— 自此才开始触碰生产 engine ——
	//
	// SetMode 必须在 gin.New() **之前**：New 会按当时的 mode 决定调试输出行为，
	// 之后再改等于让 engine 以一份和声明不符的模式被创建。
	gin.SetMode(ginModeFromEnv(envName))

	engine := gin.New()

	// 可信代理边界必须无条件设置，不能依赖 Gin 默认值。
	//
	// Gin 默认 ForwardedByClientIP=true 且可信代理集合为 0.0.0.0/0——即**信任所有代理**，
	// c.ClientIP() 会直接采信客户端完全可控的 X-Forwarded-For / X-Real-IP。
	// 于是访问日志的 ip 与业务审计的操作者 IP 都能被零成本伪造。
	//
	// "不调用 SetTrustedProxies" 与 "传空列表" 在 Gin 里语义完全不同：前者沿用不安全默认，
	// 后者才是"谁都不信任"。因此这里即使配置为空也显式调用。
	applyTrustedProxies(engine, opt)

	// 禁用重定向和方法不允许的处理
	engine.RedirectTrailingSlash = false
	engine.HandleMethodNotAllowed = true

	// 初始化中间件。失败即装配失败：request ID 生成器要读进程随机源，
	// 读不到就只能退回弱随机，而那会让跨实例的日志关联失效。
	if err := middleware.InitMiddleware(engine, opt.AllowedOrigins, middleware.AccessLogOptions{
		LogRequestBody:  opt.LogRequestBody,
		LogResponseBody: opt.LogResponseBody,
		// 预检需要知道目标路径上到底允许哪些方法：没有它，OPTIONS 会对不存在的
		// 路径回 204，而随后的实际请求拿到 404——客户端排查的方向因此是错的。
	}, opt.MaxBodyBytes, index.AllowHeaderValue, opt.Metrics); err != nil {
		return nil, fmt.Errorf("init global middleware: %w", err)
	}

	installFallbacks(engine, index)
	registerCompiledRoutes(engine, compiled, access)

	return engine, nil
}

// moduleContributions 把模块封装成带来源标识的贡献。
func moduleContributions(prefix string, modules []Module) []routespec.Contribution {
	out := make([]routespec.Contribution, 0, len(modules))
	for _, m := range modules {
		out = append(out, routespec.Contribution{
			Source: m.Name(),
			Prefix: prefix,
			Routes: m.Routes(),
		})
	}
	return out
}

// ginModeFromEnv 将运行环境映射为 gin mode。
// case 直接复用 config.Env 常量，与 Env.IsProd() 共享同一套合法环境定义；
// 不做大小写/空白归一化——config.Validate 已保证运行期只有三个精确值，
// default 分支只服务测试/空串场景，落 DebugMode。
func ginModeFromEnv(envName string) string {
	switch envName {
	case string(config.EnvProd):
		return gin.ReleaseMode
	case string(config.EnvTest):
		return gin.TestMode
	default:
		return gin.DebugMode
	}
}

// CompiledRoutesFor 编译给定模块集合并返回路由描述符，**不触碰任何 engine**。
//
// 供 OpenAPI 生成使用：契约必须来自与生产完全相同的编译路径。
// 让生成器自己遍历模块的话，它与 registrar 就成了两套来源——
// 而两套来源迟早分叉，分叉的表现是"文档里有的接口线上没有"或反过来。
func CompiledRoutesFor(modules VersionedModules) ([]routespec.CompiledRoute, error) {
	contributions := []routespec.Contribution{
		// 系统探针的 handler 在契约层无关紧要，但 compile 拒绝 nil handler，
		// 因此传一个空实现的 checker——描述符本身（方法/路径/策略）不受影响。
		{Source: "system", Prefix: "", Routes: systemRoutes(contractOnlyChecker{}, nil, false)},
	}
	contributions = append(contributions, moduleContributions(appmeta.APIV1Path, modules.V1)...)
	contributions = append(contributions, moduleContributions(appmeta.APIV2Path, modules.V2)...)

	compiled, err := routespec.Compile(contributions)
	if err != nil {
		return nil, describeCompileError(err)
	}
	return compiled, nil
}

// contractOnlyChecker 是契约生成用的空实现。
//
// 它只为满足"handler 非 nil"这条编译期约束而存在，永远不会被调用——
// 生成契约不需要执行任何探针。
type contractOnlyChecker struct{}

func (contractOnlyChecker) PingDB(context.Context) error    { return nil }
func (contractOnlyChecker) PingRedis(context.Context) error { return nil }
