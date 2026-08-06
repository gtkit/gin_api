package bootstrap

import (
	"context"
	"errors"
	"testing"
	"time"
)

// eventually 轮询直到条件为真，超时即失败。
func eventually(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

// TestStopKindStuckWorkerReportsNotExited 钉住卡死任务的停止语义：
// 预算内未退出 → StopKind 返回可 errors.Is 判别的 ErrWorkerNotExited，
// 任务进入 LiveWorkers 集合且 Cleanup 不与仍在执行的 Run 并发；
// 它真正退出后由 supervise 清理恰好一次，并离开 LiveWorkers。
func TestStopKindStuckWorkerReportsNotExited(t *testing.T) {
	release := make(chan struct{})
	stuck := &funcWorker{
		name: "stuck", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
		// 有意忽略 ctx：模拟不响应取消的任务。
		run: func(context.Context) error { <-release; return nil },
	}

	mgr, err := NewWorkerManager(testWorkerBudget(), stuck)
	if err != nil {
		t.Fatalf("NewWorkerManager: %v", err)
	}
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := mgr.StopKind(stopCtx, KindConsumer); !errors.Is(err, ErrWorkerNotExited) {
		t.Fatalf("StopKind error = %v, want it to carry ErrWorkerNotExited", err)
	}

	if live := mgr.LiveWorkers(); len(live) != 1 || live[0] != "stuck" {
		t.Fatalf("LiveWorkers = %v, want [stuck]", live)
	}
	if got := stuck.cleanupCalls.Load(); got != 0 {
		t.Fatalf("cleanup calls while Run is still executing = %d, want 0", got)
	}

	close(release)
	eventually(t, 2*time.Second, func() bool { return stuck.cleanupCalls.Load() == 1 },
		"late-exiting worker was not cleaned up exactly once by its supervisor")
	eventually(t, 2*time.Second, func() bool { return len(mgr.LiveWorkers()) == 0 },
		"worker stayed in LiveWorkers after it exited")
}

// TestCleanupRemainingSkipsRunningWorker 钉住兜底清理的边界：
// 只清理 Run 已返回的条目；卡死条目被跳过（Cleanup 计数为 0），
// 其晚退出后仍被 supervise 清理恰好一次，重复兜底不重复清理。
func TestCleanupRemainingSkipsRunningWorker(t *testing.T) {
	release := make(chan struct{})
	stuck := &funcWorker{
		name: "stuck", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
		run: func(context.Context) error { <-release; return nil },
	}
	polite := &funcWorker{
		name: "polite", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
		// 默认 run：<-ctx.Done() 后返回，即"响应取消"的任务。
	}

	mgr, err := NewWorkerManager(testWorkerBudget(), stuck, polite)
	if err != nil {
		t.Fatalf("NewWorkerManager: %v", err)
	}
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := mgr.StopKind(stopCtx, KindConsumer); !errors.Is(err, ErrWorkerNotExited) {
		t.Fatalf("StopKind error = %v, want it to carry ErrWorkerNotExited", err)
	}

	// 响应取消的任务由 supervise 正常清理。
	eventually(t, 2*time.Second, func() bool { return polite.cleanupCalls.Load() == 1 },
		"polite worker was not cleaned up after stopping")

	if err := mgr.CleanupRemaining(context.Background()); err != nil {
		t.Fatalf("CleanupRemaining: %v", err)
	}
	if got := stuck.cleanupCalls.Load(); got != 0 {
		t.Fatalf("fallback cleanup ran concurrently with Run: cleanup calls = %d, want 0", got)
	}
	if got := polite.cleanupCalls.Load(); got != 1 {
		t.Fatalf("polite worker cleanup calls = %d, want exactly 1", got)
	}

	close(release)
	eventually(t, 2*time.Second, func() bool { return stuck.cleanupCalls.Load() == 1 },
		"late-exiting worker was not cleaned up exactly once")

	// 再次兜底：对已清理条目不重复执行。
	if err := mgr.CleanupRemaining(context.Background()); err != nil {
		t.Fatalf("second CleanupRemaining: %v", err)
	}
	if got := stuck.cleanupCalls.Load(); got != 1 {
		t.Fatalf("stuck worker cleanup calls after second fallback = %d, want 1", got)
	}
	if got := polite.cleanupCalls.Load(); got != 1 {
		t.Fatalf("polite worker cleanup calls after second fallback = %d, want 1", got)
	}
}
