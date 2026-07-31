package news

import (
	"context"
	"reflect"
	"strings"
	"testing"

	newslib "github.com/gtkit/news"

	"github.com/gtkit/gin-api/internal/platform/config"
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

func TestWarnUsesDefaultURLAndHooks(t *testing.T) {
	resetNewsHooksForTest()
	setNewsURLGetterForTest(func() string { return "https://example.invalid/hook" })
	setEnvNameForTest(func() string { return "TEST-ENV" })
	setNewsNowForTest(func() string { return "2026-03-22 22:00:00" })

	called := false
	var gotURL string
	var gotArgs []string
	setWebhookSenderForTest(func(url string, args ...string) error {
		called = true
		gotURL = url
		gotArgs = append([]string(nil), args...)
		return nil
	})

	Warn("", "body")

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

func TestErrRecordLogsAndWarns(t *testing.T) {
	resetNewsHooksForTest()
	logged := ""
	warned := []string{}
	setNewsErrorLoggerForTest(func(msg string) { logged = msg })
	setWebhookSenderForTest(func(url string, args ...string) error {
		warned = append(warned, args...)
		return nil
	})
	setNewsURLGetterForTest(func() string { return "https://example.invalid/hook" })
	setEnvNameForTest(func() string { return "ENV" })
	setNewsNowForTest(func() string { return "NOW" })

	ErrRecord("boom")

	if logged != "boom" {
		t.Fatalf("expected logger to receive message, got %q", logged)
	}
	if len(warned) == 0 {
		t.Fatal("expected warn sender to be called")
	}
}

func TestInitWithConfigSetsDefaultURLAndEnvName(t *testing.T) {
	resetNewsHooksForTest()
	InitWithConfig(&config.Config{
		Env:  "prod",
		News: config.NewsConfig{FSURL: "https://example.invalid/hook"},
	})
	setNewsNowForTest(func() string { return "2026-03-23 12:00:00" })

	var gotURL string
	var gotArgs []string
	setWebhookSenderForTest(func(url string, args ...string) error {
		gotURL = url
		gotArgs = append([]string(nil), args...)
		return nil
	})

	Warn("", "body")

	if gotURL != "https://example.invalid/hook" {
		t.Fatalf("gotURL = %q", gotURL)
	}
	if len(gotArgs) == 0 || !strings.Contains(gotArgs[0], "prod") {
		t.Fatalf("title = %#v", gotArgs)
	}
}
