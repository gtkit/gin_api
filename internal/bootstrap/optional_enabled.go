//go:build optional

package bootstrap

import (
	"context"
	"errors"
	"fmt"

	"github.com/gin-gonic/gin"
	fileapp "github.com/gtkit/gin-api/internal/module/file/application"
	filehttp "github.com/gtkit/gin-api/internal/module/file/transport/http"
	"github.com/gtkit/gin-api/internal/optional/auditlog"
	"github.com/gtkit/gin-api/internal/optional/outbound"
	"github.com/gtkit/gin-api/internal/optional/taskqueue"
	"github.com/gtkit/gin-api/internal/optional/upload"
	fileruntime "github.com/gtkit/gin-api/internal/runtime/module/file"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
	"github.com/gtkit/gouploader"
)

type optionalInfra struct {
	outbound      *outbound.Downstreams
	tasksProducer *taskqueue.Producer
	uploader      *gouploader.Uploader
	auditRecorder *auditlog.Recorder
}

// optionalServices 承载 optional 构建的业务模块产物。
type optionalServices struct {
	file *fileapp.Service
}

// buildOptionalServices 装配依赖 optional 基础设施的业务模块。
// upload.enabled 且上传器已构造时才装配 file 模块；files 表缺失（无数据库）
// 在构造期即失败，不留到运行期。
func buildOptionalServices(svc *services, inf infra) error {
	if !inf.cfg.Upload.Enabled || inf.uploader == nil {
		return nil
	}
	fileService, err := fileruntime.New(inf.uploader, inf.mysqlDB,
		inf.cfg.Upload.Storage.EffectiveDriver(), inf.cfg.Upload.Storage.BucketName())
	if err != nil {
		return fmt.Errorf("build file module: %w", err)
	}
	svc.file = fileService
	return nil
}

// registerOptionalModules 挂载 optional 业务路由。已登录即可上传，
// 文件归属授权在 application 层按 owner_id 校验。
func registerOptionalModules(v1 *gin.RouterGroup, svc services, auth gin.HandlerFunc, inf infra) {
	if svc.file == nil {
		return
	}
	filehttp.Register(v1, filehttp.Deps{
		Service: svc.file,
		Auth:    auth,
		Authorizer: sharedauth.AuthorizerFunc(func(context.Context, sharedauth.Principal, sharedauth.AuthorizationRequest) (bool, error) {
			return true, nil
		}),
		Policy: upload.DefaultPolicy(),
		// 请求体上限 = 文件大小上限 + 1MB multipart 表单开销。
		MaxUploadBytes: inf.cfg.Upload.MaxFileSize + 1<<20,
		// 库默认单片上限 20MB + 1MB 表单开销。
		MaxChunkBytes:  21 << 20,
		RequestTimeout: inf.cfg.Upload.EffectiveRequestTimeout(),
	})
}

func buildOptionalProviders(inf *infra, cleanup *providerCleanup) error {
	// Validate before constructors with side effects; cleanup remains owned by the caller.
	// 驱动相关校验（local 的 dir、云凭证、redis_db）在配置加载与 NewUploader 内完成。
	if inf.cfg.Upload.Enabled && (inf.cfg.Upload.ChunkDir == "" || inf.cfg.Upload.MaxFileSize <= 0 || len(inf.cfg.Upload.AllowedTypes) == 0) {
		return errors.New("upload configuration is incomplete")
	}
	if len(inf.cfg.Outbound.Clients) > 0 {
		if err := buildOutbound(inf, cleanup); err != nil {
			return err
		}
	}
	if inf.cfg.Tasks.Enabled {
		if err := buildTaskQueueProducer(inf, cleanup); err != nil {
			return err
		}
	}
	if cfg := inf.cfg.Upload; cfg.Enabled {
		if err := buildUploader(inf, cleanup); err != nil {
			return err
		}
	}
	if inf.cfg.AuditLog.Enabled {
		if inf.mysqlDB == nil {
			return errors.New("auditlog requires a database")
		}
		if err := buildAuditLog(inf); err != nil {
			return err
		}
	}
	return nil
}

func optionalWorkers(inf *infra) ([]Worker, error) {
	var workers []Worker
	if inf.cfg.Tasks.Enabled {
		queue, err := buildTaskQueueWorkers(inf)
		if err != nil {
			return nil, err
		}
		workers = append(workers, queue...)
	}
	if inf.auditRecorder != nil {
		workers = append(workers, &auditLogWorker{recorder: inf.auditRecorder})
	}
	return workers, nil
}
