package http

import (
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
// **注销后账号立即不可用**：状态改写后，认证阶段的用户校验就不再通过，
// 因此后续请求（包括客户端因丢包发起的重试）会在进入本 handler 之前被拒。
//
// 此处曾声称"重复注销返回 200"。那个承诺在真实链路上无法兑现——第二次请求
// 到不了这里；即便到了，仓储只锁未软删的行，也拿不到已注销用户。
// 要提供跨请求幂等，正确的做法是 Idempotency-Key 或按操作记录查重，
// 而不是放宽全局认证让已注销账号继续通过——那会让注销失去意义。
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
		OperatorIP: ginc.ClientIP(c),
	})
	if err == nil {
		resp.Ok(c, nil)
		return
	}
	// 其余错误统一走 errMapper：ErrUserNotFound → StatusNotFound，其他 → StatusInternalServer。
	resp.MapErr(c, err, errMapper)
}
