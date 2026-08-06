package bootstrap

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/gtkit/gin-api/internal/testutil"

	loggerv2 "github.com/gtkit/logger/v2"
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
	inst := loggerv2.MustNew(
		loggerv2.WithConsole(true),
		loggerv2.WithFile(false),
		loggerv2.WithMessager(capture),
		loggerv2.WithReplaceGlobals(false),
	)
	prev := loggerv2.Default()
	loggerv2.SetDefault(inst)
	t.Cleanup(func() { loggerv2.SetDefault(prev) })

	return capture, func() { inst.Sync() }
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

// TestAppRunAlertsOnErrorExit 钉住进程级退出告警：Run 以错误结束时，
// 告警在日志刷写之前入队恰好一次；正常信号关停零告警。
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
