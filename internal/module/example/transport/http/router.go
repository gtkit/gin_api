package http

import (
	"errors"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/middleware"
	"github.com/gtkit/gin-api/internal/module/example/application"
	"github.com/gtkit/gin-api/internal/module/example/dto"
	"github.com/gtkit/gin-api/internal/pkg/ginc"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
	"github.com/gtkit/ginx"
	"github.com/gtkit/goerr"
)

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
	group := parent.Group("/example", middleware.NoStore(), middleware.Timeout(5*time.Second, time.Second), deps.Auth, middleware.Authorize(deps.Authorizer, "example.access"))
	handleError := func(c *gin.Context, err error) {
		switch {
		case errors.Is(err, application.ErrNotFound):
			resp.NotFoundError(c, "record not found")
		case errors.Is(err, application.ErrInvalid):
			resp.Err(c, goerr.New(err, goerr.StatusValidateParams(), "invalid record input"))
		case errors.Is(err, application.ErrForbidden):
			resp.Err(c, goerr.New(err, goerr.StatusPermission()))
		default:
			resp.MapErr(c, err, nil)
		}
	}
	group.GET("", middleware.NoBody(), func(c *gin.Context) {
		var query dto.ListRequest
		if err := c.ShouldBindQuery(&query); err != nil {
			resp.BindStructErr(c, err)
			return
		}
		result, err := deps.Service.List(c.Request.Context(), ginc.UserIDFromContext(c), query.Page, query.PageSize)
		if err != nil {
			handleError(c, err)
			return
		}
		resp.Ok(c, result)
	})
	group.GET("/:id", middleware.NoBody(), func(c *gin.Context) {
		result, err := deps.Service.Get(c.Request.Context(), ginc.UserIDFromContext(c), c.Param("id"))
		if err != nil {
			handleError(c, err)
			return
		}
		resp.Ok(c, result)
	})
	group.POST("", middleware.JSONBody(8192), func(c *gin.Context) {
		var request dto.WriteRequest
		if err := ginx.BindBody(c, &request); err != nil {
			resp.BindStructErr(c, err)
			return
		}
		result, err := deps.Service.Create(c.Request.Context(), ginc.UserIDFromContext(c), request.Title)
		if err != nil {
			handleError(c, err)
			return
		}
		resp.Ok(c, result)
	})
	group.PUT("/:id", middleware.JSONBody(8192), func(c *gin.Context) {
		var request dto.WriteRequest
		if err := ginx.BindBody(c, &request); err != nil {
			resp.BindStructErr(c, err)
			return
		}
		result, err := deps.Service.Update(c.Request.Context(), ginc.UserIDFromContext(c), c.Param("id"), request.Title)
		if err != nil {
			handleError(c, err)
			return
		}
		resp.Ok(c, result)
	})
	group.DELETE("/batch", middleware.JSONBody(8192), func(c *gin.Context) {
		var request dto.DeleteManyRequest
		if err := ginx.BindBody(c, &request); err != nil {
			resp.BindStructErr(c, err)
			return
		}
		if err := deps.Service.DeleteMany(c.Request.Context(), ginc.UserIDFromContext(c), request.IDs); err != nil {
			handleError(c, err)
			return
		}
		resp.Ok(c, nil)
	})
	group.DELETE("/:id", middleware.NoBody(), func(c *gin.Context) {
		if err := deps.Service.Delete(c.Request.Context(), ginc.UserIDFromContext(c), c.Param("id")); err != nil {
			handleError(c, err)
			return
		}
		resp.Ok(c, nil)
	})

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
	inter.POST("/notify", middleware.JSONBody(8192), func(c *gin.Context) {
		resp.Ok(c, gin.H{"received": true})
	})
}
