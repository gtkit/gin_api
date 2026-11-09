//go:build optional

package bootstrap

import (
	"errors"

	"github.com/gtkit/gin-api/internal/optional/auditlog"
	"github.com/gtkit/gin-api/internal/optional/outbound"
	"github.com/gtkit/gin-api/internal/optional/taskqueue"
	"github.com/gtkit/gin-api/internal/optional/upload"
	"github.com/gtkit/gouploader"
)

type optionalInfra struct {
	outbound      *outbound.Downstreams
	tasksProducer *taskqueue.Producer
	uploader      *gouploader.Uploader
	auditRecorder *auditlog.Recorder
}

func buildOptionalProviders(inf *infra, cleanup *providerCleanup) error {
	// Validate before constructors with side effects; cleanup remains owned by the caller.
	if inf.cfg.Upload.Enabled && (inf.cfg.Upload.Dir == "" || inf.cfg.Upload.ChunkDir == "" || inf.cfg.Upload.MaxFileSize <= 0 || len(inf.cfg.Upload.AllowedTypes) == 0) {
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
		if err := buildLocalUploader(inf, cleanup, upload.LocalOptions{Dir: cfg.Dir, MaxFileSize: cfg.MaxFileSize, AllowedTypes: cfg.AllowedTypes}, gouploader.WithChunkTempDir(cfg.ChunkDir)); err != nil {
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
