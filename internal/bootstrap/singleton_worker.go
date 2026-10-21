package bootstrap

import (
	"context"
	"fmt"

	logger "github.com/gtkit/gin-api/internal/pkg/log"

	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/internal/pkg/singleton"
)

// singletonWorker 把单实例任务适配到进程编排契约。
//
// **适配层在 bootstrap 而不是 singleton 包内**：Kind / Lifecycle 的类型属于
// 本包，让通用包实现它们会把一个可复用的租约机制绑死在本仓的编排契约上。
type singletonWorker struct {
	name   string
	runner *singleton.Runner
	// client 供 Prepare 做启动期实测。
	client singleton.LeaseClient
	prefix string
}

// newSingletonWorker 构造单实例 worker。
func newSingletonWorker(
	name string, runner *singleton.Runner, client singleton.LeaseClient, prefix string,
) Worker {
	return &singletonWorker{name: name, runner: runner, client: client, prefix: prefix}
}

func (w *singletonWorker) Name() string { return w.name }

// Kind 是 Producer：关停时**最先**停。
//
// 定时触发的任务属于 producer 语义。它与在飞请求无关，提前停不会让任何
// 请求失败；而继续跑则要么在关停期间被硬取消（中途中断），要么拖住关停。
//
// 此前清理任务是 Consumer——那个判断针对的是"它持有数据库连接、必须在资源
// 释放前收干净"。那一点由 WorkerManager 的关停顺序保证（producer 停在
// 资源释放之前），不需要靠 Consumer 类别换取。
func (w *singletonWorker) Kind() WorkerKind { return KindProducer }

// Required 为 false：任务失败不该让整个进程退出。
//
// 一次执行失败的正确处置是下一轮重试。设成 required 会让一次 Redis 抖动
// 升级成一次服务不可用——而这个任务本身并不在请求路径上。
//
// 注意这与"启用时 Redis 是硬依赖"不矛盾：Prepare 里的实测失败仍会让
// **这个 worker** 起不来并留下明确错误，只是不拖垮进程。
func (w *singletonWorker) Required() bool { return false }

func (w *singletonWorker) Lifecycle() Lifecycle { return LifecycleLongRunning }

// Prepare 在启动期真跑一遍锁的全部操作。
//
// **不能只 PING**：Redis 活着不代表当前 ACL 能执行脚本，也不代表锁键与
// fence 键落在同一个集群 slot。只探连通性就宣称锁可用是虚假保证，
// 而它失效的时刻恰好是第一次真正需要锁的时候——那时通常已经有两个实例
// 在跑同一个任务了。
func (w *singletonWorker) Prepare(ctx context.Context) error {
	if err := singleton.Probe(ctx, w.client, w.prefix); err != nil {
		return fmt.Errorf("singleton worker %q cannot use its lock: %w", w.name, err)
	}
	return nil
}

func (w *singletonWorker) Run(ctx context.Context) error {
	return w.runner.Run(ctx)
}

// Cleanup 无资源可释放：租约由 Runner 在每轮结束时释放，幂等。
func (w *singletonWorker) Cleanup(context.Context) error { return nil }

// singletonObserver 把 Runner 的事件接到日志与指标。
type singletonObserver struct {
	recorder *metrics.Recorder
}

// LeaseAcquired 记 Debug：多副本下每个周期都会有一条，Info 会刷屏。
func (o singletonObserver) LeaseAcquired(task string, fence int64) {
	logger.Debug("singleton lease acquired",
		logger.String("task", task), logger.Int64("fence", fence))
}

// LeaseSkipped 同样记 Debug：**这是正常情况**。
//
// 多副本部署下每轮只有一个实例能拿到租约，其余全部跳过。把它记成 Warn
// 会让正常运行看起来像持续故障，而真正的问题会淹没在里面。
func (o singletonObserver) LeaseSkipped(task string) {
	logger.Debug("singleton lease held by another instance", logger.String("task", task))
}

// LeaseLost 记 Warn：任务被中止了，且原因通常是 Redis 出了问题。
func (o singletonObserver) LeaseLost(task string, err error) {
	logger.Warn("singleton lease lost, the task was canceled",
		logger.String("task", task), logger.Err(err))
	o.recorder.RecordWorkerRun(context.Background(), task, false)
}

// TaskSucceeded 计入指标，不记日志。
//
// 成功是常态，每轮记一条 Info 只会淹没真正需要看的那些。它作为指标的
// 分母存在——没有分母，"失败了 3 次"在跑了 3 次和 300 次时无从分辨。
func (o singletonObserver) TaskSucceeded(task string) {
	o.recorder.RecordWorkerRun(context.Background(), task, true)
}

// TaskFailed 记 Warn 并计入指标。
func (o singletonObserver) TaskFailed(task string, err error) {
	logger.Warn("singleton task failed", logger.String("task", task), logger.Err(err))
	o.recorder.RecordWorkerRun(context.Background(), task, false)
}
