package http

import (
	"errors"

	"github.com/gtkit/gin-api/internal/module/user/application"
	"github.com/gtkit/gin-api/internal/module/user/dto"
	"github.com/gtkit/gin-api/internal/pkg/ginc"
	resp "github.com/gtkit/gin-api/internal/pkg/response"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/goerr"
)

// errMapper 统一把 user 领域 error 映射为对客状态码，避免各 handler 手写错误码。
// 未匹配到的 error 由 resp.MapErr 归为 StatusInternalServer，不再把库层细节（如 StatusMysqlSQL）泄漏给客户端。
var errMapper = resp.NewErrMapper(
	resp.ErrMapping{Target: application.ErrUserNotFound, Status: goerr.StatusNotFound(), Message: "用户不存在或已注销"},
)

type Handler struct {
	service *application.Service
}

func NewHandler(service *application.Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) Get(c *gin.Context) {
	userID := ginc.UserIDFromContext(c)
	if userID == 0 {
		resp.Err(c, goerr.New(nil, goerr.StatusAuth(), "user not authenticated"))
		return
	}

	data, err := h.service.Profile(c.Request.Context(), userID)
	if err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}

	resp.Ok(c, data)
}

func (h *Handler) Update(c *gin.Context) {
	var req dto.UserUpdateRequest
	if err := c.ShouldBind(&req); err != nil {
		resp.BindStructErr(c, err)
		return
	}

	// 至少要传一个可更新字段（手机号更新需独立验证码流程，不在此处处理）。
	if req.NickName == "" && req.Avatar == "" && req.Gender == nil {
		resp.OkErr(c, goerr.New(nil, goerr.StatusParams(), "至少有一个字段不能为空"))
		return
	}

	userID := ginc.UserIDFromContext(c)
	if userID == 0 {
		resp.Err(c, goerr.New(nil, goerr.StatusAuth(), "user not authenticated"))
		return
	}
	cmd := application.UpdateProfileCommand{
		NickName: req.NickName,
		Gender:   req.Gender,
		Avatar:   req.Avatar,
	}
	if err := h.service.UpdateProfile(c.Request.Context(), userID, cmd); err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}
	resp.Ok(c, nil)
}

// Deactivate 注销账号（方案 A：脱敏改写 + 写注销记录）。
// 不可逆，客户端调用前需自行做二次确认。
//
// 幂等：用户已是注销态时返回 200 + msg="账号已注销"，便于客户端重试不报错。
func (h *Handler) Deactivate(c *gin.Context) {
	var req dto.UserDeactivateRequest
	if err := c.ShouldBind(&req); err != nil {
		resp.BindStructErr(c, err)
		return
	}

	userID := ginc.UserIDFromContext(c)
	if userID == 0 {
		resp.Err(c, goerr.New(nil, goerr.StatusAuth(), "user not authenticated"))
		return
	}

	err := h.service.Deactivate(c.Request.Context(), application.DeactivateInput{
		UserID:     userID,
		Reason:     req.Reason,
		Channel:    req.Channel,
		OperatorIP: c.ClientIP(),
	})
	// 幂等：重复注销视为成功，避免客户端因丢包重试报错。
	if err == nil || errors.Is(err, application.ErrAlreadyDeactivated) {
		resp.Ok(c, nil)
		return
	}
	// 其余错误统一走 errMapper：ErrUserNotFound → StatusNotFound，其他 → StatusInternalServer。
	resp.MapErr(c, err, errMapper)
}
