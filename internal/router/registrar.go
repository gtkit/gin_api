package router

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	resp "github.com/gtkit/gin-api/internal/pkg/response"
	"github.com/gtkit/gin-api/internal/router/routespec"
)

// registerCompiledRoutes 是全仓**唯一**允许向 engine 注册路由的地方。
//
// 它只使用 engine.Handle 一个入口。Gin 的 IRoutes 有十余个快捷方法
// （GET/POST/Any/Match/Static* 等），任何一个被业务代码直接调用，都会产生一条
// 绕过 RouteSpec 的路由——它没有访问策略、没有超时预算，也不会出现在契约测试里。
// 用"唯一入口 + 传感器白名单"约束，比逐个禁用快捷方法可靠：后者是黑名单，
// 框架加一个新方法就漏一个。
func registerCompiledRoutes(engine *gin.Engine, routes []routespec.CompiledRoute, mw accessMiddleware) {
	for _, r := range routes {
		engine.Handle(r.Spec.Method, r.FullPath, mw.chainFor(r.Spec)...)
	}
}

// accessMiddleware 持有把访问策略翻译成 handler 所需的中间件。
//
// 当前只接线 AccessAuthenticated——其余模式（Optional / Authorized）由访问策略批次
// 补齐。在此之前，描述符已经能表达它们，但 compile 阶段会拒绝没有对应实现的模式，
// 因此不存在"声明了却没生效"的窗口。
type accessMiddleware struct {
	authenticate gin.HandlerFunc
}

// chainFor 按访问策略构造该路由的 handler 链。
//
// 顺序是固定的：鉴权在前、业务 handler 在后。链的构造收敛在这里，
// 而不是让每个模块自己 Use——后者正是"新增路由忘了挂中间件就静默公开"的成因。
func (m accessMiddleware) chainFor(spec routespec.RouteSpec) []gin.HandlerFunc {
	if spec.AccessPolicy.Mode == routespec.AccessAuthenticated {
		return []gin.HandlerFunc{m.authenticate, spec.Handler}
	}
	return []gin.HandlerFunc{spec.Handler}
}

// validateAccessSupport 确认每条路由声明的访问模式都有对应实现。
//
// fail-closed 的关键一环：若某个模式还没接线就放行，声明了鉴权的路由会以公开状态
// 对外服务，而代码和描述符看上去都完全正确。宁可启动失败。
func validateAccessSupport(routes []routespec.CompiledRoute, mw accessMiddleware) error {
	var errs []error
	for _, r := range routes {
		switch r.Spec.AccessPolicy.Mode {
		case routespec.AccessPublic:
		case routespec.AccessAuthenticated:
			if mw.authenticate == nil {
				errs = append(errs, fmt.Errorf(
					"%s %s requires authentication but no authentication middleware was provided",
					r.Spec.Method, r.FullPath))
			}
		default:
			errs = append(errs, fmt.Errorf(
				"%s %s declares access mode %s, which is not wired yet; "+
					"refusing to serve it as if it were public",
				r.Spec.Method, r.FullPath, r.Spec.AccessPolicy.Mode))
		}
	}
	return errors.Join(errs...)
}

// installFallbacks 安装 404 / 405 兜底 handler。
//
// NoRoute / NoMethod 注册的是兜底处理函数而非路由，因此不进 RouteSpec 集合，
// 双向契约测试也把它们排除在外。
func installFallbacks(engine *gin.Engine, index *routespec.RouteIndex) {
	engine.NoRoute(notFoundHandler)
	engine.NoMethod(methodNotAllowedHandler(index))
}

// notFoundHandler 处理未知路径。
//
// 此前的实现在路径含 "." 时直接 return，既不写状态也不写响应体——
// 于是 /foo.json、/.well-known/x、/favicon.ico 这类请求拿到一个空响应，
// 而扫描流量正好可以伪装成静态资源逃避审计。静态资源将来只能经显式 RouteSpec 引入，
// 未知路径一律稳定返回 404。
func notFoundHandler(c *gin.Context) {
	resp.NotFoundError(c, "unknown route")
}

// methodNotAllowedHandler 处理路径存在但方法不匹配的请求。
//
// Allow 头由不可变 RouteIndex 重建：当前 Gin 版本在 HandleMethodNotAllowed 下确实会
// 自己写 Allow，但其顺序来自内部 method tree 的实现细节、不是本项目的稳定契约，
// 且不会包含由 CORS 派生的 OPTIONS。因此显式覆盖，保证顺序固定、内容完整。
func methodNotAllowedHandler(index *routespec.RouteIndex) gin.HandlerFunc {
	return func(c *gin.Context) {
		if allow := index.AllowHeaderValue(c.Request.URL.Path); allow != "" {
			c.Header("Allow", allow)
		}
		resp.NotAllowedMethod(c, "method not allowed")
	}
}

// systemRoutes 返回系统级路由的描述符。
//
// 健康探针此前由 RegisterHealthRoutes 直接 engine.GET 旁路注册，且发生在模块路由之后。
// 那同时破坏三件事：违反"唯一 registrar"、让"实际路由 ⇄ RouteSpec 双向集合相等"必然失败、
// 并且在服务器层写超时被取消后，探针将没有任何请求级写 deadline。
//
// 探针的超时预算刻意比业务路由短得多：探针的意义是快速回答"我还行吗"，
// 一个要等好几秒才失败的探针会让编排器的判定同样迟钝。
func systemRoutes(checker HealthChecker) []routespec.RouteSpec {
	probeTimeout := routespec.TimeoutPolicy{
		Mode:       routespec.TimeoutCooperative,
		Deadline:   healthProbeDeadline,
		WriteGrace: healthProbeWriteGrace,
	}
	return []routespec.RouteSpec{
		{
			Method:        http.MethodGet,
			Path:          "/health",
			Handler:       healthHandler,
			AccessPolicy:  routespec.AccessPolicy{Mode: routespec.AccessPublic},
			TimeoutPolicy: probeTimeout,
		},
		{
			Method:        http.MethodGet,
			Path:          "/ready",
			Handler:       readyHandler(checker),
			AccessPolicy:  routespec.AccessPolicy{Mode: routespec.AccessPublic},
			TimeoutPolicy: probeTimeout,
		},
	}
}

// describeCompileError 给编译失败补上足够定位的上下文。
func describeCompileError(err error) error {
	return fmt.Errorf("route compilation failed; no route was registered: %w", err)
}
