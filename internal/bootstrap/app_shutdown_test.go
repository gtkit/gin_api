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

// TestAppRunFirstSignalCancelsStartupBarrier 钉住启动屏障对关停请求的响应：
// 屏障内（required one-shot 执行中）收到首个关停请求时，屏障立即取消、
// 已 Prepare 的任务逆序清理，HTTP 从不监听，Run 按信号关停语义返回 nil。
//
// 不响应的话，信号被 signal.Notify 接管（默认的进程终止已被禁用）却不被执行，
// 操作者面对卡住的启动任务按 Ctrl+C 只能干等启动预算耗尽。
func TestAppRunFirstSignalCancelsStartupBarrier(t *testing.T) {
	testutil.RequireLoopback(t)
	server := newTestHTTPServer()

	entered := make(chan struct{})
	oneShot := &funcWorker{
		name: "blocking-repair", kind: KindBootstrap, lifecycle: LifecycleOneShot, required: true,
		run: func(ctx context.Context) error {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	app := newTestApp(t, server, oneShot)

	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(context.Background()) }()

	waitClosed(t, entered, "required one-shot did not start")
	// 等价于第一个 OS 信号：watchSignals 把信号翻译成 RequestShutdown(nil)，
	// 该翻译已有专测（TestWatchSignalsFirstSignalGracefulAndExitsOnDone）。
	app.shutdown.RequestShutdown(nil)

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned %v; a signal-canceled startup must follow the signal semantics (nil)", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after the shutdown request; the startup barrier ignored it")
	}

	select {
	case <-server.listenCalled:
		t.Fatal("http server started even though shutdown was requested during startup")
	default:
	}
	if got := oneShot.cleanupCalls.Load(); got != 1 {
		t.Fatalf("one-shot cleanup calls = %d, want exactly 1 (startup rollback)", got)
	}
}

// TestAppRunForcedShutdownDuringStartupReturnsNonZero 钉住启动期的强制路径：
// 屏障期间先后到达优雅与强制两次关停请求时，屏障取消且 Run 以
// ErrForcedShutdown 非零结束——进程是被迫结束的，不能表现成一次正常关停。
func TestAppRunForcedShutdownDuringStartupReturnsNonZero(t *testing.T) {
	testutil.RequireLoopback(t)
	server := newTestHTTPServer()

	entered := make(chan struct{})
	release := make(chan struct{})
	oneShot := &funcWorker{
		name: "blocking-repair", kind: KindBootstrap, lifecycle: LifecycleOneShot, required: true,
		run: func(ctx context.Context) error {
			close(entered)
			<-ctx.Done()
			// 拿住 Run 不返回，让第二次关停请求先于屏障退出到达，时序确定。
			<-release
			return ctx.Err()
		},
	}
	app := newTestApp(t, server, oneShot)

	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(context.Background()) }()

	waitClosed(t, entered, "required one-shot did not start")
	app.shutdown.RequestShutdown(nil)
	app.shutdown.Force()
	close(release)

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrForcedShutdown) {
			t.Fatalf("Run returned %v, want it to carry ErrForcedShutdown", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after a forced shutdown during startup")
	}

	select {
	case <-server.listenCalled:
		t.Fatal("http server started even though shutdown was forced during startup")
	default:
	}
}

// TestAppRunSkipsServeWhenShutdownRequestedDuringBarrier 钉住屏障与 serve 之间的窗口：
// 屏障成功完成但关停已被请求时，HTTP 不再开始服务——否则编排器会观察到
// 一次毫无意义的 serving 窗口（启动即关停）。
func TestAppRunSkipsServeWhenShutdownRequestedDuringBarrier(t *testing.T) {
	testutil.RequireLoopback(t)
	server := newTestHTTPServer()

	var app *App
	oneShot := &funcWorker{
		name: "requests-shutdown", kind: KindBootstrap, lifecycle: LifecycleOneShot, required: true,
		run: func(context.Context) error {
			// 关停请求发生在屏障内、one-shot 正常完成之前：屏障整体仍然成功。
			app.shutdown.RequestShutdown(nil)
			return nil
		},
	}
	app = newTestApp(t, server, oneShot)

	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(context.Background()) }()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned %v, want nil (signal semantics)", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after shutdown was requested during the barrier")
	}

	select {
	case <-server.listenCalled:
		t.Fatal("http server started even though shutdown was already requested when the barrier finished")
	default:
	}
	if got := oneShot.cleanupCalls.Load(); got != 1 {
		t.Fatalf("one-shot cleanup calls = %d, want exactly 1", got)
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

// TestLifecycleEntrypointsRejectReuse 钉住生命周期入口的一次性契约
// （runtime-state-machine spec）：App.Run 与 WorkerManager.Start 的第二次调用
// 返回可判别错误且零副作用——重复 Prepare、重复启动 goroutine 是未定义行为。
func TestLifecycleEntrypointsRejectReuse(t *testing.T) {
	t.Run("App.Run 首次未返回时的重复调用被拒绝", func(t *testing.T) {
		testutil.RequireLoopback(t)
		server := newTestHTTPServer()
		app := newTestApp(t, server)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		errCh := make(chan error, 1)
		go func() { errCh <- app.Run(ctx) }()
		waitClosed(t, server.listenCalled, "server did not start")

		if err := app.Run(ctx); !errors.Is(err, ErrLifecycleReused) {
			t.Fatalf("second Run returned %v, want ErrLifecycleReused", err)
		}

		cancel()
		if err := <-errCh; err != nil {
			t.Fatalf("first Run returned %v after a rejected reuse; the guard must be side-effect free", err)
		}
	})

	t.Run("WorkerManager.Start 重复调用被拒绝且不重复 Prepare", func(t *testing.T) {
		w := &funcWorker{name: "once", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true}
		m, err := NewWorkerManager(DefaultWorkerBudget(), w)
		if err != nil {
			t.Fatalf("NewWorkerManager: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		if err := m.Start(ctx); err != nil {
			t.Fatalf("first Start: %v", err)
		}
		if err := m.Start(ctx); !errors.Is(err, ErrLifecycleReused) {
			t.Fatalf("second Start returned %v, want ErrLifecycleReused", err)
		}
		if got := w.prepareCalls.Load(); got != 1 {
			t.Fatalf("prepare calls = %d, want exactly 1", got)
		}

		if err := m.StopAll(ctx); err != nil {
			t.Fatalf("StopAll: %v", err)
		}
	})
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
