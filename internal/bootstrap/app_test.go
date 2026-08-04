package bootstrap

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/testutil"

	"github.com/gtkit/tools/mailx"
	"github.com/gtkit/verify"

	"github.com/gtkit/gin-api/internal/pkg/jwtauth"
	"github.com/gtkit/gin-api/internal/platform/config"
)

type testHTTPServer struct {
	listenCalled   chan struct{}
	shutdownCalled chan struct{}
	mu             sync.Mutex
}

func newTestHTTPServer() *testHTTPServer {
	return &testHTTPServer{listenCalled: make(chan struct{}), shutdownCalled: make(chan struct{})}
}

func (s *testHTTPServer) Serve(net.Listener) error {
	close(s.listenCalled)
	<-s.shutdownCalled
	return http.ErrServerClosed
}

func (s *testHTTPServer) ServeTLS(l net.Listener, _, _ string) error { return s.Serve(l) }

func (s *testHTTPServer) Shutdown(context.Context) error {
	s.markShutdown()
	return nil
}

func (s *testHTTPServer) Close() error {
	s.markShutdown()
	return nil
}

func (s *testHTTPServer) markShutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.shutdownCalled:
	default:
		close(s.shutdownCalled)
	}
}

// funcWorker 是测试用的 Worker：类别、生命周期、required 与执行体全部可控。
type funcWorker struct {
	name      string
	kind      WorkerKind
	lifecycle Lifecycle
	required  bool
	prepare   func(context.Context) error
	run       func(context.Context) error
	cleanup   func(context.Context) error

	prepareCalls atomic.Int32
	cleanupCalls atomic.Int32
}

func (w *funcWorker) Name() string         { return w.name }
func (w *funcWorker) Kind() WorkerKind     { return w.kind }
func (w *funcWorker) Required() bool       { return w.required }
func (w *funcWorker) Lifecycle() Lifecycle { return w.lifecycle }

func (w *funcWorker) Prepare(ctx context.Context) error {
	w.prepareCalls.Add(1)
	if w.prepare != nil {
		return w.prepare(ctx)
	}
	return nil
}

func (w *funcWorker) Run(ctx context.Context) error {
	if w.run != nil {
		return w.run(ctx)
	}
	<-ctx.Done()
	return nil
}

func (w *funcWorker) Cleanup(ctx context.Context) error {
	w.cleanupCalls.Add(1)
	if w.cleanup != nil {
		return w.cleanup(ctx)
	}
	return nil
}

// newTestApp 组装一个只带 HTTP 与 worker 的最小 App。
//
// drainWait 置 0：LB 传播等待在生产上是必需的，但在测试里只会让每个用例
// 白等 5 秒——那正是测试被人加 -short 跳过的开始。
func newTestApp(t *testing.T, server httpServer, workers ...Worker) *App {
	t.Helper()

	mgr, err := NewWorkerManager(WorkerBudget{
		Prepare: time.Second, Startup: 5 * time.Second, Cleanup: time.Second,
	}, workers...)
	if err != nil {
		t.Fatalf("NewWorkerManager: %v", err)
	}

	return &App{
		// 端口留空 = 由内核分配一个空闲端口：写死端口会让并行的用例互相抢占。
		httpConfig: HTTPConfig{Host: "127.0.0.1", Port: "0", ShutdownTimeout: time.Second},
		httpServer: server,
		workers:    mgr,
		shutdown:   NewShutdownCoordinator(),
		drainWait:  0,
	}
}

// TestAppRunPhasedShutdownOrder 钉住分相关停顺序：
//
//	producer 必须在 HTTP 关闭**之前**停；consumer 必须在 HTTP 关闭**之后**才停。
//
// 两个方向都要断言。只测其一时，一个"全部 worker 都在 HTTP 之前停"或
// "全部都在之后停"的实现都能蒙混过关——而那正是本批次要消灭的单一 Worker 抽象。
func TestAppRunPhasedShutdownOrder(t *testing.T) {
	testutil.RequireLoopback(t)
	server := newTestHTTPServer()

	var producerBeforeHTTP, consumerAfterHTTP atomic.Bool
	producerStopped := make(chan struct{})
	consumerStopped := make(chan struct{})

	producer := &funcWorker{
		name: "cron", kind: KindProducer, lifecycle: LifecycleLongRunning, required: true,
		run: func(ctx context.Context) error {
			<-ctx.Done()
			select {
			case <-server.shutdownCalled:
			default:
				producerBeforeHTTP.Store(true) // HTTP 尚未关闭 → producer 先停，正确
			}
			close(producerStopped)
			return nil
		},
	}
	consumer := &funcWorker{
		name: "outbox", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
		run: func(ctx context.Context) error {
			<-ctx.Done()
			select {
			case <-server.shutdownCalled:
				consumerAfterHTTP.Store(true) // HTTP 已关闭 → consumer 后停，正确
			default:
			}
			close(consumerStopped)
			return nil
		},
	}

	app := newTestApp(t, server, producer, consumer)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	waitClosed(t, server.listenCalled, "server did not start")
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}

	waitClosed(t, producerStopped, "producer did not stop")
	waitClosed(t, consumerStopped, "consumer did not stop")

	if !producerBeforeHTTP.Load() {
		t.Fatal("producer was canceled after the HTTP server shut down; it must stop first")
	}
	if !consumerAfterHTTP.Load() {
		t.Fatal("consumer was canceled before the HTTP server shut down; it must drain afterwards")
	}
	if got := producer.cleanupCalls.Load(); got != 1 {
		t.Fatalf("producer cleanup calls = %d, want exactly 1", got)
	}
	if got := consumer.cleanupCalls.Load(); got != 1 {
		t.Fatalf("consumer cleanup calls = %d, want exactly 1", got)
	}
}

// TestAppRunWithoutWorkers 覆盖母版默认形态：worker 集合为空时启停不阻塞、不报错。
func TestAppRunWithoutWorkers(t *testing.T) {
	testutil.RequireLoopback(t)
	server := newTestHTTPServer()
	app := newTestApp(t, server)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	waitClosed(t, server.listenCalled, "server did not start")
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return with an empty worker set")
	}
}

// TestAppRunFailsFastOnRequiredWorkerError 锁定 required worker 异常的传播：
// 异常触发全进程优雅关停，Run 以带 worker 名与根因的错误退出，不做静默重启。
func TestAppRunFailsFastOnRequiredWorkerError(t *testing.T) {
	testutil.RequireLoopback(t)
	server := newTestHTTPServer()
	app := newTestApp(t, server, &funcWorker{
		name: "failing-worker", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
		run: func(context.Context) error { return errors.New("boom") },
	})

	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(context.Background()) }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Run returned nil error after a required worker failed")
		}
		for _, want := range []string{"worker failed", "failing-worker", "boom"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not contain %q", err, want)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after a required worker failed")
	}
}

// TestAppRunOptionalWorkerFailureDoesNotStopProcess 验证 optional worker 异常
// 只标降级并停止该 worker，进程继续服务。
//
// 反过来（optional 异常也拖垮进程）会让"可选"这个声明失去全部意义。
func TestAppRunOptionalWorkerFailureDoesNotStopProcess(t *testing.T) {
	testutil.RequireLoopback(t)
	server := newTestHTTPServer()
	failed := make(chan struct{})
	app := newTestApp(t, server, &funcWorker{
		name: "optional-worker", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: false,
		run: func(context.Context) error { close(failed); return errors.New("boom") },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	waitClosed(t, server.listenCalled, "server did not start")
	waitClosed(t, failed, "optional worker did not run")

	// 进程必须仍在服务：给它一点时间证明自己没有被拖垮。
	select {
	case err := <-errCh:
		t.Fatalf("Run returned %v after an optional worker failed; the process must keep serving", err)
	case <-time.After(150 * time.Millisecond):
	}

	if _, ok := app.workers.Degraded()["optional-worker"]; !ok {
		t.Fatal("optional worker failure was not recorded as degraded")
	}

	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

// TestAppRunForcedShutdownReturnsNonZero 验证强制关停路径：
// 第二个信号取消剩余等待、直接 Close，并以非零（非 nil）错误退出——
// 进程是被迫结束的，不能表现成一次正常关停。
func TestAppRunForcedShutdownReturnsNonZero(t *testing.T) {
	testutil.RequireLoopback(t)
	server := newTestHTTPServer()
	app := newTestApp(t, server)

	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(context.Background()) }()

	waitClosed(t, server.listenCalled, "server did not start")
	app.shutdown.Force()

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrForcedShutdown) {
			t.Fatalf("Run returned %v, want it to carry ErrForcedShutdown", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after a forced shutdown")
	}
}

// TestAppRunRequiredOneShotBlocksHTTP 验证 required OneShot 是 HTTP 的前置屏障：
// 它必须执行完成（并清理）之后 HTTP 才开始监听。
//
// 放到 HTTP 之后就失去了全部意义——启动期数据修复的整个前提是
// "对外服务之前先把它做完"。
func TestAppRunRequiredOneShotBlocksHTTP(t *testing.T) {
	testutil.RequireLoopback(t)
	server := newTestHTTPServer()
	release := make(chan struct{})
	var listenedBeforeOneShot atomic.Bool

	oneShot := &funcWorker{
		name: "startup-repair", kind: KindBootstrap, lifecycle: LifecycleOneShot, required: true,
		run: func(context.Context) error {
			select {
			case <-server.listenCalled:
				listenedBeforeOneShot.Store(true)
			default:
			}
			<-release
			return nil
		},
	}

	app := newTestApp(t, server, oneShot)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	// one-shot 未完成期间 HTTP 不得监听。
	select {
	case <-server.listenCalled:
		t.Fatal("http server started before the required one-shot worker finished")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	waitClosed(t, server.listenCalled, "server did not start after the one-shot worker finished")

	if listenedBeforeOneShot.Load() {
		t.Fatal("http listener was already open when the one-shot worker ran")
	}
	if got := oneShot.cleanupCalls.Load(); got != 1 {
		t.Fatalf("one-shot cleanup calls = %d, want exactly 1 (cleaned right after Run)", got)
	}

	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

// TestAppRunRequiredPrepareFailureNeverStartsHTTP 验证 Prepare 失败即启动失败，
// 且 HTTP 从不监听、已 Prepare 的 worker 被逆序清理。
func TestAppRunRequiredPrepareFailureNeverStartsHTTP(t *testing.T) {
	testutil.RequireLoopback(t)
	server := newTestHTTPServer()

	ok := &funcWorker{
		name: "ok-worker", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
	}
	bad := &funcWorker{
		name: "bad-worker", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
		prepare: func(context.Context) error { return errors.New("prepare failed") },
	}

	app := newTestApp(t, server, ok, bad)

	err := app.Run(context.Background())
	if err == nil {
		t.Fatal("Run returned nil after a required worker failed to prepare")
	}
	if !strings.Contains(err.Error(), "bad-worker") {
		t.Fatalf("error %q does not name the failing worker", err)
	}

	select {
	case <-server.listenCalled:
		t.Fatal("http server started even though a required worker failed to prepare")
	default:
	}

	if got := ok.cleanupCalls.Load(); got != 1 {
		t.Fatalf("prepared worker cleanup calls = %d, want exactly 1 on startup rollback", got)
	}
	if got := bad.cleanupCalls.Load(); got != 0 {
		t.Fatalf("failed-prepare worker cleanup calls = %d, want 0 (it never entered the prepared set)", got)
	}
}

// waitClosed 等待通道关闭，超时即失败。
func waitClosed(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal(msg)
	}
}

func TestInitProcessRuntimeAppliesConfiguredTimezone(t *testing.T) {
	originalLocal := time.Local
	originalInitLog := initLogWithConfig
	originalBuildMail := buildMailClient
	originalInitJWT := initJWTWithConfig
	originalVerify := initVerify
	originalJSONCheck := checkJSONRuntime
	defer func() {
		time.Local = originalLocal
		initLogWithConfig = originalInitLog
		buildMailClient = originalBuildMail
		initJWTWithConfig = originalInitJWT
		initVerify = originalVerify
		checkJSONRuntime = originalJSONCheck
	}()

	initLogWithConfig = func(*config.Config) {}
	buildMailClient = func(*config.Config) (*mailx.Client, error) { return nil, nil }
	initJWTWithConfig = func(*config.Config) (*jwtauth.Assembly, error) { return nil, nil }
	initVerify = func(...verify.Option) error { return nil }
	checkJSONRuntime = func() {}

	if err := initProcessRuntime(&config.Config{
		Env: "test",
		App: config.AppConfig{Timezone: "Asia/Shanghai"},
	}); err != nil {
		t.Fatalf("initProcessRuntime returned error: %v", err)
	}
	if got := time.Local.String(); got != "Asia/Shanghai" {
		t.Fatalf("time.Local = %q, want Asia/Shanghai", got)
	}
}
