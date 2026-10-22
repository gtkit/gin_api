package news

import (
	"context"
	"errors"
	"slices"
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
	n, err := NewWithConfig(nil)
	if err != nil {
		panic("construct disabled test notifier: " + err.Error())
	}
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

// TestWarnLogsWebhookFailure 钉住 webhook 失败的可观测性
// （outbound-notification spec）：发送失败与邮件/短信通道一样进入错误日志，
// 不静默丢弃返回值。
func TestWarnLogsWebhookFailure(t *testing.T) {
	var logged []string
	n := newTestNotifier("https://example.invalid/hook", "TEST-ENV", "2026-03-22 22:00:00",
		func(context.Context, string, ...string) error { return errors.New("hook down") },
		func(msg string) { logged = append(logged, msg) })

	n.Warn("", "body")

	if len(logged) != 1 || !strings.Contains(logged[0], "hook down") {
		t.Fatalf("webhook failure was not logged, got %v", logged)
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
	if !slices.Equal(app.sent, want) {
		t.Fatalf("sent=%v want=%v", app.sent, want)
	}
}

// TestWebhookSenderSelectsPlatform 钉住三家平台可选，且留空按飞书兜底。
//
// platform 是后加的字段，历史配置里没有它——留空必须与引入本字段之前的
// 行为一致，否则一次升级就会让所有存量部署的告警发不出去。
func TestWebhookSenderSelectsPlatform(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		platform string
		wantErr  string
	}{
		{name: "留空按飞书", platform: ""},
		{name: "飞书", platform: "feishu"},
		{name: "钉钉", platform: "dingtalk"},
		{name: "企业微信", platform: "wecom"},
		{name: "未知平台报错", platform: "slack", wantErr: "unknown alert platform"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			send := webhookSenderFor(tt.platform, "")
			// 地址不可达：这里验的是平台选择与构造，不是真的发出去。
			err := send(context.Background(), "http://127.0.0.1:1/hook", "msg")

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			// 构造成功即可；发送失败是预期的（地址不可达）。
			if err != nil && strings.Contains(err.Error(), "unknown alert platform") {
				t.Fatalf("平台 %q 被判为未知", tt.platform)
			}
		})
	}
}

// TestWebhookSenderRejectsEmptyURL 钉住空 url 明确报错。
//
// 静默返回 nil 会让"配置漏填 fsurl"表现为"告警一切正常但没人收到"。
func TestWebhookSenderRejectsEmptyURL(t *testing.T) {
	t.Parallel()

	err := webhookSenderFor("feishu", "")(context.Background(), "  ", "msg")
	if err == nil {
		t.Fatal("空 url 被接受了")
	}
	if !strings.Contains(err.Error(), "url is empty") {
		t.Fatalf("错误 %q 没有指出是 url 为空", err)
	}
}
