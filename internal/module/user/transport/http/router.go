package http

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/middleware"
	userapp "github.com/gtkit/gin-api/internal/module/user/application"
)

// 本模块的业务超时预算。
//
// 显式声明而不是继承某个全局默认：不同模块的合理预算本就不同，
// 而"全局默认"会让所有路由跟着最慢的那条走。
const (
	userDeadline   = 5 * time.Second
	userWriteGrace = 2 * time.Second
)

// Deps 是注册路由所需的依赖。
//
// 用一个结构体而不是长参数列表：中间件会随功能增加，逐个加参数会让每次
// 新增都改动全部模块的签名。
type Deps struct {
	Service *userapp.Service
	// Auth 是认证中间件，由装配层构造后注入。
	//
	// 模块不自己构造它：认证依赖 token parser 与用户校验器，那些是进程级
	// 资源，让每个模块各建一份会产生多份缓存与多份配置。
	Auth gin.HandlerFunc
	// RateLimit 按策略标识取限流中间件，并按作用域分成认证前、认证后两组。
	// 首参是引用发生处的路由标识，装配失败时用于指认现场。
	//
	// 模块只声明受哪些策略约束，不决定每条挂在认证的哪一侧——那个位置由策略
	// 自身的作用域决定，推导逻辑收口在装配层的唯一一处。
	RateLimit func(route string, policyIDs ...string) (preAuth, postAuth []gin.HandlerFunc)
	// Cache 按 TTL 取路由级 GET 响应缓存中间件（cache-preset spec），由装配层注入。
	// ttl <= 0 取配置默认值；缓存关闭时注入的是透传中间件，模块无需判空。
	//
	// 只对**匿名可访问的只读路由**有意义：带身份的请求在中间件内一律绕过缓存，
	// 挂在认证路由上等于没挂。挂法（写在具体路由上，不 Group.Use 全组开启）：
	//
	//	g.GET("/hot", deps.Cache(0), handler.Hot)
	//
	// 本模块当前全部路由都要求身份，因此不挂；字段保留是装配契约——
	// 新增匿名只读路由时直接使用，不需要再改装配层。
	Cache func(ttl time.Duration) gin.HandlerFunc
}

// Register 在给定路由组上注册 user 模块的路由。
//
// 用法就是 gin 的用法：拿 *gin.RouterGroup，用 Group 挂中间件，
// 用 GET/PUT/POST 注册处理函数。
//
// **访问控制由代码位置表达**：下面的路由必须写在 g 里面。写到 g 外面就是
// 公开接口，而编译与测试都不会有任何异常——这是 gin 惯用写法的已知代价，
// 新增路由时需要人工确认。
func Register(r *gin.RouterGroup, deps Deps) {
	handler := NewHandler(deps.Service)

	g := r.Group("/user")
	g.Use(middleware.Timeout(userDeadline, userWriteGrace))

	// 默认限流：来源 + 身份两层。
	//
	// **两层都要**：只限身份时，一个来源可以用大量账号并发打满；
	// 只限来源时，就失去了按账号的精确限额。
	//
	// 两条策略挂在认证的哪一侧由它们各自的作用域决定，这里只声明受哪些策略约束。
	preAuth, postAuth := stageRateLimits(deps.RateLimit, "/user", "default.ip", "default.user")

	// 顺序是契约：来源限流挡的是尚未证明身份的流量，必须在认证之前；
	// 身份限流拿不到身份就退化成"每个请求都被拒"，必须在认证之后。
	g.Use(preAuth...)

	// 按请求体形态分子组，让**请求体治理排在认证之前**
	//（http-request-contract spec）。
	//
	// 必须分组而不是把 JSONBody 写在路由上：gin 的组中间件恒先于路由级中间件，
	// 所以"认证挂组上、body 治理挂路由上"的写法里，认证永远先跑——一个
	// Content-Type 不符的请求要先付掉令牌解析、用户查库与撤销查 Redis 才被 415 拒。
	//
	// 也**不**把认证下移到路由级来排这个顺序：那样认证从一处声明变成每条路由
	// 各一处，新增路由漏写即成公开接口，而该遗漏在编译与静态检查里都不显形。
	// 用一个每条路由都可能犯的错，换一次内容类型判定的开销，不成比例。
	read := g.Group("", middleware.NoBody())
	write := g.Group("", middleware.JSONBody(0))

	// 认证与身份限流对两个子组同款：新增路由落进哪个子组都自动继承。
	// 挂载点因此从一处变成两处，由 router_test.go 的 TestEveryRouteGoesThroughAuth
	// 逐条路由兜住——它是这套结构下访问控制的唯一自动化保障。
	for _, sub := range []*gin.RouterGroup{read, write} {
		sub.Use(deps.Auth)
		sub.Use(postAuth...)
	}

	read.GET("", handler.Get)
	write.PUT("", handler.Update)
	write.POST("/deactivate", handler.Deactivate)
}

// stageRateLimits 容错地调用装配层注入的分组函数。
//
// nil 容错只服务于单测手工构造 Deps 的场景；生产装配恒注入 stager，
// 引用解析不到时由装配层在路由注册完成后统一失败，不存在静默不限流的通道。
func stageRateLimits(
	stager func(string, ...string) ([]gin.HandlerFunc, []gin.HandlerFunc), route string, ids ...string,
) (preAuth, postAuth []gin.HandlerFunc) {
	if stager == nil {
		return nil, nil
	}
	return stager(route, ids...)
}
