//go:build optional

package bootstrap

import (
	"context"
	"time"

	fileapp "github.com/gtkit/gin-api/internal/module/file/application"
	"github.com/gtkit/logger/v2"
	"go.uber.org/zap"
)

func fileRecoveryWorkers(svc services) []Worker {
	if svc.file == nil {
		return nil
	}
	return []Worker{fileRecoveryWorker{service: svc.file}}
}

type fileRecoveryWorker struct{ service *fileapp.Service }

func (fileRecoveryWorker) Name() string                  { return "file-recovery" }
func (fileRecoveryWorker) Kind() WorkerKind              { return KindConsumer }
func (fileRecoveryWorker) Required() bool                { return true }
func (fileRecoveryWorker) Lifecycle() Lifecycle          { return LifecycleLongRunning }
func (fileRecoveryWorker) Cleanup(context.Context) error { return nil }

func (w fileRecoveryWorker) Prepare(ctx context.Context) error {
	return w.service.CheckRecovery(ctx)
}

func (w fileRecoveryWorker) Run(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	nextPrune := time.Now().Add(time.Minute)
	for {
		batch, cancel := context.WithTimeout(ctx, 45*time.Second)
		err := w.service.RecoverOperations(batch)
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.WarnCtx(ctx, "file recovery batch incomplete", zap.Error(err))
		}
		if time.Now().After(nextPrune) && ctx.Err() == nil {
			prune, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := w.service.PruneCompletedAttempts(prune)
			cancel()
			if err != nil && ctx.Err() == nil {
				logger.WarnCtx(ctx, "prune completed file attempts", zap.Error(err))
			}
			nextPrune = time.Now().Add(time.Hour)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
