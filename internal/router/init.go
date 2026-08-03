package router

import (
	"github.com/gtkit/gin-api/internal/appmeta"
	middleware "github.com/gtkit/gin-api/internal/middleware"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/router/routespec"

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
	// AuthMiddleware 实现 AccessAuthenticated。声明了该模式却没提供它，
	// 会在注册期直接失败——绝不把要求鉴权的路由当公开路由服务。
	AuthMiddleware gin.HandlerFunc
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
		{Source: "system", Prefix: "", Routes: systemRoutes(opt.HealthChecker)},
	}
	contributions = append(contributions, moduleContributions(appmeta.APIV1Path, modules.V1)...)
	contributions = append(contributions, moduleContributions(appmeta.APIV2Path, modules.V2)...)

	compiled, err := routespec.Compile(contributions)
	if err != nil {
		return nil, describeCompileError(err)
	}
	index := routespec.BuildIndex(compiled)

	access := accessMiddleware{authenticate: opt.AuthMiddleware}
	if err := validateAccessSupport(compiled, access); err != nil {
		return nil, describeCompileError(err)
	}

	// —— 自此才开始触碰生产 engine ——
	engine := gin.New()

	// 设置 Gin mode, 根据运行环境
	gin.SetMode(ginModeFromEnv(envName))

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

	// 初始化中间件
	middleware.InitMiddleware(engine, opt.AllowedOrigins, middleware.AccessLogOptions{
		LogRequestBody:  opt.LogRequestBody,
		LogResponseBody: opt.LogResponseBody,
	}, opt.MaxBodyBytes)

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
