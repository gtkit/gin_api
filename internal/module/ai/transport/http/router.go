package http

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/middleware"
	aiapp "github.com/gtkit/gin-api/internal/module/ai/application"
)

// 本模块**非流式**路由的业务超时预算。
//
// 显式声明而不是继承某个全局默认：不同模块的合理预算本就不同，
// 而"全局默认"会让所有路由跟着最慢的那条走。
//
// 对话路由与管理路由分开：管理接口只查库，5 秒绰绰有余；对话要等模型，
// 用同一个数字意味着要么管理接口的超时形同虚设，要么对话被过早掐断。
const (
	manageDeadline   = 5 * time.Second
	manageWriteGrace = 2 * time.Second
	// chatWriteGrace 是非流式对话在业务预算之外的 socket 写宽限。
	//
	// 两级缺一不可：write deadline 一旦过期这条连接的写就废了，无法再可靠
	// 写出 504，所以负责回 504 的必须是更短的业务预算（见 middleware.Timeout）。
	chatWriteGrace = 5 * time.Second
)

// Deps 是注册路由所需的依赖。
//
// 用一个结构体而不是长参数列表：中间件会随功能增加，逐个加参数会让每次
// 新增都改动全部模块的签名。
type Deps struct {
	Service *aiapp.Service
	// Auth 是认证中间件，由装配层构造后注入。
	//
	// 模块不自己构造它：认证依赖 token parser 与用户校验器，那些是进程级
	// 资源，让每个模块各建一份会产生多份缓存与多份配置。
	Auth gin.HandlerFunc
	// RateLimit 按策略标识取限流中间件，并按作用域分成认证前、认证后两组。
	// 首参是引用发生处的路由标识，装配失败时用于指认现场。
	RateLimit func(route string, policyIDs ...string) (preAuth, postAuth []gin.HandlerFunc)
	// Cache 按 TTL 取路由级 GET 响应缓存中间件（cache-preset spec），由装配层注入。
	//
	// 本模块当前**全部不挂**：所有路由都要求身份，而带身份的请求在中间件内
	// 一律绕过缓存，挂上等于没挂。字段保留是装配契约——将来加匿名只读路由
	// 时直接使用，不需要再改装配层。
	Cache func(ttl time.Duration) gin.HandlerFunc
	// Timeouts 是两条对话路由的生效时长，由装配层从配置补齐默认值后传入。
	Timeouts Timeouts
}

// Register 在给定路由组上注册 ai 模块的路由。
//
// **访问控制由代码位置表达**：下面的路由必须写在带 deps.Auth 的组里面。
// 写到外面就是公开接口，而编译与测试都不会有任何异常——这是 gin 惯用写法的
// 已知代价，新增路由时需要人工确认（由 router_test.go 的逐路由断言兜底）。
func Register(r *gin.RouterGroup, deps Deps) {
	handler := NewHandler(deps.Service, deps.Timeouts)

	g := r.Group("/ai")

	// 默认限流：来源 + 身份两层。
	//
	// **两层都要**：只限身份时，一个来源可以用大量账号并发打满；
	// 只限来源时，就失去了按账号的精确限额。对 AI 端点这尤其要紧——
	// 每个放行的请求都是一笔真实的上游账单。
	preAuth, postAuth := stageRateLimits(deps.RateLimit, "/ai", "default.ip", "default.user")

	// 顺序是契约：来源限流挡的是尚未证明身份的流量，必须在认证之前；
	// 身份限流拿不到身份就退化成"每个请求都被拒"，必须在认证之后。
	g.Use(preAuth...)

	// 按请求体形态分子组，让**请求体治理排在认证之前**（http-request-contract spec）。
	//
	// 必须分组而不是把 JSONBody 写在路由上：gin 的组中间件恒先于路由级中间件，
	// 所以"认证挂组上、body 治理挂路由上"的写法里，认证永远先跑——一个
	// Content-Type 不符的请求要先付掉令牌解析、用户查库与撤销查 Redis 才被 415 拒。
	read := g.Group("", middleware.NoBody())
	write := g.Group("", middleware.JSONBody(0))

	// 认证与身份限流对两个子组同款：新增路由落进哪个子组都自动继承。
	for _, sub := range []*gin.RouterGroup{read, write} {
		sub.Use(deps.Auth)
		sub.Use(postAuth...)
	}

	// 只查库的路由用短预算。
	read.Use(middleware.Timeout(manageDeadline, manageWriteGrace))
	read.GET("/providers", handler.Providers)
	read.GET("/conversations", handler.ListConversations)
	read.GET("/conversations/:id", handler.GetConversation)

	// 写侧再分两组：管理操作只查/改库，与要等模型的对话预算差着一个数量级。
	manage := write.Group("", middleware.Timeout(manageDeadline, manageWriteGrace))
	manage.PATCH("/conversations/:id", handler.RenameConversation)
	manage.DELETE("/conversations/:id", handler.DeleteConversation)

	// 非流式对话：预算跟着配置里的 request_timeout 走，与实际下发给上游的
	// ctx 预算同源——两处分叉的话，要么本地先掐断（浪费一次已计费的调用），
	// 要么本地等得比上游还久（超时形同虚设）。
	chat := write.Group("", middleware.Timeout(deps.Timeouts.resolved().Request, chatWriteGrace))
	chat.POST("/chat", handler.Chat)

	// 流式对话**不挂任何超时中间件**，两条都不行：
	//
	//	middleware.Timeout      协作式 ctx deadline 会把正常生成中的长流掐断；
	//	middleware.StreamTimeout gtkit/ssex 自己管连接级写 deadline（起流时解除、
	//	                        每次写出前后设置与恢复），再挂一层等于两套机制
	//	                        互相覆盖，谁最后设的谁生效，行为无法推理。
	//
	// 整体预算由 handler 内的 context.WithTimeout(stream_total_timeout) 给出。
	write.POST("/chat/stream", handler.ChatStream)
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
