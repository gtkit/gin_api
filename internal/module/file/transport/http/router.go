package http

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/middleware"
	"github.com/gtkit/gin-api/internal/module/file/application"
	"github.com/gtkit/gin-api/internal/optional/upload"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
)

// Deps 是注册路由所需的依赖。
type Deps struct {
	Service    *application.Service
	Auth       gin.HandlerFunc
	Authorizer sharedauth.Authorizer
	// Policy 是下载回源的内联白名单；下载路由无扩展名，fail-closed 全部附件。
	Policy upload.Policy
	// MaxUploadBytes 是普通上传请求体上限（文件大小 + multipart 开销）。
	MaxUploadBytes int64
	// MaxChunkBytes 是分片请求体上限（库默认单片上限 20MB + 表单开销）。
	MaxChunkBytes int64
	// RequestTimeout 是上传/合并/下载路由的超时；非正时按 300s。
	RequestTimeout time.Duration
}

// Register 在给定路由组上注册上传协议与文件管理路由。
//
// 只在 optional 构建且 upload.enabled 时调用。路由布局按 method 树避开
// gin 静态/参数同级冲突：POST 树只有 init/chunk/resume/merge 静态段，
// :upload_id 只出现在 GET/DELETE 树。
func Register(parent *gin.RouterGroup, deps Deps) {
	if deps.Service == nil || deps.Auth == nil || deps.Authorizer == nil {
		panic("file service, authentication and authorization required")
	}
	if deps.RequestTimeout <= 0 {
		deps.RequestTimeout = 300 * time.Second
	}
	handler := NewHandler(deps.Service)
	authorize := middleware.Authorize(deps.Authorizer, "file.access")
	uploadTimeout := middleware.Timeout(deps.RequestTimeout, time.Second)

	up := parent.Group("/upload", middleware.NoStore())
	up.POST("", middleware.MultipartBody(deps.MaxUploadBytes), uploadTimeout, deps.Auth, authorize, handler.Upload)
	up.POST("/init", middleware.JSONBody(8192), uploadTimeout, deps.Auth, authorize, handler.ChunkInit)
	up.POST("/chunk", middleware.MultipartBody(deps.MaxChunkBytes), uploadTimeout, deps.Auth, authorize, handler.UploadChunk)
	up.POST("/resume", middleware.JSONBody(8192), uploadTimeout, deps.Auth, authorize, handler.Resume)
	up.POST("/merge/:upload_id", middleware.NoBody(), uploadTimeout, deps.Auth, authorize, handler.Merge)
	up.GET("/status/:upload_id", middleware.NoBody(), deps.Auth, authorize, handler.Status)
	up.DELETE("/:upload_id", middleware.NoBody(), deps.Auth, authorize, handler.Abort)

	files := parent.Group("/files", middleware.NoStore())
	files.GET("", middleware.NoBody(), deps.Auth, authorize, handler.List)
	files.GET("/:id", middleware.NoBody(), deps.Auth, authorize, handler.Get)
	files.DELETE("/:id", middleware.NoBody(), deps.Auth, authorize, handler.Delete)
	files.GET("/:id/download", middleware.NoBody(), upload.FileHeaders(deps.Policy), uploadTimeout, deps.Auth, authorize, handler.Download)
}
