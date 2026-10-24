package bootstrap

import (
	"context"

	"github.com/gtkit/logger/v2"

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

// Lifecycle 为 LifecycleLongRunning：Run 是选主循环，阻塞至 ctx 取消。
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

// schedulerObserver 把调度器的运行期事件接到进程的日志栈。
//
// 事件经回调交出来而不是由 tasks 包直接写日志：那样包会绑定到某个具体日志库。
//
// 级别的分配基于"这件事是否需要人看"：
//   - 抢到租约是状态变更，Info；
//   - **被跳过是正常情况**——多副本下每一轮只有一个实例成为 leader，
//     其余实例每次重试都会命中它。用 Debug，否则正常运行会持续刷屏，
//     而刷屏的日志等于没有日志。
//   - 失去租约与轮次失败需要关注，Warn。
func schedulerObserver() tasks.SchedulerObserver {
	return tasks.SchedulerObserver{
		LeaseAcquired: func(scheduler string, fence int64) {
			logger.Infof("task scheduler %q acquired the lease (fence=%d)", scheduler, fence)
		},
		LeaseSkipped: func(scheduler string) {
			logger.Debugf("task scheduler %q: lease held by another instance", scheduler)
		},
		LeaseLost: func(scheduler string, err error) {
			logger.Warnf("task scheduler %q lost the lease; shutdown requested: %v", scheduler, err)
		},
		RoundFailed: func(scheduler string, err error) {
			logger.Warnf("task scheduler %q round failure (shutdown may still be in progress): %v", scheduler, err)
		},
	}
}
