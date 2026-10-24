package bootstrap

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gtkit/gin-api/internal/testutil"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"github.com/gtkit/verify"

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

func TestNewAppTransfersCleanupWithoutOwningRuntimeResources(t *testing.T) {
	f := newLifecycleFixture(t)
	cfg := f.components[0].cfg
	cfg.Env = "test"
	cfg.App.Name = "provider-cleanup-test"
	cfg.Auth.Mode = config.AuthModeExternal
	cfg.RateLimit = config.RateLimitConfig{KeyHMACSecret: strings.Repeat("x", 32), Policies: []config.RateLimitPolicyConfig{
		{ID: "default.ip", Scope: "ip", Store: "local", Rate: 10, Burst: 10, MaxKeys: 100, FailureMode: "fail_open"},
		{ID: "default.user", Scope: "user", Store: "local", Rate: 10, Burst: 10, MaxKeys: 100, FailureMode: "fail_open"},
	}}
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	f.components[2].db, f.components[2].sql = db, sqlDB
	f.components[2].stop = func(context.Context) error { return sqlDB.Close() }
	mock.ExpectClose()
	startupCtx, cancelStartup := context.WithCancel(t.Context())
	defer cancelStartup()
	app, err := NewApp(startupCtx, f.options())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, finish := cleanupContext(context.WithoutCancel(t.Context()), nil)
		defer finish()
		if err := app.closeExtensions(ctx); err != nil {
			t.Error(err)
		}
		if err := app.closeResources(ctx); err != nil {
			t.Error(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	if app.cleanup == nil {
		t.Fatal("provider cleanup ownership was not transferred to App")
	}
	var names []string
	for _, step := range app.cleanup.steps {
		names = append(names, step.name)
	}
	want := []string{"telemetry", "db pool metrics", "rate limits"}
	if _, wired := reflect.TypeFor[infra]().FieldByName("outbound"); wired {
		want = append(want, "outbound http clients")
	}
	// 队列接线可按 README 手动恢复；无论哪种状态，都检查完整清理清单。
	if _, wired := reflect.TypeFor[config.Config]().FieldByName("Tasks"); wired {
		want = append(want, "task producer")
	}
	if _, wired := reflect.TypeFor[infra]().FieldByName("uploader"); wired {
		want = append(want, "local uploader")
	}
	if !slices.Equal(names, want) {
		t.Fatalf("registered provider resources = %v, want %v", names, want)
	}
	if app.runtime.Cache() != f.components[4] {
		t.Fatal("App did not retain Runtime's borrowed cache view")
	}
	cancelStartup()
	type key struct{}
	closeCtx := context.WithValue(t.Context(), key{}, "shutdown")
	probeCalls := 0
	app.cleanup.push("context probe", cleanupExtensions, func(ctx context.Context) error {
		probeCalls++
		if ctx != closeCtx || ctx.Err() != nil || ctx.Value(key{}) != "shutdown" {
			t.Error("cleanup captured the canceled startup context")
		}
		return nil
	})
	if err := app.closeExtensions(closeCtx); err != nil {
		t.Fatal(err)
	}
	if probeCalls != 1 || len(app.cleanup.steps) != 0 {
		t.Fatalf("provider cleanup calls=%d remaining=%d", probeCalls, len(app.cleanup.steps))
	}
	f.assertStopped(t)
	for range 2 {
		if err := app.closeResources(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	f.assertStopped(t, "cache", "redis", "database", "logging")
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
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run after optional failure and cancellation: %v", err)
		}
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
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run after cancellation: %v", err)
		}
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

// TestAppRunReturnsWorkerCleanupErrors 钉住终止错误的收割（runtime-state-machine spec）：
// 关停期间任务的 Cleanup 失败必须进入 Run 的返回值——required 与 optional 一视同仁。
// 清理失败意味着进程退出时存在未释放的资源，带着失败以零退出码结束会让
// 编排器把一次异常退出当成正常关停。
func TestAppRunReturnsWorkerCleanupErrors(t *testing.T) {
	testutil.RequireLoopback(t)

	tests := []struct {
		name     string
		required bool
	}{
		{name: "required 任务的清理失败进入返回值", required: true},
		{name: "optional 任务的清理失败进入返回值", required: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestHTTPServer()
			app := newTestApp(t, server, &funcWorker{
				name: "cleanup-fails", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: tt.required,
				cleanup: func(context.Context) error { return errors.New("cleanup boom") },
			})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			errCh := make(chan error, 1)
			go func() { errCh <- app.Run(ctx) }()

			waitClosed(t, server.listenCalled, "server did not start")
			cancel()

			select {
			case err := <-errCh:
				if err == nil {
					t.Fatal("Run returned nil although a worker cleanup failed during shutdown")
				}
				for _, want := range []string{"cleanup-fails", "cleanup boom"} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("error %q does not contain %q", err, want)
					}
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Run did not return after cancellation")
			}
		})
	}
}

// TestAppReleasesExtensionsOnEveryExitPath 钉住 App 扩展产物的释放
// （bootstrap-module-assembly spec）：NewApp 成功后所有权移交 App 的限流注册表
// 与指标注销回调，必须在 Run 的任意返回路径上恰好释放一次——启动失败的
// 提前返回若只关连接资源，限流器的清理 goroutine 与 OTel 回调就地泄漏。
func TestAppReleasesExtensionsOnEveryExitPath(t *testing.T) {
	testutil.RequireLoopback(t)

	tests := []struct {
		name    string
		workers []Worker
		// driveShutdown 非 nil 时表示正常路径：等服务起来后触发关停。
		driveShutdown bool
	}{
		{
			name: "启动失败路径释放扩展产物",
			workers: []Worker{&funcWorker{
				name: "bad-prepare", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
				prepare: func(context.Context) error { return errors.New("prepare failed") },
			}},
		},
		{
			name:          "正常关停路径恰好释放一次",
			driveShutdown: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestHTTPServer()
			app := newTestApp(t, server, tt.workers...)

			var calls []string
			app.cleanup = newRecordingProviderCleanup(func(ctx context.Context, name string) error {
				if ctx.Err() != nil {
					t.Errorf("cleanup inherited canceled startup context: %v", ctx.Err())
				}
				calls = append(calls, name)
				return nil
			})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			errCh := make(chan error, 1)
			go func() { errCh <- app.Run(ctx) }()

			if tt.driveShutdown {
				waitClosed(t, server.listenCalled, "server did not start")
				cancel()
			}

			select {
			case err := <-errCh:
				if (err == nil) != tt.driveShutdown {
					t.Fatalf("Run error = %v, expected normal shutdown = %v", err, tt.driveShutdown)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Run did not return")
			}

			want := []string{"rate limits", "outbound http clients", "task producer", "telemetry", "db pool metrics", "worker metrics"}
			if !slices.Equal(calls, want) {
				t.Fatalf("extension cleanup order = %v, want %v", calls, want)
			}
		})
	}
}

// TestAppRunDoesNotDuplicateShutdownCause 钉住终止错误收割的去重：
// 触发关停的那个错误已作为 cause 记录，末端收割不得再次计入。
func TestAppRunDoesNotDuplicateShutdownCause(t *testing.T) {
	testutil.RequireLoopback(t)
	server := newTestHTTPServer()
	app := newTestApp(t, server, &funcWorker{
		name: "failing-worker", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
		run: func(context.Context) error { return errors.New("uniquely-marked-boom") },
	})

	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(context.Background()) }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Run returned nil after a required worker failed")
		}
		if got := strings.Count(err.Error(), "uniquely-marked-boom"); got != 1 {
			t.Fatalf("shutdown cause appears %d times in %q, want exactly 1", got, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after a required worker failed")
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
	// time.Local is process-global; a prior HTTP test may still be finishing a
	// connection goroutine. Test startup's global mutation in a fresh process.
	const childEnv = "GIN_API_TEST_TIMEZONE_PROCESS"
	if os.Getenv(childEnv) != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, executable, "-test.run=^TestInitProcessRuntimeAppliesConfiguredTimezone$", "-test.count=1", "-test.v")
		command.Env = append(os.Environ(), childEnv+"=1")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated timezone test: %v\n%s", err, output)
		}
		if !strings.Contains(string(output), "--- PASS: TestInitProcessRuntimeAppliesConfiguredTimezone") {
			t.Fatalf("isolated process did not execute the timezone assertion:\n%s", output)
		}
		t.Log("isolated timezone initialization passed")
		return
	}

	originalLocal := time.Local
	originalVerify := initVerify
	originalJSONCheck := checkJSONRuntime
	defer func() {
		time.Local = originalLocal
		initVerify = originalVerify
		checkJSONRuntime = originalJSONCheck
	}()

	initVerify = func(...verify.Option) error { return nil }
	checkJSONRuntime = func() {}

	ApplyRuntimeTimezone(&config.Config{App: config.AppConfig{Timezone: "Asia/Shanghai"}})
	if got := time.Local.String(); got != "Asia/Shanghai" {
		t.Fatalf("time.Local = %q, want Asia/Shanghai", got)
	}
}

// TestInitProcessRuntimeFlushesLogsWhenLaterStepsFail verifies the outer Runtime
// failure path, including the selected logging owner and original validator error.
func TestInitProcessRuntimeFlushesLogsWhenLaterStepsFail(t *testing.T) {
	originalVerify := initVerify
	t.Cleanup(func() { initVerify = originalVerify })
	failure := errors.New("validator exploded")
	initVerify = func(...verify.Option) error { return failure }
	fixture := newLifecycleFixture(t)
	if _, err := NewRuntime(t.Context(), fixture.options()); !errors.Is(err, failure) {
		t.Fatalf("validator failure lost: %v", err)
	}
	fixture.assertStopped(t, "cache", "redis", "database", "logging")
}
