package http

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/middleware"
	"github.com/gtkit/gin-api/internal/module/example/application"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
)

// 本模块的业务超时预算：CRUD 各是一次数据库往返，5 秒足够；
// 写宽限 1 秒，让已开始的写在超时后有机会收尾。
const (
	exampleDeadline   = 5 * time.Second
	exampleWriteGrace = time.Second
	exampleBodyLimit  = 8 << 10
)

// Deps 是注册路由所需的依赖。
type Deps struct {
	Service    *application.Service
	Auth       gin.HandlerFunc
	Authorizer sharedauth.Authorizer
	// Signature 校验服务端间调用的请求签名（HMAC appId + timestamp + nonce）。
	// 非 nil 时在 /example/inter-service 组上挂载；它与 Auth 是不同防线——
	// Auth 证明"调用方是谁"，签名证明"参数未被篡改、非重放、来自持 appSecret
	// 的合法方"，服务端间调用不走 JWT 鉴权。由调用方用 middleware.Signature
	// 装配后注入（构造方式见 Register 内注释）。
	Signature gin.HandlerFunc
}

// Register mounts authenticated CRUD. The caller supplies its existing auth middleware.
func Register(parent *gin.RouterGroup, deps Deps) {
	if deps.Service == nil || deps.Auth == nil || deps.Authorizer == nil {
		panic("record service, authentication and authorization required")
	}
	handler := NewHandler(deps.Service)
	group := parent.Group("/example", middleware.NoStore(), middleware.Timeout(exampleDeadline, exampleWriteGrace), deps.Auth, middleware.Authorize(deps.Authorizer, "example.access"))
	group.GET("", middleware.NoBody(), handler.List)
	group.GET("/:id", middleware.NoBody(), handler.Get)
	group.POST("", middleware.JSONBody(exampleBodyLimit), handler.Create)
	group.PUT("/:id", middleware.JSONBody(exampleBodyLimit), handler.Update)
	group.DELETE("/batch", middleware.JSONBody(exampleBodyLimit), handler.DeleteMany)
	group.DELETE("/:id", middleware.NoBody(), handler.Delete)

	registerInterService(parent, deps)
}

// registerInterService 演示服务端间调用签名的装配：不挂 Auth/Authorize，
// 只挂 Signature。由调用方（bootstrap）按以下方式构造 Signature 中间件并
// 注入 Deps.Signature：
//
//	middleware.Signature(middleware.SignatureOptions{
//	    Secrets:     cfg.Signature.Clients, // appId -> appSecret
//	    Nonce:       apisign.NewRedisNonceStore(rdb, cfg.EffectiveRedisPrefix()),
//	    MaxSkew:     cfg.Signature.EffectiveMaxSkew(),
//	    ValidWindow: cfg.Signature.EffectiveValidWindow(),
//	})
func registerInterService(parent *gin.RouterGroup, deps Deps) {
	if deps.Signature == nil {
		return
	}
	inter := parent.Group("/example/inter-service", deps.Signature)
	// 服务端间回调端点：method/path/query/body 全部参与签名，
	// 改动任一字段验签即失败。这类请求没有登录身份，不读 ginc.UserID。
	inter.POST("/notify", middleware.JSONBody(exampleBodyLimit), func(c *gin.Context) {
		resp.Ok(c, gin.H{"received": true})
	})
}
