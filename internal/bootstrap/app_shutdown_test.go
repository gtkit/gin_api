package bootstrap

import (
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/testutil"
)

// TestWatchSignalsFirstSignalGracefulAndExitsOnDone 钉住单信号路径：
// 第一个信号请求优雅关停（不强制），done 关闭后监听协程退出、不残留。
func TestWatchSignalsFirstSignalGracefulAndExitsOnDone(t *testing.T) {
	app := &App{shutdown: NewShutdownCoordinator()}
	signals := make(chan os.Signal, 2)
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() { app.watchSignals(signals, done); close(exited) }()

	signals <- syscall.SIGINT
	select {
	case <-app.shutdown.Requested():
	case <-time.After(2 * time.Second):
		t.Fatal("first signal did not request a graceful shutdown")
	}
	if app.shutdown.Forced() {
		t.Fatal("first signal must not force shutdown")
	}

	close(done)
	waitClosed(t, exited, "watchSignals did not exit after done was closed")
}

// TestWatchSignalsSecondSignalForces 钉住双信号路径：
// 第二个信号进入强制关停，监听协程自行返回。
func TestWatchSignalsSecondSignalForces(t *testing.T) {
	app := &App{shutdown: NewShutdownCoordinator()}
	signals := make(chan os.Signal, 2)
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() { app.watchSignals(signals, done); close(exited) }()

	signals <- syscall.SIGINT
	signals <- syscall.SIGTERM
	waitClosed(t, exited, "watchSignals did not return after the second signal")
	if !app.shutdown.Forced() {
		t.Fatal("second signal did not force shutdown")
	}
}

// TestAppRunForcedShutdownCancelsWorkerStopWait 钉住强制路径对任务停止等待的取消：
// 卡死的 consumer 让优雅关停停在 consumer 相位（停止预算 30 秒），
// 强制关停必须立即结束等待——不取消的话本测试会在 3 秒超时上失败。
// Run 的返回错误同时携带 ErrForcedShutdown 与 ErrWorkerNotExited 并指名任务；
// 清理不与仍在执行的 Run 并发；任务晚退出后仍被清理恰好一次。
func TestAppRunForcedShutdownCancelsWorkerStopWait(t *testing.T) {
	testutil.RequireLoopback(t)
	server := newTestHTTPServer()

	stopRequested := make(chan struct{})
	release := make(chan struct{})
	stuck := &funcWorker{
		name: "stuck", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
		run: func(ctx context.Context) error {
			<-ctx.Done()
			close(stopRequested)
			<-release
			return nil
		},
	}
	app := newTestApp(t, server, stuck)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	waitClosed(t, server.listenCalled, "server did not start")
	cancel()
	waitClosed(t, stopRequested, "consumer stop was not requested")
	app.shutdown.Force()

	var err error
	select {
	case err = <-errCh:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return promptly after forced shutdown; the worker stop wait was not canceled")
	}
	if !errors.Is(err, ErrForcedShutdown) {
		t.Fatalf("Run error %v does not carry ErrForcedShutdown", err)
	}
	if !errors.Is(err, ErrWorkerNotExited) {
		t.Fatalf("Run error %v does not carry ErrWorkerNotExited", err)
	}
	if !strings.Contains(err.Error(), "stuck") {
		t.Fatalf("error %q does not name the stuck worker", err)
	}
	if got := stuck.cleanupCalls.Load(); got != 0 {
		t.Fatalf("cleanup calls while Run is still executing = %d, want 0", got)
	}

	close(release)
	eventually(t, 2*time.Second, func() bool { return stuck.cleanupCalls.Load() == 1 },
		"late-exiting worker was not cleaned up exactly once")
}

// TestCloseResourcesSkipsWhileWorkerAlive 钉住资源关闭的存活判定：
// 存在未退出任务时跳过关闭并返回 ErrWorkerNotExited；任务退出后正常关闭。
func TestCloseResourcesSkipsWhileWorkerAlive(t *testing.T) {
	release := make(chan struct{})
	stuck := &funcWorker{
		name: "stuck", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
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

	app := &App{workers: mgr, runtime: &Runtime{}, shutdown: NewShutdownCoordinator()}
	if err := app.closeResources(); !errors.Is(err, ErrWorkerNotExited) {
		t.Fatalf("closeResources = %v, want it to carry ErrWorkerNotExited", err)
	}
	if app.runtime.closed.Load() {
		t.Fatal("runtime was closed while a worker was still running")
	}

	close(release)
	eventually(t, 2*time.Second, func() bool { return len(mgr.LiveWorkers()) == 0 },
		"worker stayed in LiveWorkers after it exited")
	if err := app.closeResources(); err != nil {
		t.Fatalf("closeResources after workers exited: %v", err)
	}
	if !app.runtime.closed.Load() {
		t.Fatal("runtime was not closed after all workers exited")
	}
}

// TestRuntimeCloseIsNilSafeAndRepeatable 钉住 Close 的边界语义：
// nil 接收者与零值 Runtime 均安全，重复调用返回与首次相同的结果。
func TestRuntimeCloseIsNilSafeAndRepeatable(t *testing.T) {
	var nilRuntime *Runtime
	if err := nilRuntime.Close(); err != nil {
		t.Fatalf("nil runtime Close: %v", err)
	}

	r := &Runtime{}
	if err := r.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
