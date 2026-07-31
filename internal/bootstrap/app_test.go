package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gojwt "github.com/gtkit/gojwt"
	"github.com/gtkit/tools/mailx"
	"github.com/gtkit/verify"

	"github.com/gtkit/gin-api/internal/platform/config"
)

type testHTTPServer struct {
	listenCalled   chan struct{}
	shutdownCalled chan struct{}
}

func newTestHTTPServer() *testHTTPServer {
	return &testHTTPServer{listenCalled: make(chan struct{}), shutdownCalled: make(chan struct{})}
}

func (s *testHTTPServer) ListenAndServe() error {
	close(s.listenCalled)
	<-s.shutdownCalled
	return http.ErrServerClosed
}

func (s *testHTTPServer) ListenAndServeTLS(string, string) error { return s.ListenAndServe() }

func (s *testHTTPServer) Shutdown(context.Context) error {
	select {
	case <-s.shutdownCalled:
	default:
		close(s.shutdownCalled)
	}
	return nil
}

func TestAppRunStartsServerAndWaitsForWorkerExitOnCancel(t *testing.T) {
	server := newTestHTTPServer()
	workerExited := make(chan struct{})

	app := &App{
		httpConfig: HTTPConfig{ShutdownTimeout: time.Second},
		httpServer: server,
		workers: NewWorkerManager(NewLegacyWorker("blocking-worker", func(ctx context.Context) error {
			<-ctx.Done()
			time.Sleep(20 * time.Millisecond)
			close(workerExited)
			return nil
		})),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	select {
	case <-server.listenCalled:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("server did not start before timeout")
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("Run did not return after cancellation")
	}

	select {
	case <-workerExited:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("worker was not waited before Run returned")
	}
}

// TestAppRunShutsDownHTTPBeforeCancellingWorkers 锁定关停顺序契约：
// workerCtx 经 context.WithoutCancel 独立派生，因此外部 ctx 取消不会立刻掐掉 worker——
// 必须先完成 HTTP Shutdown，再由 Run 显式取消 worker context。
// 若有人把 workerCtx 改回跟随 runCtx，worker 会在 HTTP 关停前被取消，本用例失败。
func TestAppRunShutsDownHTTPBeforeCancellingWorkers(t *testing.T) {
	server := newTestHTTPServer()
	var httpDownFirst atomic.Bool
	workerExited := make(chan struct{})

	app := &App{
		httpConfig: HTTPConfig{ShutdownTimeout: time.Second},
		httpServer: server,
		workers: NewWorkerManager(NewLegacyWorker("ordered-worker", func(ctx context.Context) error {
			<-ctx.Done()
			// worker context 被取消的瞬间，HTTP 必须已经 Shutdown。
			select {
			case <-server.shutdownCalled:
				httpDownFirst.Store(true)
			default:
			}
			close(workerExited)
			return nil
		})),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	select {
	case <-server.listenCalled:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("server did not start before timeout")
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("Run did not return after cancellation")
	}

	select {
	case <-workerExited:
	case <-time.After(time.Second):
		t.Fatalf("worker did not exit")
	}

	if !httpDownFirst.Load() {
		t.Fatal("worker context was canceled before HTTP shutdown completed")
	}
}

// TestAppRunWithoutWorkers 覆盖母版默认形态：buildWorkers 返回空 worker 集合时，
// 启停路径不得因等待 worker 而阻塞或报错。
func TestAppRunWithoutWorkers(t *testing.T) {
	server := newTestHTTPServer()
	app := &App{
		httpConfig: HTTPConfig{ShutdownTimeout: time.Second},
		httpServer: server,
		workers:    NewWorkerManager(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	select {
	case <-server.listenCalled:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("server did not start before timeout")
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("Run did not return with an empty worker set")
	}
}

// TestAppRunFailsFastOnWorkerError 锁定 worker 失败的错误传播：
// 任一 worker 返回错误即让 Run 以 worker failed 包装错误退出，不做静默重启。
func TestAppRunFailsFastOnWorkerError(t *testing.T) {
	server := newTestHTTPServer()
	app := &App{
		httpConfig: HTTPConfig{ShutdownTimeout: time.Second},
		httpServer: server,
		workers: NewWorkerManager(NewLegacyWorker("failing-worker", func(context.Context) error {
			return errors.New("boom")
		})),
	}

	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(context.Background()) }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Run returned nil error after worker failure")
		}
		if !strings.Contains(err.Error(), "worker failed") ||
			!strings.Contains(err.Error(), "failing-worker") ||
			!strings.Contains(err.Error(), "boom") {
			t.Fatalf("error %q does not carry worker name and cause", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after worker failure")
	}
}

func TestInitLegacyRuntimeAppliesConfiguredTimezone(t *testing.T) {
	originalLocal := time.Local
	originalInitLog := initLogWithConfig
	originalInitNews := initNewsWithConfig
	originalBuildMail := buildMailClient
	originalInitFile := initFileWithConfig
	originalInitDAO := initDAOWithConfig
	originalInitJWT := initJWTWithConfig
	originalVerify := initVerify
	originalJSONCheck := checkJSONRuntime
	defer func() {
		time.Local = originalLocal
		initLogWithConfig = originalInitLog
		initNewsWithConfig = originalInitNews
		buildMailClient = originalBuildMail
		initFileWithConfig = originalInitFile
		initDAOWithConfig = originalInitDAO
		initJWTWithConfig = originalInitJWT
		initVerify = originalVerify
		checkJSONRuntime = originalJSONCheck
	}()

	initLogWithConfig = func(*config.Config) {}
	initNewsWithConfig = func(*config.Config) {}
	buildMailClient = func(*config.Config) (*mailx.Client, error) { return nil, nil }
	initFileWithConfig = func(*config.Config) {}
	initDAOWithConfig = func(*config.Config, config.DBCredentials) {}
	initJWTWithConfig = func(*config.Config) (*gojwt.JwtEd25519, error) { return nil, nil }
	initVerify = func(...verify.Option) error { return nil }
	checkJSONRuntime = func() {}

	if err := initLegacyRuntime(&config.Config{
		Env: "test",
		App: config.AppConfig{Timezone: "Asia/Shanghai"},
	}, Options{}); err != nil {
		t.Fatalf("initLegacyRuntime returned error: %v", err)
	}
	if got := time.Local.String(); got != "Asia/Shanghai" {
		t.Fatalf("time.Local = %q, want Asia/Shanghai", got)
	}
}
