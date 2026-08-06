package http

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/middleware"
	authapp "github.com/gtkit/gin-api/internal/module/auth/application"

	logger "github.com/gtkit/logger/v2"
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
	// RateLimit 按策略标识取限流中间件，并按作用域分成认证前、认证后两组。
	//
	// 模块只声明受哪些策略约束，位置由策略自身的作用域决定。
	RateLimit func(policyIDs ...string) (preAuth, postAuth []gin.HandlerFunc)
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

	// 凭据端点用**专用的严格限流**，不复用业务默认值。
	//
	// 这几条是全站唯一接受口令猜测的入口：与读接口共用一个额度，等于给
	// 爆破留出与正常浏览同样宽的通道。
	//
	// 只按来源限、不按身份限：请求发生在拿到身份**之前**，此时没有可用的
	// 用户维度——按身份限流对未登录的爆破者不起任何作用。
	credential := g.Group("")
	credential.Use(preIdentityRateLimits(deps.RateLimit, "auth.credential")...)
	credential.POST("/register", middleware.JSONBody(0), handler.Register)
	credential.POST("/login", middleware.JSONBody(0), handler.Login)

	// 令牌端点的额度可以宽一些：它们要求持有一个已签发的有效令牌，
	// 不构成口令猜测面。
	token := g.Group("")
	token.Use(preIdentityRateLimits(deps.RateLimit, "auth.token")...)
	token.POST("/refresh", middleware.JSONBody(0), handler.Refresh)
	// 登出要求认证：要撤销的正是本次请求所用的令牌，
	// 而它的标识只有认证中间件能提供。
	token.POST("/logout", deps.Auth, middleware.JSONBody(0), handler.Logout)
}

// preIdentityRateLimits 取认证前那一组限流中间件。
//
// 本模块的端点全部发生在拿到身份**之前**（登出除外，它的认证挂在路由级），
// 因此身份作用域的策略在这里无处可挂。真被配成身份作用域时不能静默丢弃——
// 那会让一条本以为受保护的凭据端点实际无限流，而配置里看起来是配了的。
// 因此显式告警并指出这几条端点只能按来源限流。
func preIdentityRateLimits(
	stager func(...string) ([]gin.HandlerFunc, []gin.HandlerFunc), ids ...string,
) []gin.HandlerFunc {
	if stager == nil {
		return nil
	}

	preAuth, postAuth := stager(ids...)
	if len(postAuth) > 0 {
		logger.Warnf("auth: %d identity-scoped rate limit policy(ies) in %v were not mounted: "+
			"these endpoints run before an identity exists, so only source-scoped policies apply",
			len(postAuth), ids)
	}
	return preAuth
}
