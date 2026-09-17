package bootstrap

import (
	"context"
	"errors"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/internal/pkg/singleton"
)

// singletonObserver 必须始终满足 singleton.Observer。
//
// 接口由 internal/pkg/singleton 定义，实现在本包——跨包契约没有编译期绑定的话，
// 那边加一个方法这边不会有任何提示，直到装配时才发现。
var _ singleton.Observer = singletonObserver{}

// newObserverWithReader 构造一个接了真实指标管道的 observer。
//
// 用 SDK 的 ManualReader 而不是替身，理由与 internal/pkg/metrics 的测试相同：
// observer 的产出是**指标数据**，替身只能证明"某个方法被调用了"，
// 而这里要断言的恰恰是调用时传的 outcome 对不对。
func newObserverWithReader(t *testing.T) (singletonObserver, *sdkmetric.ManualReader) {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	rec, err := metrics.New(provider.Meter("github.com/gtkit/gin-api"))
	if err != nil {
		t.Fatalf("metrics.New: %v", err)
	}
	return singletonObserver{recorder: rec}, reader
}

// workerRunsByOutcome 采集一次并返回 worker.runs 按 outcome 聚合的计数。
func workerRunsByOutcome(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "worker.runs" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("worker.runs is %T, want Sum[int64]", m.Data)
			}
			for _, dp := range sum.DataPoints {
				v, _ := dp.Attributes.Value("worker.outcome")
				out[v.AsString()] += dp.Value
			}
		}
	}
	return out
}

// TestSingletonObserverRecordsSuccessAsTheDenominator 钉住成功事件必须被计入。
//
// 这是 singleton.Observer 对 TaskSucceeded 的核心论证：只报失败的话指标上没有
// 分母——"今天失败了 3 次"在跑了 3 次和跑了 300 次时是完全不同的两件事。
// 该论证此前只写在注释里，没有任何东西阻止别人把这条记录删掉"省点开销"。
func TestSingletonObserverRecordsSuccessAsTheDenominator(t *testing.T) {
	t.Parallel()

	obs, reader := newObserverWithReader(t)

	obs.TaskSucceeded("cleanup")
	obs.TaskSucceeded("cleanup")

	got := workerRunsByOutcome(t, reader)
	if got["success"] != 2 {
		t.Fatalf("success count = %d, want 2 (got %v); without a denominator "+
			"a failure count cannot be interpreted", got["success"], got)
	}
	if got["failure"] != 0 {
		t.Fatalf("failure count = %d, want 0 (got %v)", got["failure"], got)
	}
}

// TestSingletonObserverRecordsBothFailureKinds 钉住两类失败都计入 failure。
//
// LeaseLost 与 TaskFailed 是两个不同的事件，但对"这个任务今天成功了几次"
// 这个问题而言是同一件事：任务没跑完。漏掉 LeaseLost 会让 Redis 抖动期间的
// 中止在指标上不可见——而那恰恰是最需要看见的时段。
func TestSingletonObserverRecordsBothFailureKinds(t *testing.T) {
	t.Parallel()

	obs, reader := newObserverWithReader(t)
	boom := errors.New("boom")

	obs.TaskFailed("cleanup", boom)
	obs.LeaseLost("cleanup", boom)

	got := workerRunsByOutcome(t, reader)
	if got["failure"] != 2 {
		t.Fatalf("failure count = %d, want 2 (got %v); TaskFailed and LeaseLost "+
			"must both count as a run that did not complete", got["failure"], got)
	}
}

// TestSingletonObserverLeaseTrafficIsNotCounted 钉住租约事件不计入执行次数。
//
// 多副本下每一轮每个实例都会产生一次 LeaseAcquired 或 LeaseSkipped。把它们
// 计进 worker.runs 会让执行次数随副本数线性膨胀，成功率随之失真——
// 而 worker.runs 的语义是"任务执行了几次"，不是"抢了几次锁"。
func TestSingletonObserverLeaseTrafficIsNotCounted(t *testing.T) {
	t.Parallel()

	obs, reader := newObserverWithReader(t)

	obs.LeaseAcquired("cleanup", 7)
	obs.LeaseSkipped("cleanup")
	obs.LeaseSkipped("cleanup")

	if got := workerRunsByOutcome(t, reader); len(got) != 0 {
		t.Fatalf("lease events produced worker.runs data points %v; "+
			"run counts would scale with replica count instead of actual executions", got)
	}
}

// TestSingletonObserverToleratesNilRecorder 钉住"没接指标"是正常路径。
//
// SCAFFOLD.md 明确把未接指标列为正常形态（Recorder 为 nil 时记录点直接返回），
// 因此 observer 必须在 recorder 为 nil 时照常工作。若它在这里 panic，后果是
// **周期任务在运行期崩掉 worker**——那种失败要等第一轮任务跑完才出现，
// 而不是在启动时就暴露。
func TestSingletonObserverToleratesNilRecorder(t *testing.T) {
	t.Parallel()

	obs := singletonObserver{recorder: nil}
	boom := errors.New("boom")

	// 五个事件全部走一遍：任何一个漏掉 nil 判断都会在这里 panic。
	obs.LeaseAcquired("task", 1)
	obs.LeaseSkipped("task")
	obs.LeaseLost("task", boom)
	obs.TaskSucceeded("task")
	obs.TaskFailed("task", boom)
}
