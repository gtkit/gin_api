package taskqueue

import (
	"context"

	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/hibiken/asynq"
)

func executionMetrics(recorder *metrics.Recorder, consumer string) asynq.MiddlewareFunc {
	return func(next asynq.Handler) asynq.Handler {
		return asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) error {
			return recorder.RunWorker(ctx, metrics.WorkerQueue, consumer, func(ctx context.Context) error {
				return next.ProcessTask(ctx, task)
			})
		})
	}
}
