package bootstrap

import (
	"context"

	"github.com/gtkit/gin-api/internal/pkg/tasks"
)

// asynqConsumerWorker 把队列消费端接进进程的 worker 管理器。
//
// adapter 留在装配层而不是让 tasks 包自己实现 Worker：那会让一个通用能力包
// 反向依赖装配层。分类（Kind / Required / Lifecycle）本来也是**部署决策**，
// 不是队列能力的属性。
type asynqConsumerWorker struct {
	consumer *tasks.Consumer
}

// newAsynqConsumerWorker 包装消费端；consumer 为 nil 时返回 nil（不注册）。
func newAsynqConsumerWorker(consumer *tasks.Consumer) Worker {
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
