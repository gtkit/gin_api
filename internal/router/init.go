package router

import (
	"strings"

	"github.com/gtkit/gin-api/internal/appmeta"
	middleware "github.com/gtkit/gin-api/internal/middleware"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
	"github.com/gtkit/gin-api/internal/platform/config"

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
}

// InitRouterWithModules 创建 Gin engine 并注册所有业务模块路由。
//
// 新增模块只需在 providers.go 中将 Module 实现追加到切片，
// 路由层无需任何改动。
func InitRouterWithModules(envName string, modules []Module) *gin.Engine {
	return InitRouterWithVersionedModules(envName, VersionedModules{V1: modules})
}

// InitRouterWithVersionedModules 创建支持多版本 API 分组的 Gin engine。
//
// 当前默认支持 v1 和 v2；如果某个版本没有模块，路由层不会注册该分组。
func InitRouterWithVersionedModules(envName string, modules VersionedModules, opts ...RouterOptions) *gin.Engine {
	var opt RouterOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

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

	// 注册全局处理函数，处理 405 和 404 错误
	engine.NoMethod(noMethod)
	engine.NoRoute(noRoute)

	// 初始化中间件
	middleware.InitMiddleware(engine, opt.AllowedOrigins, middleware.AccessLogOptions{
		LogRequestBody:  opt.LogRequestBody,
		LogResponseBody: opt.LogResponseBody,
	}, opt.MaxBodyBytes)

	// 注册模块分组
	registerModuleGroup(engine, appmeta.APIV1Path, modules.V1)
	registerModuleGroup(engine, appmeta.APIV2Path, modules.V2)

	return engine
}

func registerModuleGroup(engine *gin.Engine, path string, modules []Module) {
	if len(modules) == 0 {
		return
	}

	group := engine.Group(path)
	for _, m := range modules {
		m.RegisterRoutes(group)
	}
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

// 找不到路由.
func noRoute(c *gin.Context) {
	if strings.Contains(c.Request.URL.Path, ".") {
		return
	}
	resp.NotFoundError(c, "unknown route")
}

// 方法不允许.
func noMethod(c *gin.Context) {
	resp.NotAllowedMethod(c, "method not allowed")
}
