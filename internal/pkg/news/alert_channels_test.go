package news

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/pkg/mail"
	"github.com/gtkit/gin-api/internal/platform/config"
)

type fakeMail struct {
	got  []mail.Message
	fail error
}

func (f *fakeMail) Send(_ context.Context, msg mail.Message) error {
	f.got = append(f.got, msg)
	return f.fail
}

type fakeSMSChannel struct {
	phones    []string
	templates []string
	params    []map[string]string
	fail      error
}

func (f *fakeSMSChannel) Send(_ context.Context, phone, templateKey string, params map[string]string) error {
	f.phones = append(f.phones, phone)
	f.templates = append(f.templates, templateKey)
	f.params = append(f.params, params)
	return f.fail
}

// TestWarnFansOutToConfiguredChannels 钉住多通道扇出：
// 邮件与短信各收到一次发送，邮件主题带环境名、短信参数契约为 {"env": 环境名}。
func TestWarnFansOutToConfiguredChannels(t *testing.T) {
	t.Parallel()

	fm := &fakeMail{}
	fs := &fakeSMSChannel{}
	n := newTestNotifier("http://hook", "prod", "2026-08-06 10:00:00",
		func(string, ...string) error { return nil }, nil)
	n.mail, n.mailTo = fm, []string{"ops@example.com"}
	n.sms, n.smsPhones, n.smsTemplate = fs, []string{"13800000000", "13900000000"}, "alert"

	n.Warn("", "db is down")

	if len(fm.got) != 1 || fm.got[0].To[0] != "ops@example.com" ||
		!strings.Contains(fm.got[0].Subject, "prod") || fm.got[0].Text != "db is down" {
		t.Fatalf("mail alert = %+v", fm.got)
	}
	if len(fs.phones) != 2 || fs.templates[0] != "alert" || fs.params[0]["env"] != "prod" {
		t.Fatalf("sms alert = phones %v templates %v params %v", fs.phones, fs.templates, fs.params)
	}
}

// TestWarnSingleChannelFailureIsIsolated 钉住通道故障隔离：
// 邮件失败被记录，短信照常发送，调用自身不受影响。
func TestWarnSingleChannelFailureIsIsolated(t *testing.T) {
	t.Parallel()

	var logged []string
	fm := &fakeMail{fail: errors.New("smtp down")}
	fs := &fakeSMSChannel{}
	n := newTestNotifier("http://hook", "prod", "2026-08-06 10:00:00",
		func(string, ...string) error { return nil },
		func(msg string) { logged = append(logged, msg) })
	n.mail, n.mailTo = fm, []string{"ops@example.com"}
	n.sms, n.smsPhones, n.smsTemplate = fs, []string{"13800000000"}, "alert"

	n.Warn("", "boom")

	if len(fs.phones) != 1 {
		t.Fatalf("sms channel did not send after the mail channel failed: %v", fs.phones)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "alert mail send failed") {
		t.Fatalf("mail failure was not recorded: %v", logged)
	}
}

// TestWarnSkipsUnconfiguredChannels 钉住零调用：未配置通道不存在任何发送。
func TestWarnSkipsUnconfiguredChannels(t *testing.T) {
	t.Parallel()

	n := newTestNotifier("http://hook", "dev", "2026-08-06 10:00:00",
		func(string, ...string) error { return nil }, nil)

	// 不接线 mail/sms：Warn 只走 webhook，不 panic、无其他调用。
	n.Warn("", "quiet")
}

// TestNewWithConfigPropagatesChannelBuildFailure 钉住构造失败传播：
// 配置了通道但客户端构造失败时 NewWithConfig 返回 error（fail-closed）。
func TestNewWithConfigPropagatesChannelBuildFailure(t *testing.T) {
	t.Parallel()

	// email.host 已设但 from 缺失：mailx 构造必然失败（无网络依赖）。
	_, err := NewWithConfig(&config.Config{
		Email: config.EmailConfig{Host: "smtp.example.com"},
	})
	if err == nil {
		t.Fatal("NewWithConfig swallowed a mail channel construction failure")
	}
	if !strings.Contains(err.Error(), "init mail channel") {
		t.Fatalf("error %q does not name the failing channel", err)
	}
}

// TestNewWithConfigLeavesChannelsNilWhenAlertTargetsMissing 钉住接线条件：
// 客户端可构造但没有告警目标时，通道字段保持 nil（接口不得包住 nil 指针）。
func TestNewWithConfigLeavesChannelsNilWhenAlertTargetsMissing(t *testing.T) {
	t.Parallel()

	n, err := NewWithConfig(&config.Config{
		Email: config.EmailConfig{Host: "smtp.example.com", From: "noreply@example.com"},
	})
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	if n.mail != nil || n.sms != nil {
		t.Fatalf("channels wired without alert targets: mail=%v sms=%v", n.mail, n.sms)
	}
	// 扇出对未接线通道零调用且不 panic。
	n.Warn("", "no targets")
}
