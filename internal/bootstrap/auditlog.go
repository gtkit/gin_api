package bootstrap

// AUDITLOG: 审计的可选装配及 worker 适配。默认整个实现为注释。
// 启用时去掉下面一对 /* 和 */，并按 internal/optional/auditlog/README.md 恢复接线。

/*
import (
	"context"
	"fmt"

	"github.com/gtkit/gin-api/internal/optional/auditlog"
)

func buildAuditLog(inf *infra) error {
	recorder, err := auditlog.New(auditlog.NewRepository(inf.mysqlDB), auditlog.Options{})
	if err != nil {
		return fmt.Errorf("build audit log: %w", err)
	}
	inf.auditRecorder = recorder
	return nil
}

// auditLogWorker 属于 consumer：停止接收 HTTP 请求后再排空已有审计记录。
// 记录器只分配有界队列；它不在 Prepare 阶段取得额外资源。
type auditLogWorker struct {
	recorder *auditlog.Recorder
}

func (w *auditLogWorker) Name() string { return "audit-log" }
func (w *auditLogWorker) Kind() WorkerKind { return KindConsumer }
func (w *auditLogWorker) Required() bool { return false }
func (w *auditLogWorker) Lifecycle() Lifecycle { return LifecycleLongRunning }
func (w *auditLogWorker) Prepare(context.Context) error { return nil }
func (w *auditLogWorker) Run(ctx context.Context) error { return w.recorder.Run(ctx) }
func (w *auditLogWorker) Cleanup(context.Context) error { return nil }
*/
