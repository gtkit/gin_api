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
	// RateLimit 按策略 ID 取限流中间件；未配置该策略时返回 nil。
	RateLimit func(policyID string) gin.HandlerFunc
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
	useRateLimit(g, deps.RateLimit, "default.ip", "default.user")
	// 三条路由都要求已认证身份。
	g.Use(deps.Auth)

	g.GET("", handler.Get)
	g.PUT("", middleware.JSONBody(0), handler.Update)
	g.POST("/deactivate", middleware.JSONBody(0), handler.Deactivate)
}

// useRateLimit 按 ID 挂载限流中间件，跳过未配置的策略。
//
// 跳过而不是报错：限流策略集合由部署配置决定，一个只跑单机的开发环境
// 完全可以不配 redis 策略，那时这条路由不限流是预期行为。
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
