// Package auditlog 提供写操作审计的异步记录器。
//
// 语义是**尽力而为**：入队非阻塞，队列满载丢弃并计数告警；落库故障
// 整批丢弃不重试。审计故障绝不放大成业务故障——零丢失审计需要同步写
// 或 WAL，代价是把数据库可用性并进每个写请求的关键路径，那是另一种产品。
//
// 母版不构造实例：派生项目在装配处构造 Recorder、把 Run 注册为
// consumer worker、在写操作路由组挂 middleware.OperationAudit，
// 见 SCAFFOLD.md「注册位」。
package auditlog

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	applog "github.com/gtkit/gin-api/internal/pkg/log"

	"github.com/gtkit/gin-api/internal/models"
)

// Sink 承接批量落库，由 internal/repository/auditlog 实现、装配处连线。
// 实现不得在返回后保留切片（记录器会复用/释放它）。
type Sink interface {
	WriteBatch(ctx context.Context, records []models.OperationAudit) error
}

// Options 是构造参数。零值字段取默认值；越界值构造报错而不是静默修正。
type Options struct {
	// QueueSize 是有界队列容量，默认 1024。
	QueueSize int
	// BatchSize 是落库批大小，默认 100，攒满立即写。
	BatchSize int
	// FlushInterval 是不足一批时的最长等待，默认 2 秒。
	FlushInterval time.Duration
	// DrainBudget 是运行 ctx 取消后排空存量记录的独立预算，默认 5 秒。
	DrainBudget time.Duration
}

const (
	defaultQueueSize     = 1024
	defaultBatchSize     = 100
	defaultFlushInterval = 2 * time.Second
	defaultDrainBudget   = 5 * time.Second
)

// Recorder 异步收集审计记录并批量交给 Sink。并发安全。
type Recorder struct {
	sink Sink
	opts Options
	ch   chan models.OperationAudit
	// dropped 是累计丢弃数（队列满载 + 落库失败批），reported 是已告警过的水位。
	dropped  atomic.Int64
	reported atomic.Int64
}

// New 构造记录器。必需依赖与参数边界在装配期校验。
func New(sink Sink, opts Options) (*Recorder, error) {
	if sink == nil {
		return nil, errors.New("auditlog: sink is required")
	}
	if opts.QueueSize < 0 || opts.BatchSize < 0 || opts.FlushInterval < 0 || opts.DrainBudget < 0 {
		return nil, fmt.Errorf("auditlog: options must not be negative: %+v", opts)
	}
	if opts.QueueSize == 0 {
		opts.QueueSize = defaultQueueSize
	}
	if opts.BatchSize == 0 {
		opts.BatchSize = defaultBatchSize
	}
	if opts.BatchSize > opts.QueueSize {
		return nil, fmt.Errorf("auditlog: batch size %d must not exceed queue size %d",
			opts.BatchSize, opts.QueueSize)
	}
	if opts.FlushInterval == 0 {
		opts.FlushInterval = defaultFlushInterval
	}
	if opts.DrainBudget == 0 {
		opts.DrainBudget = defaultDrainBudget
	}
	return &Recorder{
		sink: sink,
		opts: opts,
		ch:   make(chan models.OperationAudit, opts.QueueSize),
	}, nil
}

// Enqueue 非阻塞投递一条记录；队列满载返回 false 并计数（由 Run 节流告警）。
// 绝不阻塞调用方——这是审计不放大业务故障的第一道保证。
func (r *Recorder) Enqueue(record models.OperationAudit) bool {
	select {
	case r.ch <- record:
		return true
	default:
		r.dropped.Add(1)
		return false
	}
}

// Dropped 返回累计丢弃数（满载 + 落库失败）。
func (r *Recorder) Dropped() int64 {
	return r.dropped.Load()
}

// Run 是落库循环：攒满批或到期即写。ctx 取消后以 DrainBudget 独立预算
// 排空存量记录再返回——关停语义与 consumer worker 对齐（HTTP 停止后
// 不再有新记录入队，排空只处理存量）。恒返回 nil：审计的一切故障
// 都已降级为丢弃计数，不该让进程编排把它当成 worker 失败。
func (r *Recorder) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.opts.FlushInterval)
	defer ticker.Stop()

	batch := make([]models.OperationAudit, 0, r.opts.BatchSize)
	for {
		select {
		case <-ctx.Done():
			r.drain(ctx, batch)
			return nil
		case record := <-r.ch:
			batch = append(batch, record)
			if len(batch) >= r.opts.BatchSize {
				r.flush(ctx, batch)
				batch = make([]models.OperationAudit, 0, r.opts.BatchSize)
			}
		case <-ticker.C:
			if len(batch) > 0 {
				r.flush(ctx, batch)
				batch = make([]models.OperationAudit, 0, r.opts.BatchSize)
			}
			r.reportDrops()
		}
	}
}

// drain 在独立预算内排空队列与未写出的批。预算 ctx 剥离运行 ctx 的取消
// （运行 ctx 已经取消了），只保留其 value 链。
func (r *Recorder) drain(ctx context.Context, batch []models.OperationAudit) {
	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.opts.DrainBudget)
	defer cancel()

	for {
		select {
		case record := <-r.ch:
			batch = append(batch, record)
			if len(batch) >= r.opts.BatchSize {
				r.flush(drainCtx, batch)
				batch = make([]models.OperationAudit, 0, r.opts.BatchSize)
			}
		default:
			if len(batch) > 0 {
				r.flush(drainCtx, batch)
			}
			r.reportDrops()
			return
		}
		if drainCtx.Err() != nil {
			// 预算耗尽：剩余记录随进程丢弃（尽力而为语义），计入丢弃数。
			r.dropped.Add(int64(len(batch) + len(r.ch)))
			r.reportDrops()
			return
		}
	}
}

// flush 写出一批。失败整批丢弃并计数——审计写库故障时无界重试
// 会把内存队列变成不设限的堆积面；数据库恢复后新记录照常落库。
func (r *Recorder) flush(ctx context.Context, batch []models.OperationAudit) {
	if err := r.sink.WriteBatch(ctx, batch); err != nil {
		r.dropped.Add(int64(len(batch)))
		applog.Warnf("auditlog: drop batch of %d records on sink failure: %v", len(batch), err)
	}
}

// reportDrops 按 flush 节奏把丢弃增量告警一次——丢弃必须可见，
// 但不能每条丢弃刷一行日志。
func (r *Recorder) reportDrops() {
	current := r.dropped.Load()
	last := r.reported.Load()
	if current > last && r.reported.CompareAndSwap(last, current) {
		applog.Warnf("auditlog: %d audit records dropped so far (+%d since last report)",
			current, current-last)
	}
}
