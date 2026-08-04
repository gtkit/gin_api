package bootstrap

import (
	"context"

	userworker "github.com/gtkit/gin-api/internal/module/user/worker"
)

// deactivationCleanupWorker 把纯业务的清理逻辑适配到进程编排契约。
//
// **适配层在 bootstrap 而不是模块内**：Kind / Lifecycle 的类型属于本包，
// 让业务模块实现它们会使依赖方向倒转，而 runtime 层实现它们会形成
// bootstrap ↔ runtime 的循环依赖（实测确实如此）。
// 业务模块只提供 Run，编排语义由这里补齐。
type deactivationCleanupWorker struct {
	cleanup *userworker.DeactivationCleanup
}

// newDeactivationCleanupWorker 把 runtime 装配好的清理任务包成编排契约。
//
// 仓储与业务对象的装配在 runtime/module/user——编排层不该知道 user 模块
// 用了哪个仓储。这里只补齐 Kind / Lifecycle / Required 这类**编排语义**，
// 它们的类型属于本包，在模块层实现会让依赖方向倒转。
//
// cleanup 为 nil（未配置保留期）时返回 nil，调用方据此不注册。
func newDeactivationCleanupWorker(cleanup *userworker.DeactivationCleanup) Worker {
	if cleanup == nil {
		return nil
	}
	return &deactivationCleanupWorker{cleanup: cleanup}
}

func (w *deactivationCleanupWorker) Name() string { return "user.deactivation-cleanup" }

// Kind 是 Consumer：在 HTTP 关闭**之后**才停。
//
// 清理与在飞请求无关，提前停只会让这一轮白跑；而它持有数据库连接，
// 必须在资源释放之前收干净。
func (w *deactivationCleanupWorker) Kind() WorkerKind { return KindConsumer }

// Required 为 false：清理失败不该让整个进程退出。
//
// 一次删除失败的正确处置是下一轮重试——数据还在，没有丢失风险。
// 设成 required 会让一次数据库抖动升级成一次服务不可用。
func (w *deactivationCleanupWorker) Required() bool { return false }

func (w *deactivationCleanupWorker) Lifecycle() Lifecycle { return LifecycleLongRunning }

// Prepare 无需额外资源：仓储由构造时注入，连接归 Resources 管。
func (w *deactivationCleanupWorker) Prepare(context.Context) error { return nil }

func (w *deactivationCleanupWorker) Run(ctx context.Context) error {
	return w.cleanup.Run(ctx)
}

// Cleanup 无资源可释放，幂等。
func (w *deactivationCleanupWorker) Cleanup(context.Context) error { return nil }
