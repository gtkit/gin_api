// Package http 是记录示例模块的 HTTP 适配层。
package http

import (
	"github.com/gin-gonic/gin"
	"github.com/gtkit/ginx"
	"github.com/gtkit/goerr"

	"github.com/gtkit/gin-api/internal/module/example/application"
	"github.com/gtkit/gin-api/internal/module/example/dto"
	"github.com/gtkit/gin-api/internal/pkg/ginc"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
)

// errMapper 把记录领域错误映射为对客状态码。
//
// 选 Status 时看的是它的 HTTP 码：ErrNotFound 覆盖「不存在或属于他人」
// （仓储按 owner 作用域查询，两种情况在 application 已合并），这里不再拆开，
// 避免向跨 owner 的探测者泄漏记录是否存在；参数拒绝走 4xx 校验族；
// 未登记的错误由 MapErr 按内部错误处理，ctx 取消/超时交终态中间件裁决。
var errMapper = resp.NewErrMapper(
	resp.ErrMapping{Target: application.ErrNotFound, Status: goerr.StatusNotFound(), Message: "record not found"},
	resp.ErrMapping{Target: application.ErrInvalid, Status: goerr.StatusValidateParams(), Message: "invalid record input"},
	resp.ErrMapping{Target: application.ErrForbidden, Status: goerr.StatusPermission()},
)

// Handler 是记录 CRUD 端点。
type Handler struct {
	service *application.Service
}

// NewHandler 构造 Handler。
func NewHandler(service *application.Service) *Handler {
	return &Handler{service: service}
}

// List 分页列出当前用户的记录。
func (h *Handler) List(c *gin.Context) {
	var query dto.ListRequest
	if err := c.ShouldBindQuery(&query); err != nil {
		resp.BindStructErr(c, err)
		return
	}
	result, err := h.service.List(c.Request.Context(), ginc.UserIDFromContext(c), query.Page, query.PageSize)
	if err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}
	resp.Ok(c, result)
}

// Get 返回单条记录。
func (h *Handler) Get(c *gin.Context) {
	result, err := h.service.Get(c.Request.Context(), ginc.UserIDFromContext(c), c.Param("id"))
	if err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}
	resp.Ok(c, result)
}

// Create 新建记录。
func (h *Handler) Create(c *gin.Context) {
	var request dto.WriteRequest
	if err := ginx.BindBody(c, &request); err != nil {
		resp.BindStructErr(c, err)
		return
	}
	result, err := h.service.Create(c.Request.Context(), ginc.UserIDFromContext(c), request.Title)
	if err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}
	resp.Ok(c, result)
}

// Update 更新记录标题。
func (h *Handler) Update(c *gin.Context) {
	var request dto.WriteRequest
	if err := ginx.BindBody(c, &request); err != nil {
		resp.BindStructErr(c, err)
		return
	}
	result, err := h.service.Update(c.Request.Context(), ginc.UserIDFromContext(c), c.Param("id"), request.Title)
	if err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}
	resp.Ok(c, result)
}

// DeleteMany 批量删除，全有或全无。
func (h *Handler) DeleteMany(c *gin.Context) {
	var request dto.DeleteManyRequest
	if err := ginx.BindBody(c, &request); err != nil {
		resp.BindStructErr(c, err)
		return
	}
	if err := h.service.DeleteMany(c.Request.Context(), ginc.UserIDFromContext(c), request.IDs); err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}
	resp.Ok(c, nil)
}

// Delete 删除单条记录。
func (h *Handler) Delete(c *gin.Context) {
	if err := h.service.Delete(c.Request.Context(), ginc.UserIDFromContext(c), c.Param("id")); err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}
	resp.Ok(c, nil)
}
