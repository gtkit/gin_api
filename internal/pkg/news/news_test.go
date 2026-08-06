package news

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	newslib "github.com/gtkit/news"
)

type fakeAppNewser struct {
	sent []string
}

func (f *fakeAppNewser) Expired() int { return 0 }
func (f *fakeAppNewser) SendImageMsg(_ context.Context, openID, filepath string) error {
	f.sent = append(f.sent, openID+":"+filepath)
	return nil
}
func (f *fakeAppNewser) SendTextMsg(context.Context, string, string) error { return nil }

var _ newslib.AppNewser = (*fakeAppNewser)(nil)

// newTestNotifier 直接构造一个 Notifier 实例。
//
// 删掉包级 defaultNotifier 之后，测试不再需要"重置全局 → 设 hook → 调包级函数"
// 这一套：每个用例持有自己的实例，天然隔离，也不会因执行顺序互相影响。
func newTestNotifier(url, env, now string, sender func(context.Context, string, ...string) error, errLogger func(string)) *Notifier {
	n, _ := NewWithConfig(nil) // nil 配置不启用任何通道，构造不可能失败
	n.defaultURL = url
	n.envName = env
	n.now = func() string { return now }
	if sender != nil {
		n.webhookSender = sender
	}
	if errLogger != nil {
		n.errorLogger = errLogger
	}
	return n
}

func TestWarnUsesDefaultURLAndHooks(t *testing.T) {
	called := false
	var gotURL string
	var gotArgs []string

	n := newTestNotifier("https://example.invalid/hook", "TEST-ENV", "2026-03-22 22:00:00",
		func(_ context.Context, url string, args ...string) error {
			called = true
			gotURL = url
			gotArgs = append([]string(nil), args...)
			return nil
		}, nil)

	n.Warn("", "body")

	if !called {
		t.Fatal("expected webhook sender to be called")
	}
	if gotURL != "https://example.invalid/hook" {
		t.Fatalf("expected default url to be used, got %q", gotURL)
	}
	if len(gotArgs) != 2 {
		t.Fatalf("expected title and body args, got %#v", gotArgs)
	}
	if !strings.Contains(gotArgs[0], "TEST-ENV") || !strings.Contains(gotArgs[0], "2026-03-22 22:00:00") {
		t.Fatalf("unexpected title %q", gotArgs[0])
	}
}

// TestWarnWebhookSendHasTimeout 钉住 webhook 发送的独立超时
// （outbound-notification spec：每次通道发送必须有独立超时）：
// 无超时的 webhook 会让 logger.Sync 的队列排空被一个外部端点无限拖住。
func TestWarnWebhookSendHasTimeout(t *testing.T) {
	var gotDeadline time.Time
	var hasDeadline bool

	n := newTestNotifier("https://example.invalid/hook", "TEST-ENV", "2026-03-22 22:00:00",
		func(ctx context.Context, _ string, _ ...string) error {
			gotDeadline, hasDeadline = ctx.Deadline()
			return nil
		}, nil)

	before := time.Now()
	n.Warn("", "body")

	if !hasDeadline {
		t.Fatal("webhook send context has no deadline; a stuck endpoint would stall log draining forever")
	}
	if remain := gotDeadline.Sub(before); remain <= 0 || remain > alertSendTimeout+time.Second {
		t.Fatalf("webhook deadline %v from call time, want within (0, %v]", remain, alertSendTimeout)
	}
}

// TestNilNotifierIsSafeAndSilent 锁住 nil 接收者的行为。
//
// 此前 nil 会回退到包级 defaultNotifier——那让"我没有配置通知器"
// 与"我用的是某个全局配置"变成同一件事，调用方无从分辨。
func TestNilNotifierIsSafeAndSilent(t *testing.T) {
	var n *Notifier
	n.Warn("https://example.invalid/hook", "body")
	n.ErrRecord("msg")
}

func TestSendPicMsgSkipsBlankOpenIDs(t *testing.T) {
	app := &fakeAppNewser{}

	if err := SendPicMsg(context.Background(), app, "u1, ,u2,,", "shot.png"); err != nil {
		t.Fatalf("SendPicMsg returned error: %v", err)
	}

	want := []string{"u1:shot.png", "u2:shot.png"}
	if !reflect.DeepEqual(app.sent, want) {
		t.Fatalf("sent=%v want=%v", app.sent, want)
	}
}
