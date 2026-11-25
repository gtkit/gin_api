// Package http 是文件上传模块的 HTTP 适配层。
package http

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/ginx"
	"github.com/gtkit/goerr"
	"github.com/gtkit/gouploader"
	"github.com/gtkit/logger/v2"

	"github.com/gtkit/gin-api/internal/module/file/application"
	"github.com/gtkit/gin-api/internal/module/file/dto"
	"github.com/gtkit/gin-api/internal/pkg/ginc"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
)

// errMapper 把文件领域错误与上传引擎哨兵映射为对客状态码。
//
// 选 Status 时看的是它的 HTTP 码：会话越权是 403（StatusPermission），
// 会话不存在/过期是 404，类型/大小/参数拒绝走 4xx 校验族，
// 存储介质故障是 5xx 基础设施族——不把底层错误字符串透给客户端。
var errMapper = resp.NewErrMapper(
	resp.ErrMapping{Target: application.ErrNotFound, Status: goerr.StatusFileNotFound(), Message: "文件不存在"},
	resp.ErrMapping{Target: application.ErrForbidden, Status: goerr.StatusPermission()},
	resp.ErrMapping{Target: application.ErrInvalid, Status: goerr.StatusValidateParams()},
	resp.ErrMapping{Target: application.ErrStorageLocationMismatch, Status: goerr.StatusConflict(), Message: "文件存储位置与当前部署不匹配"},
	resp.ErrMapping{Target: application.ErrDependencyUnavailable, Status: goerr.StatusDependencyUnavailable()},
	resp.ErrMapping{Target: gouploader.ErrSessionNotFound, Status: goerr.StatusNotFound(), Message: "上传会话不存在"},
	resp.ErrMapping{Target: gouploader.ErrSessionExpired, Status: goerr.StatusNotFound(), Message: "上传会话已过期"},
	resp.ErrMapping{Target: gouploader.ErrUnauthorized, Status: goerr.StatusPermission(), Message: "无权访问该上传会话"},
	resp.ErrMapping{Target: gouploader.ErrFileTypeNotAllow, Status: goerr.StatusFileTypeNotAllowed()},
	resp.ErrMapping{Target: gouploader.ErrFileTooLarge, Status: goerr.StatusFileTooLarge()},
	resp.ErrMapping{Target: gouploader.ErrChecksumMismatch, Status: goerr.StatusFileChecksumMismatch()},
	resp.ErrMapping{Target: gouploader.ErrChunkHashMismatch, Status: goerr.StatusFileChecksumMismatch()},
	resp.ErrMapping{Target: gouploader.ErrChunkIndexOutRange, Status: goerr.StatusValidateParams()},
	resp.ErrMapping{Target: gouploader.ErrMergeIncomplete, Status: goerr.StatusValidateParams(), Message: "分片未传齐"},
	resp.ErrMapping{Target: gouploader.ErrMergeInProgress, Status: goerr.StatusConflict(), Message: "合并进行中，请稍后重试"},
	resp.ErrMapping{Target: gouploader.ErrInvalidParam, Status: goerr.StatusValidateParams()},
	resp.ErrMapping{Target: gouploader.ErrStorageWrite, Status: goerr.StatusFileStorageUnavailable()},
	resp.ErrMapping{Target: gouploader.ErrStorageRead, Status: goerr.StatusFileStorageUnavailable()},
	resp.ErrMapping{Target: gouploader.ErrStorageDelete, Status: goerr.StatusFileStorageUnavailable()},
)

// Handler 是上传协议与文件管理端点。
type Handler struct {
	service     *application.Service
	uploadBytes int64
	chunkBytes  int64
	stagingDir  string
}

// NewHandler 构造 Handler。
func NewHandler(service *application.Service) *Handler {
	return &Handler{service: service, uploadBytes: 10 << 20, chunkBytes: 20 << 20}
}

// Upload 普通单文件上传（multipart，流式落盘并建立归属记录）。
func (h *Handler) Upload(c *gin.Context) {
	form, err := readTransferForm(c.Request, h.uploadBytes, h.stagingDir, false)
	if err != nil {
		transferFormError(c, err)
		return
	}
	defer closeTransferForm(form)
	file, err := form.payload.Reader()
	if err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}
	record, err := h.service.UploadPrepared(c.Request.Context(), ginc.UserIDFromContext(c), form.name, file, form.size, form.checksum)
	if err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}
	resp.Ok(c, record)
}

// ChunkInit 初始化分片会话。
func (h *Handler) ChunkInit(c *gin.Context) {
	var request dto.ChunkInitRequest
	if err := ginx.BindBody(c, &request); err != nil {
		resp.BindStructErr(c, err)
		return
	}
	result, err := h.service.ChunkInit(c.Request.Context(), ginc.UserIDFromContext(c), &request)
	if err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}
	resp.Ok(c, result)
}

// UploadChunk 上传单个分片（multipart，chunkIndex 0-based）。
func (h *Handler) UploadChunk(c *gin.Context) {
	form, err := readTransferForm(c.Request, h.chunkBytes, h.stagingDir, true)
	if err != nil {
		transferFormError(c, err)
		return
	}
	defer closeTransferForm(form)
	file, err := form.payload.Reader()
	if err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}
	result, err := h.service.UploadChunk(c.Request.Context(), ginc.UserIDFromContext(c), form.chunk.UploadID, form.chunk.ChunkIndex, file, form.size, form.chunk.Checksum)
	if err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}
	resp.Ok(c, result)
}

func transferFormError(c *gin.Context, err error) {
	if errors.Is(err, application.ErrDependencyUnavailable) {
		resp.MapErr(c, err, errMapper)
		return
	}
	resp.BindStructErr(c, err)
}

func closeTransferForm(form *transferForm) {
	if err := form.payload.Close(); err != nil {
		logger.Errorf("upload: close staged payload: %v", err)
	}
}

// Resume 按整文件哈希找回未完成会话（断点续传）。
func (h *Handler) Resume(c *gin.Context) {
	var request dto.ResumeRequest
	if err := ginx.BindBody(c, &request); err != nil {
		resp.BindStructErr(c, err)
		return
	}
	result, err := h.service.Resume(c.Request.Context(), ginc.UserIDFromContext(c), &request)
	if err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}
	resp.Ok(c, result)
}

// Merge 合并分片并建立归属记录（重复合并幂等）。
func (h *Handler) Merge(c *gin.Context) {
	record, err := h.service.Merge(c.Request.Context(), ginc.UserIDFromContext(c), c.Param("upload_id"))
	if err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}
	resp.Ok(c, record)
}

// Status 查询分片进度。
func (h *Handler) Status(c *gin.Context) {
	result, err := h.service.Status(c.Request.Context(), ginc.UserIDFromContext(c), c.Param("upload_id"))
	if err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}
	resp.Ok(c, result)
}

// Abort 中止会话并清理分片数据。
func (h *Handler) Abort(c *gin.Context) {
	if err := h.service.Abort(c.Request.Context(), ginc.UserIDFromContext(c), c.Param("upload_id")); err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}
	resp.Ok(c, nil)
}

// List 分页列出当前用户的文件。
func (h *Handler) List(c *gin.Context) {
	var query dto.ListRequest
	if err := c.ShouldBindQuery(&query); err != nil {
		resp.BindStructErr(c, err)
		return
	}
	if cursors, ok := c.GetQueryArray("cursor"); ok {
		if len(cursors) != 1 || len(c.QueryArray("page")) != 0 {
			resp.MapErr(c, application.ErrInvalid, errMapper)
			return
		}
		page, err := h.service.ListAfter(c.Request.Context(), ginc.UserIDFromContext(c), cursors[0], query.PageSize)
		if err != nil {
			resp.MapErr(c, err, errMapper)
			return
		}
		resp.Ok(c, page)
		return
	}
	page, err := h.service.List(c.Request.Context(), ginc.UserIDFromContext(c), query.Page, query.PageSize)
	if err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}
	resp.Ok(c, page)
}

// Get 返回文件元数据。
func (h *Handler) Get(c *gin.Context) {
	record, err := h.service.Get(c.Request.Context(), ginc.UserIDFromContext(c), c.Param("id"))
	if err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}
	resp.Ok(c, record)
}

// Delete 删除存储对象并软删记录。
func (h *Handler) Delete(c *gin.Context) {
	if err := h.service.Delete(c.Request.Context(), ginc.UserIDFromContext(c), c.Param("id")); err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}
	resp.Ok(c, nil)
}

// Download 归属校验通过后流式回源（强制附件下载）。
func (h *Handler) Download(c *gin.Context) {
	record, stream, err := h.service.Download(c.Request.Context(), ginc.UserIDFromContext(c), c.Param("id"))
	if err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}
	defer func() {
		if err := stream.Close(); err != nil {
			logger.Errorf("download: close storage stream: %v", err)
		}
	}()
	c.Header("Content-Type", record.MIMEType)
	c.Header("Content-Length", strconv.FormatInt(record.Size, 10))
	c.Status(http.StatusOK)
	if _, err := io.Copy(c.Writer, stream); err != nil {
		if !c.Writer.Written() {
			c.Writer.Header().Del("Content-Length")
			c.Writer.Header().Del("Content-Disposition")
		}
		c.Error(err).Type = gin.ErrorTypePrivate
	}
}
