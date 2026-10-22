package bootstrap

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/platform/config"
	defaultlogging "github.com/gtkit/gin-api/internal/plugin/builtin/logging"
	"github.com/gtkit/gin-api/internal/testutil"

	"github.com/gtkit/logger/v2"
)

// captureMessager 收集经 H 系列推送的消息，断言告警执行点。
type captureMessager struct {
	mu   sync.Mutex
	msgs []string
}

func (c *captureMessager) Send(msg string) {
	c.mu.Lock()
	c.msgs = append(c.msgs, msg)
	c.mu.Unlock()
}

func (c *captureMessager) SendTo(_, msg string) { c.Send(msg) }

func (c *captureMessager) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.msgs...)
}

// withCaptureMessager 把带捕获 Messager 的实例设为进程默认 logger，
// 返回捕获器与排空函数（Sync 等待异步推送队列清空后才可断言）。
//
// SetDefault 是进程级状态：使用本装置的测试不得 t.Parallel()。
func withCaptureMessager(t *testing.T) (*captureMessager, func()) {
	t.Helper()

	capture := &captureMessager{}
	inst := logger.MustNew(
		logger.WithConsole(true),
		logger.WithFile(false),
		logger.WithMessager(capture),
		logger.WithReplaceGlobals(false),
	)
	drain := installTestLogger(t, inst)
	return capture, drain
}

// countContaining 统计包含子串的消息条数。
func countContaining(msgs []string, sub string) int {
	n := 0
	for _, m := range msgs {
		if strings.Contains(m, sub) {
			n++
		}
	}
	return n
}

// TestReportRunFailureAlertsWorkerEvents 钉住告警执行点（outbound-notification spec）：
// required 任务异常与 optional 任务降级各触发恰好一次推送。
// 只装 Messager 而没有生产调用点时，邮件/短信/webhook 配置永远不会产生告警。
func TestReportRunFailureAlertsWorkerEvents(t *testing.T) {
	capture, drain := withCaptureMessager(t)

	m, err := NewWorkerManager(DefaultWorkerBudget())
	if err != nil {
		t.Fatalf("NewWorkerManager: %v", err)
	}

	m.reportRunFailure(&funcWorker{
		name: "req-alert-worker", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
	}, errors.New("boom"))
	m.reportRunFailure(&funcWorker{
		name: "opt-alert-worker", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: false,
	}, errors.New("slump"))

	drain()
	msgs := capture.snapshot()
	if got := countContaining(msgs, "req-alert-worker"); got != 1 {
		t.Fatalf("required worker failure produced %d alert(s), want exactly 1; msgs=%q", got, msgs)
	}
	if got := countContaining(msgs, "opt-alert-worker"); got != 1 {
		t.Fatalf("optional worker degradation produced %d alert(s), want exactly 1; msgs=%q", got, msgs)
	}
}

// blockingMessager 的 Send 阻塞到 release 关闭，模拟被外部端点拖死的告警通道。
type blockingMessager struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingMessager) Send(string) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
}

func (b *blockingMessager) SendTo(_, msg string) { b.Send(msg) }

// TestSDKDrainWaitsForItsOwner records the SDK's cooperative limitation: Runtime
// does not hide an unfinished Sync in an abandoned goroutine. Release and join
// the test's blocking sender before finishing.
func TestSDKDrainWaitsForItsOwner(t *testing.T) {
	block := &blockingMessager{entered: make(chan struct{}), release: make(chan struct{})}
	inst := logger.MustNew(logger.WithConsole(true), logger.WithFile(false), logger.WithMessager(block), logger.WithReplaceGlobals(false))
	drain := installTestLogger(t, inst)
	defer drain()
	applog.HErrorf("stuck alert")
	waitClosed(t, block.entered, "messager did not receive alert")
	runtime := &Runtime{drain: func(context.Context) error { drain(); return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	entered, done := make(chan struct{}), make(chan error, 1)
	go func() { close(entered); done <- runtime.DrainLogs(ctx) }()
	<-entered
	select {
	case err := <-done:
		close(block.release)
		t.Fatalf("SDK drain finished before sender release: %v", err)
	default:
	}
	close(block.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled drain = %v", err)
	}
}

// TestAppRunAlertsOnErrorExit verifies exit alerts precede owner drain.
func TestAppRunAlertsOnErrorExit(t *testing.T) {
	testutil.RequireLoopback(t)

	tests := []struct {
		name       string
		workers    []Worker
		drive      func(app *App, cancel context.CancelFunc, errCh <-chan error)
		wantAlerts int
	}{
		{
			name: "启动失败以错误退出并告警一次",
			workers: []Worker{&funcWorker{
				name: "bad-prepare", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
				prepare: func(context.Context) error { return errors.New("prepare failed") },
			}},
			wantAlerts: 1,
		},
		{
			name:       "正常信号关停零退出告警",
			wantAlerts: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capture, drain := withCaptureMessager(t)
			server := newTestHTTPServer()
			app := newTestApp(t, server, tt.workers...)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			errCh := make(chan error, 1)
			go func() { errCh <- app.Run(ctx) }()

			if len(tt.workers) == 0 {
				waitClosed(t, server.listenCalled, "server did not start")
				cancel()
			}
			<-errCh

			drain()
			if got := countContaining(capture.snapshot(), "process exiting with error"); got != tt.wantAlerts {
				t.Fatalf("exit alerts = %d, want %d; msgs=%q", got, tt.wantAlerts, capture.snapshot())
			}
		})
	}
}

type loggingTestConfiguration struct{}

func (loggingTestConfiguration) Value() (*config.Config, error) { return &config.Config{}, nil }
func installTestLogger(t *testing.T, inst *logger.Logger) func() {
	t.Helper()
	p := defaultlogging.New(defaultlogging.Options{Configuration: loggingTestConfiguration{}, MainFactory: func(...logger.Option) (*logger.Logger, error) { return inst, nil }, AccessFactory: func(...logger.Option) (*logger.Logger, error) {
		return logger.New(logger.WithConsole(true), logger.WithFile(false), logger.WithReplaceGlobals(false))
	}})
	if err := p.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	drain := func() {
		if err := p.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(drain)
	return drain
}
