package http

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/middleware"
	authapp "github.com/gtkit/gin-api/internal/module/auth/application"
)

// 本模块的业务超时预算。
//
// 比 user 模块宽：密码哈希本身就要几百毫秒（那是它的设计目的），
// 再加上一次数据库往返与一次 Redis 往返。按读接口的预算裁，
// 正常的登录会在高负载下被自己的超时掐断。
const (
	authDeadline   = 10 * time.Second
	authWriteGrace = 2 * time.Second
)

// Deps 是注册路由所需的依赖。
type Deps struct {
	Service *authapp.Service
	// Auth 是认证中间件，只有登出用得到。
	Auth gin.HandlerFunc
	// RateLimit 按策略标识取限流中间件（pre-identity 形态）。
	// 首参是引用发生处的路由标识，装配失败时用于指认现场。
	//
	// 本模块的端点全部发生在拿到身份**之前**，身份作用域的策略在这里
	// 无处可挂——装配层注入的取用函数会把这类引用记为装配错误并让启动
	// 失败，而不是静默丢弃后让凭据端点裸奔。
	RateLimit func(route string, policyIDs ...string) []gin.HandlerFunc
}

// Register 在给定路由组上注册本地认证的路由。
//
// 只在 auth.mode 为 local 时调用。外部身份模式下这些端点不注册——
// 那种部署里令牌由外部签发，它们没有任何意义，留着只是多一组
// 无人看管的凭据入口。
func Register(r *gin.RouterGroup, deps Deps) {
	handler := NewHandler(deps.Service)

	g := r.Group("/auth")
	g.Use(middleware.Timeout(authDeadline, authWriteGrace))
	// 认证响应携带令牌，禁止任何缓存层保存（浏览器缓存/共享代理/CDN
	// 都会让令牌寿命脱离服务端控制）。
	g.Use(middleware.NoStore())

	// 凭据端点用**专用的严格限流**，不复用业务默认值。
	//
	// 这几条是全站唯一接受口令猜测的入口：与读接口共用一个额度，等于给
	// 爆破留出与正常浏览同样宽的通道。
	//
	// 只按来源限、不按身份限：请求发生在拿到身份**之前**，此时没有可用的
	// 用户维度——按身份限流对未登录的爆破者不起任何作用。
	credential := g.Group("")
	credential.Use(stageRateLimits(deps.RateLimit, "/auth", "auth.credential")...)
	credential.POST("/register", middleware.JSONBody(0), handler.Register)
	credential.POST("/login", middleware.JSONBody(0), handler.Login)

	// 令牌端点的额度可以宽一些：它们要求持有一个已签发的有效令牌，
	// 不构成口令猜测面。
	token := g.Group("")
	token.Use(stageRateLimits(deps.RateLimit, "/auth", "auth.token")...)
	token.POST("/refresh", middleware.JSONBody(0), handler.Refresh)
	// 登出要求认证：要撤销的正是本次请求所用的令牌，
	// 而它的标识只有认证中间件能提供。
	token.POST("/logout", deps.Auth, middleware.JSONBody(0), handler.Logout)
}

// stageRateLimits 容错地调用装配层注入的 pre-identity 取用函数。
//
// nil 容错只服务于单测手工构造 Deps 的场景；生产装配恒注入。
// 作用域不兼容（身份作用域策略挂到本模块端点）由取用函数记为装配错误、
// 在路由注册完成后统一让启动失败，本模块无需自行判定。
func stageRateLimits(
	stager func(string, ...string) []gin.HandlerFunc, route string, ids ...string,
) []gin.HandlerFunc {
	if stager == nil {
		return nil
	}
	return stager(route, ids...)
}
