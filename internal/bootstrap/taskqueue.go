package bootstrap

// TASKQUEUE: 可选任务队列的全部适配与装配代码。
// 默认保留为注释，主程序不会导入 internal/optional/taskqueue。
// 启用时删除下面包围代码的 /* 和 */，并完成 internal/optional/taskqueue/README.md 的接线清单。

/*
import (
	"context"
	"fmt"

	"github.com/gtkit/gin-api/internal/optional/taskqueue"
)

// asynqConsumerWorker 把队列消费端接进进程的 worker 管理器。
//
// adapter 留在装配层而不是让 taskqueue 包自己实现 Worker：那会让一个通用能力包
// 反向依赖装配层。分类（Kind / Required / Lifecycle）本来也是**部署决策**，
// 不是队列能力的属性。
type asynqConsumerWorker struct {
	consumer *taskqueue.Consumer
}

// newAsynqConsumerWorker 包装消费端；consumer 为 nil 时返回 nil（不注册）。
func newAsynqConsumerWorker(consumer *taskqueue.Consumer) Worker {
	if consumer == nil {
		return nil
	}
	return &asynqConsumerWorker{consumer: consumer}
}

func (w *asynqConsumerWorker) Name() string { return w.consumer.Name() }

// Kind 为 KindConsumer：关停时它在 HTTP 之后 drain。
//
// 顺序是实质的：HTTP 还在收请求时就停掉消费者，那些请求入队的任务这一轮
// 没人处理；反过来先停 HTTP，在飞请求入队的任务仍能被这一批消费者取走。
func (w *asynqConsumerWorker) Kind() WorkerKind { return KindConsumer }

// Required 为 false：队列不可用时实例仍能提供同步接口。
//
// 把它设为 required 意味着 Redis 抖一下整个实例被摘流——而队列本身
// 有重试与积压，那正是它用来吸收这类抖动的。
func (w *asynqConsumerWorker) Required() bool { return false }

func (w *asynqConsumerWorker) Lifecycle() Lifecycle { return LifecycleLongRunning }

func (w *asynqConsumerWorker) Prepare(ctx context.Context) error {
	return w.consumer.Prepare(ctx)
}

func (w *asynqConsumerWorker) Run(ctx context.Context) error {
	return w.consumer.Run(ctx)
}

func (w *asynqConsumerWorker) Cleanup(ctx context.Context) error {
	return w.consumer.Cleanup(ctx)
}

// asynqSchedulerWorker 把周期任务调度器接进进程的 worker 管理器。
//
// adapter 留在装配层而不是让 taskqueue 包自己实现 Worker：那会让一个通用能力包
// 反向依赖装配层。分类（Kind / Required / Lifecycle）本来也是**部署决策**，
// 不是调度能力的属性。
type asynqSchedulerWorker struct {
	scheduler *taskqueue.Scheduler
}

// newAsynqSchedulerWorker 包装调度器；scheduler 为 nil 时返回 nil（不注册）。
func newAsynqSchedulerWorker(scheduler *taskqueue.Scheduler) Worker {
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

func buildTaskQueueProducer(inf *infra, cleanup *providerCleanup) error {
	producer, err := taskqueue.NewProducer(inf.cfg.Tasks, inf.resources.Rdb(inf.cfg.Tasks.RedisDB))
	if err != nil {
		return fmt.Errorf("build task producer: %w", err)
	}
	inf.tasksProducer = producer
	cleanup.push("task producer", cleanupExtensions, producer.Shutdown)
	return nil
}

func buildTaskQueueWorkers(inf *infra) ([]Worker, error) {
	var workers []Worker

	// 在 NewObservedConsumer 的末尾追加 taskqueue.Registrar，注册实际业务处理器。
	// 没有处理器时不启动消费者。
	if consumer := newAsynqConsumerWorker(taskqueue.NewObservedConsumer(
		inf.cfg.Tasks, inf.resources.Rdb(inf.cfg.Tasks.RedisDB), "asynq-consumer", inf.recorder,
	)); consumer != nil {
		workers = append(workers, consumer)
	}

	// 在 NewScheduler 的末尾追加 taskqueue.PeriodicTask；不需要周期任务时保持为空。
	scheduler, err := taskqueue.NewScheduler(
		inf.cfg.Tasks, inf.resources.Rdb(inf.cfg.Tasks.RedisDB), "asynq-scheduler",
	)
	if err != nil {
		return nil, fmt.Errorf("build task scheduler: %w", err)
	}
	if worker := newAsynqSchedulerWorker(scheduler); worker != nil {
		workers = append(workers, worker)
	}
	return workers, nil
}
*/
