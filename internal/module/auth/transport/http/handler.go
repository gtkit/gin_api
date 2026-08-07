// Package http 是认证模块的 HTTP 适配层。
package http

import (
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/goerr"

	"github.com/gtkit/gin-api/internal/module/auth/application"
	"github.com/gtkit/gin-api/internal/module/auth/dto"
	"github.com/gtkit/gin-api/internal/pkg/ginc"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
)

// errMapper 把认证领域错误映射为对客状态码。
//
// **ErrInvalidCredentials 与"账号不存在"共用一条映射**是刻意的：application
// 已经把两者合并成同一个错误，这里若再拆开，防账号枚举的努力会在最后一步失效。
//
// 选 Status 时看的是它的 **HTTP 码**，不是名字。goerr 里有一批"业务码 +
// HTTP 200"型的状态（如 StatusPasswordIncorrect、StatusAccountDisabled），
// 名字读起来正合适，实际会让一次登录失败返回 200——而路由描述符声明的是 401。
// 那种组合下契约在说谎，客户端按状态码写的分支全部失效。
var errMapper = resp.NewErrMapper(
	resp.ErrMapping{
		Target: application.ErrInvalidCredentials,
		Status: goerr.StatusAuth(), Message: "手机号或密码错误",
	},
	resp.ErrMapping{
		Target: application.ErrAccountInactive,
		Status: goerr.StatusPermission(), Message: "账号不可用",
	},
	resp.ErrMapping{
		// 重复注册是正常的业务结果，不是服务端故障。
		Target: application.ErrPhoneTaken,
		Status: goerr.StatusAlreadyExists(), Message: "该手机号已注册",
	},
	resp.ErrMapping{
		// 刷新失败一律指向"重新登录"：区分具体原因只会告诉攻击者
		// 他手里的令牌处于哪种状态。
		Target: application.ErrRefreshRejected,
		Status: goerr.StatusLoginExpired(), Message: "登录状态已失效，请重新登录",
	},
	resp.ErrMapping{
		Target: application.ErrPasswordTooLong,
		Status: goerr.StatusValidateParams(), Message: "密码过长",
	},
)

// Handler 是认证端点。
type Handler struct {
	service *application.Service
}

// NewHandler 构造 Handler。
func NewHandler(service *application.Service) *Handler {
	return &Handler{service: service}
}

// Register 建号并直接签发令牌。
func (h *Handler) Register(c *gin.Context) {
	var req dto.RegisterRequest
	if err := c.ShouldBind(&req); err != nil {
		resp.BindStructErr(c, err)
		return
	}

	pair, err := h.service.Register(c.Request.Context(), req.Phone, req.Password, req.Nickname)
	if err != nil {
		// 与 Login 同款：账号维度限流走 429（见 Login 的注释）。
		if errors.Is(err, application.ErrAccountRateLimited) {
			resp.TooManyRequestsErr(c, "尝试过于频繁，请稍后再试")
			return
		}
		resp.MapErr(c, err, errMapper)
		return
	}

	resp.Ok(c, tokenResponse(pair))
}

// Login 校验凭据并签发令牌。
func (h *Handler) Login(c *gin.Context) {
	var req dto.LoginRequest
	if err := c.ShouldBind(&req); err != nil {
		resp.BindStructErr(c, err)
		return
	}

	pair, err := h.service.Login(c.Request.Context(), req.Phone, req.Password)
	if err != nil {
		// 账号维度限流必须是 429 而不是 401/403：客户端与网关对
		// "退避后重试"与"凭据错误"的处置完全不同。
		if errors.Is(err, application.ErrAccountRateLimited) {
			resp.TooManyRequestsErr(c, "尝试过于频繁，请稍后再试")
			return
		}
		resp.MapErr(c, err, errMapper)
		return
	}

	resp.Ok(c, tokenResponse(pair))
}

// Refresh 用刷新令牌换取新的令牌对。
func (h *Handler) Refresh(c *gin.Context) {
	var req dto.RefreshRequest
	if err := c.ShouldBind(&req); err != nil {
		resp.BindStructErr(c, err)
		return
	}

	pair, err := h.service.Refresh(c.Request.Context(), req.RefreshToken)
	if err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}

	resp.Ok(c, tokenResponse(pair))
}

// Logout 撤销当前访问令牌与刷新令牌族。
//
// 本端点要求认证：要撤销的正是本次请求所用的那个令牌，而它的标识只有
// 认证中间件能提供。
func (h *Handler) Logout(c *gin.Context) {
	var req dto.LogoutRequest
	if err := c.ShouldBind(&req); err != nil {
		resp.BindStructErr(c, err)
		return
	}

	identity, ok := ginc.TokenIdentityFromContext(c)
	if !ok || identity.TokenID == "" || identity.ExpiresAt.IsZero() {
		// 拿不到令牌标识就无法撤销。报错而不是回成功——回成功会让用户
		// 以为自己已经登出，而那个令牌仍然可用直到自然过期。
		resp.Err(c, goerr.New(nil, goerr.StatusAuth(),
			"cannot revoke a token without an id and expiry"))
		return
	}

	if err := h.service.Logout(c.Request.Context(), identity.TokenID, identity.ExpiresAt, req.RefreshToken); err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}

	resp.Ok(c, nil)
}

// tokenResponse 把领域结果转成对外契约。
func tokenResponse(pair application.TokenPair) dto.TokenResponse {
	return dto.TokenResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    pair.ExpiresIn,
		TokenType:    "Bearer",
	}
}
