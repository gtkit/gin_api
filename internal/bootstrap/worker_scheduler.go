package bootstrap

import (
	"context"

	"github.com/gtkit/gin-api/internal/pkg/tasks"
)

// asynqSchedulerWorker 把周期任务调度器接进进程的 worker 管理器。
//
// adapter 留在装配层而不是让 tasks 包自己实现 Worker：那会让一个通用能力包
// 反向依赖装配层。分类（Kind / Required / Lifecycle）本来也是**部署决策**，
// 不是调度能力的属性。
type asynqSchedulerWorker struct {
	scheduler *tasks.Scheduler
}

// newAsynqSchedulerWorker 包装调度器；scheduler 为 nil 时返回 nil（不注册）。
func newAsynqSchedulerWorker(scheduler *tasks.Scheduler) Worker {
	if scheduler == nil {
		return nil
	}
	return &asynqSchedulerWorker{scheduler: scheduler}
}

func (w *asynqSchedulerWorker) Name() string { return w.scheduler.Name() }

// Kind 为 KindProducer：关停时它**最先**停。
//
// 先停生产者再停消费者，在飞的任务才能被处理完。反过来先停消费者，
// 调度器在关停期间继续投递的任务这一轮没人处理。
func (w *asynqSchedulerWorker) Kind() WorkerKind { return KindProducer }

// Required 为 false：调度器不可用时实例仍能提供同步接口。
//
// 设为 required 意味着 Redis 抖一下整个实例被摘流，而周期任务的性质
// 本就允许迟到——它的下一个触发点会照常到来。
func (w *asynqSchedulerWorker) Required() bool { return false }

// Lifecycle 为 LifecycleLongRunning：Run 持续调度，阻塞至 ctx 取消。
//
// 在此之前返回（包括返回 nil）会被管理器视为未预期退出并上报——
// 那正是需要的：一个悄悄退出的调度器意味着所有周期任务都停了，
// 而进程看起来完全正常。
func (w *asynqSchedulerWorker) Lifecycle() Lifecycle { return LifecycleLongRunning }

func (w *asynqSchedulerWorker) Prepare(ctx context.Context) error {
	return w.scheduler.Prepare(ctx)
}

func (w *asynqSchedulerWorker) Run(ctx context.Context) error {
	return w.scheduler.Run(ctx)
}

func (w *asynqSchedulerWorker) Cleanup(ctx context.Context) error {
	return w.scheduler.Cleanup(ctx)
}
