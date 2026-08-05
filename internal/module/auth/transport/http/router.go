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
	// RateLimit 按策略 ID 取限流中间件；未配置该策略时返回 nil。
	RateLimit func(policyID string) gin.HandlerFunc
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
	useRateLimit(credential, deps.RateLimit, "auth.credential")
	credential.POST("/register", middleware.JSONBody(0), handler.Register)
	credential.POST("/login", middleware.JSONBody(0), handler.Login)

	// 令牌端点的额度可以宽一些：它们要求持有一个已签发的有效令牌，
	// 不构成口令猜测面。
	token := g.Group("")
	useRateLimit(token, deps.RateLimit, "auth.token")
	token.POST("/refresh", middleware.JSONBody(0), handler.Refresh)
	// 登出要求认证：要撤销的正是本次请求所用的令牌，
	// 而它的标识只有认证中间件能提供。
	token.POST("/logout", deps.Auth, middleware.JSONBody(0), handler.Logout)
}

// useRateLimit 按 ID 挂载限流中间件，跳过未配置的策略。
func useRateLimit(g *gin.RouterGroup, byID func(string) gin.HandlerFunc, ids ...string) {
	if byID == nil {
		return
	}
	for _, id := range ids {
		if mw := byID(id); mw != nil {
			g.Use(mw)
		}
	}
}
