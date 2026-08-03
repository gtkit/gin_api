package router

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/goerr"

	"github.com/gtkit/gin-api/internal/middleware"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
	"github.com/gtkit/gin-api/internal/ratelimit"
	"github.com/gtkit/gin-api/internal/router/routespec"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
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
	// authenticate 实现 AccessAuthenticated / AccessAuthorized 的认证段。
	authenticate gin.HandlerFunc
	// authenticateOptional 实现 AccessOptionalAuthenticated：缺 token 放行，
	// 但带了非法 token 仍须拒绝。
	authenticateOptional gin.HandlerFunc
	// authorizers 是具名授权实现，AccessAuthorized 按 ID 解析。
	authorizers *sharedauth.AuthorizerRegistry
	// rateLimits 是限流策略注册表，RouteSpec.RateLimitPolicyID 按 ID 解析。
	rateLimits *ratelimit.Registry
	// rateLimitKeys 生成限流键（主体标识经 HMAC）。
	rateLimitKeys *ratelimit.KeyBuilder
}

// chainFor 按路由策略构造该路由的 handler 链。
//
// 顺序固定为 **超时 → IP/全局限流 → 认证 → 授权 → user 限流 → 业务 handler**。
//
// 每处位置都有理由：
//
//	超时最外层        要覆盖鉴权本身。鉴权链会查库查缓存，那些调用同样会挂住；
//	                  排在鉴权之后的话，一次数据库僵死会让请求卡在鉴权阶段
//	                  而不受任何预算约束。
//	IP/全局限流靠前    它挡的是"还没证明自己是谁"的流量。放在鉴权之后，
//	                  每个被限流的请求都要先做一次完整的令牌校验——
//	                  而那正是攻击者想消耗的资源。
//	user 限流在鉴权后  它需要已认证身份；排在鉴权之前拿不到 userID。
//
// 链的构造收敛在这里，而不是让每个模块自己 Use——后者正是
// "新增路由忘了挂中间件就静默公开"的成因。
func (m accessMiddleware) chainFor(spec routespec.RouteSpec) []gin.HandlerFunc {
	chain := make([]gin.HandlerFunc, 0, 6)

	timeoutMW, err := middleware.RouteTimeout(spec.TimeoutPolicy)
	if err != nil {
		// 走不到这里：compile 已拒绝非法策略。万一走到，唯一安全的行为是
		// 拒绝该路由而不是让它以"无超时"运行——那正是本批次要消灭的形态。
		return []gin.HandlerFunc{denyEverything}
	}
	chain = append(chain, timeoutMW)

	// 鉴权**之前**的限流：IP 与全局作用域不需要身份。
	if mw, ok := m.rateLimitFor(spec, false); ok {
		chain = append(chain, mw)
	}

	switch spec.AccessPolicy.Mode {
	case routespec.AccessAuthenticated:
		chain = append(chain, m.authenticate)
	case routespec.AccessOptionalAuthenticated:
		chain = append(chain, m.authenticateOptional)
	case routespec.AccessAuthorized:
		authorizer, _ := m.authorizers.Lookup(spec.AccessPolicy.AuthorizerID)
		authorize, authErr := middleware.Authorize(authorizer, spec.AccessPolicy.AuthorizerID)
		if authErr != nil {
			// 走不到这里：validateAccessSupport 已在 compile 阶段确认过可解析。
			// 万一走到，唯一安全的行为是拒绝一切，绝不放行。
			authorize = denyEverything
		}
		chain = append(chain, m.authenticate, authorize)
	case routespec.AccessPublic, routespec.AccessUnspecified:
		// 公开路由不加鉴权段；未指定已在 compile 阶段拒绝。
	}

	// 鉴权**之后**的限流：user 作用域需要已认证身份。
	if mw, ok := m.rateLimitFor(spec, true); ok {
		chain = append(chain, mw)
	}

	return append(chain, spec.Handler)
}

// rateLimitFor 返回该路由在指定阶段的限流中间件。
//
// afterAuth 决定取哪一段：user 作用域必须在鉴权之后（否则拿不到身份），
// IP/global 必须在鉴权之前（否则每个被限流的请求都要先付出一次完整的
// 令牌校验，而那正是攻击者想消耗的资源）。
func (m accessMiddleware) rateLimitFor(spec routespec.RouteSpec, afterAuth bool) (gin.HandlerFunc, bool) {
	if spec.RateLimitPolicyID == "" || m.rateLimits == nil || m.rateLimitKeys == nil {
		return nil, false
	}

	limiter, ok := m.rateLimits.Lookup(spec.RateLimitPolicyID)
	if !ok {
		// 走不到这里：ValidateReferences 已在 compile 之后确认过可解析。
		// 万一走到，拒绝而不是静默放行——后者等于这条路由完全没有限流。
		if afterAuth {
			return denyEverything, true
		}
		return nil, false
	}

	isUserScope := limiter.Policy().Scope == routespec.ScopeUser
	if isUserScope != afterAuth {
		return nil, false
	}

	mw, err := middleware.RateLimit(limiter, m.rateLimitKeys)
	if err != nil {
		return denyEverything, true
	}
	return mw, true
}

// denyEverything 是装配异常时的兜底 handler：拒绝而不是放行。
func denyEverything(c *gin.Context) {
	resp.Error(c, goerr.New(nil, goerr.StatusPermission(), "authorization is not available"))
	c.Abort()
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
		case routespec.AccessOptionalAuthenticated:
			if mw.authenticateOptional == nil {
				errs = append(errs, fmt.Errorf(
					"%s %s allows optional authentication but no optional authentication middleware was provided",
					r.Spec.Method, r.FullPath))
			}
		case routespec.AccessAuthorized:
			if mw.authenticate == nil {
				errs = append(errs, fmt.Errorf(
					"%s %s requires authentication before authorization but none was provided",
					r.Spec.Method, r.FullPath))
			}
			// 引用了不存在的 authorizer 必须在启动期发现：
			// 留到请求期才发现时，该路由的安全行为是未定义的。
			if _, ok := mw.authorizers.Lookup(r.Spec.AccessPolicy.AuthorizerID); !ok {
				errs = append(errs, fmt.Errorf(
					"%s %s references authorizer %q, which is not registered (registered: %v)",
					r.Spec.Method, r.FullPath, r.Spec.AccessPolicy.AuthorizerID, mw.authorizers.IDs()))
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
func systemRoutes(checker HealthChecker, status ProcessStatus, redisRequired bool) []routespec.RouteSpec {
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
			OperationID:   "getHealth",
			Tags:          []string{"Health"},
			Summary:       "Liveness probe",
			// 探针不走统一信封：它的形状是编排器的契约。
			ResponseShape: routespec.ResponseShapeRaw,
			ResponseBody:  LivenessResponse{},
		},
		{
			Method:        http.MethodGet,
			Path:          "/ready",
			Handler:       readyHandler(checker, status, redisRequired),
			AccessPolicy:  routespec.AccessPolicy{Mode: routespec.AccessPublic},
			TimeoutPolicy: probeTimeout,
			OperationID:   "getReadiness",
			Tags:          []string{"Health"},
			Summary:       "Readiness probe",
			Responses:     map[string]string{"503": "Not ready to receive traffic"},
			ResponseShape: routespec.ResponseShapeRaw,
			ResponseBody:  ReadinessResponse{},
		},
	}
}

// describeCompileError 给编译失败补上足够定位的上下文。
func describeCompileError(err error) error {
	return fmt.Errorf("route compilation failed; no route was registered: %w", err)
}

// validateRateLimitSupport 校验路由引用的限流策略在装配期可解析。
//
// 引用一个未登记的策略若不拦住，那条路由会**完全没有限流**——
// 而 RouteSpec 里明明写了一个 ID，代码审查时看起来一切正常。
// 这与"声明了鉴权却没提供实现"是同一类错误，处置也相同：启动失败。
func validateRateLimitSupport(routes []routespec.CompiledRoute, opt RouterOptions) error {
	var referenced bool
	for _, route := range routes {
		if route.Spec.RateLimitPolicyID != "" {
			referenced = true
			break
		}
	}
	if !referenced {
		return nil
	}

	if opt.RateLimits == nil {
		return errors.New(
			"routes declare rate limit policies but no policy registry was provided: " +
				"those routes would run with no rate limiting at all")
	}
	if opt.RateLimitKeys == nil {
		return errors.New(
			"routes declare rate limit policies but no key builder was provided: " +
				"limiter keys must derive subject identifiers through a keyed HMAC")
	}

	return opt.RateLimits.ValidateReferences(routes)
}
