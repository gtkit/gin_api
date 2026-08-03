package bootstrap

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestShutdownCoordinatorTriggersOnce 钉住"各阶段只执行一次"的基础：
// 无论多少个触发源并发到达，关停信号只关闭一次。
//
// 没有这一条，OS 信号、worker 异常、HTTP 异常各自直接执行关停动作时，
// 并发到达就会重复 close channel（直接 panic）、重复关闭连接、重复清理。
func TestShutdownCoordinatorTriggersOnce(t *testing.T) {
	t.Parallel()

	c := NewShutdownCoordinator()

	const concurrent = 16
	var wg sync.WaitGroup
	wg.Add(concurrent)
	for i := range concurrent {
		go func() {
			defer wg.Done()
			c.RequestShutdown(errors.New("cause " + string(rune('a'+i%26))))
		}()
	}
	wg.Wait()

	select {
	case <-c.Requested():
	default:
		t.Fatal("shutdown was requested but the signal channel is not closed")
	}
	if got := c.Phase(); got != PhaseDraining {
		t.Fatalf("phase = %s, want draining", got)
	}
}

// TestShutdownCoordinatorAggregatesAllCauses 验证并发到达的其他 cause
// 只聚合记录，不重复触发阶段——但也不能被丢掉。
//
// 丢掉后续 cause 的代价很具体：HTTP 先因端口冲突退出触发关停，
// 随后 required worker 也失败，而最终错误只报了端口冲突。
func TestShutdownCoordinatorAggregatesAllCauses(t *testing.T) {
	t.Parallel()

	c := NewShutdownCoordinator()
	first := errors.New("http server died")
	second := errors.New("worker died")

	c.RequestShutdown(first)
	c.RequestShutdown(second)

	cause := c.Cause()
	if !errors.Is(cause, first) || !errors.Is(cause, second) {
		t.Fatalf("cause = %v, want both causes aggregated", cause)
	}
}

// TestShutdownCoordinatorNilCauseIsNotAnError 验证信号触发的正常关停
// 不产生错误——否则每次 Ctrl+C 都会以非零退出。
func TestShutdownCoordinatorNilCauseIsNotAnError(t *testing.T) {
	t.Parallel()

	c := NewShutdownCoordinator()
	c.RequestShutdown(nil)

	if err := c.Cause(); err != nil {
		t.Fatalf("Cause() = %v, want nil for a plain signal shutdown", err)
	}
}

// TestShutdownCoordinatorForce 验证第二个信号进入强制路径：
// 置位 forced、关闭强制通道、并且自身也算一次关停请求
// （先收到第二个信号的情形同样要能启动关停）。
func TestShutdownCoordinatorForce(t *testing.T) {
	t.Parallel()

	c := NewShutdownCoordinator()
	c.Force()

	if !c.Forced() {
		t.Fatal("Forced() = false after Force()")
	}
	select {
	case <-c.ForcedCh():
	default:
		t.Fatal("forced channel is not closed after Force()")
	}
	select {
	case <-c.Requested():
	default:
		t.Fatal("Force() did not also request a shutdown")
	}
	if err := c.Cause(); !errors.Is(err, ErrForcedShutdown) {
		t.Fatalf("Cause() = %v, want it to carry ErrForcedShutdown", err)
	}
}

// TestShutdownCoordinatorForceIsIdempotent 验证重复 Force 不 panic
// （重复 close 通道会直接 panic，而运维连按三次 Ctrl+C 是常态）。
func TestShutdownCoordinatorForceIsIdempotent(t *testing.T) {
	t.Parallel()

	c := NewShutdownCoordinator()
	for range 5 {
		c.Force()
	}
	if !c.Forced() {
		t.Fatal("Forced() = false after repeated Force()")
	}
}

// TestShutdownCoordinatorPhaseIsOneWay 验证相位单向推进。
//
// 已进入 draining 的实例若能变回 serving，编排器会在摘流之后又把流量导回来。
func TestShutdownCoordinatorPhaseIsOneWay(t *testing.T) {
	t.Parallel()

	c := NewShutdownCoordinator()
	if got := c.Phase(); got != PhaseStarting {
		t.Fatalf("initial phase = %s, want starting", got)
	}

	c.MarkServing()
	if got := c.Phase(); got != PhaseServing {
		t.Fatalf("phase = %s, want serving", got)
	}

	c.RequestShutdown(nil)
	c.MarkServing() // 必须无效
	if got := c.Phase(); got != PhaseDraining {
		t.Fatalf("phase = %s after MarkServing during draining, want draining", got)
	}
}

// TestWaitOrForceReturnsEarlyOnForce 验证强制路径不等 LB 传播。
//
// 强制关停的前提正是"已经等不下去了"，此时还老老实实等满 drain wait
// 会让进程在故障关停中继续滞留。
func TestWaitOrForceReturnsEarlyOnForce(t *testing.T) {
	t.Parallel()

	c := NewShutdownCoordinator()
	c.Force()

	start := time.Now()
	c.WaitOrForce(3 * time.Second)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("WaitOrForce waited %s after Force(); it must return immediately", elapsed)
	}
}

// TestWaitOrForceWaitsWhenNotForced 反向证明：未强制时确实会等。
// 没有这条，上一条断言可能因为"从不等待"而恒真。
func TestWaitOrForceWaitsWhenNotForced(t *testing.T) {
	t.Parallel()

	c := NewShutdownCoordinator()

	start := time.Now()
	c.WaitOrForce(80 * time.Millisecond)
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond {
		t.Fatalf("WaitOrForce returned after %s, want it to wait the configured drain time", elapsed)
	}
}

func TestProcessPhaseString(t *testing.T) {
	t.Parallel()

	cases := map[ProcessPhase]string{
		PhaseStarting: "starting", PhaseServing: "serving", PhaseDraining: "draining",
		ProcessPhase(200): "starting",
	}
	for phase, want := range cases {
		if got := phase.String(); got != want {
			t.Errorf("ProcessPhase(%d).String() = %q, want %q", phase, got, want)
		}
	}
}

// TestProcessStatusSplitsRequiredAndOptional 验证 readiness 输入按 required 分流：
// required 失效进 FailedRequirements（→503），optional 降级进 DegradedCapabilities（→200）。
func TestProcessStatusSplitsRequiredAndOptional(t *testing.T) {
	t.Parallel()

	requiredWorker := &funcWorker{
		name: "outbox", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
	}
	optionalWorker := &funcWorker{
		name: "metrics", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: false,
	}

	mgr, err := NewWorkerManager(testWorkerBudget(), requiredWorker, optionalWorker)
	if err != nil {
		t.Fatalf("NewWorkerManager: %v", err)
	}
	mgr.markDegraded("outbox", errors.New("died"))
	mgr.markDegraded("metrics", errors.New("died"))

	coordinator := NewShutdownCoordinator()
	status := newProcessStatus(coordinator, mgr)

	failed := status.FailedRequirements()
	if len(failed) != 1 || failed[0] != "outbox" {
		t.Fatalf("FailedRequirements() = %v, want [outbox]", failed)
	}
	degraded := status.DegradedCapabilities()
	if len(degraded) != 1 || degraded[0] != "metrics" {
		t.Fatalf("DegradedCapabilities() = %v, want [metrics]", degraded)
	}

	if status.Draining() {
		t.Fatal("Draining() = true before shutdown was requested")
	}
	coordinator.RequestShutdown(nil)
	if !status.Draining() {
		t.Fatal("Draining() = false after shutdown was requested")
	}
}

// TestProcessStatusNamesAreSorted 验证列表稳定排序：
// 它进 readiness 响应体，顺序抖动会干扰比对与告警去重。
func TestProcessStatusNamesAreSorted(t *testing.T) {
	t.Parallel()

	workers := []Worker{
		&funcWorker{name: "zeta", kind: KindConsumer, lifecycle: LifecycleLongRunning},
		&funcWorker{name: "alpha", kind: KindConsumer, lifecycle: LifecycleLongRunning},
		&funcWorker{name: "mid", kind: KindConsumer, lifecycle: LifecycleLongRunning},
	}
	mgr, err := NewWorkerManager(testWorkerBudget(), workers...)
	if err != nil {
		t.Fatalf("NewWorkerManager: %v", err)
	}
	for _, w := range workers {
		mgr.markDegraded(w.Name(), errors.New("died"))
	}

	got := newProcessStatus(NewShutdownCoordinator(), mgr).DegradedCapabilities()
	if want := "alpha,mid,zeta"; strings.Join(got, ",") != want {
		t.Fatalf("DegradedCapabilities() = %v, want sorted %s", got, want)
	}
}
