package mail

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gtkit/tools/mailx"

	"github.com/gtkit/gin-api/internal/platform/config"
)

type fakeSender struct {
	got  []mailx.Message
	fail error
}

func (f *fakeSender) Send(_ context.Context, msg mailx.Message) error {
	f.got = append(f.got, msg)
	return f.fail
}

// TestNewWithConfigGate 钉住配置门：未启用返回 (nil, nil)，
// 启用但材料非法（from 缺失）时构造失败——配置了却静默没有通道是禁止形态。
func TestNewWithConfigGate(t *testing.T) {
	t.Parallel()

	c, err := NewWithConfig(config.EmailConfig{})
	if c != nil || err != nil {
		t.Fatalf("disabled channel = (%v, %v), want (nil, nil)", c, err)
	}

	// 走真实 mailx 构造：from 缺失必须报错（构造不拨号，无网络依赖）。
	if _, err := NewWithConfig(config.EmailConfig{Host: "smtp.example.com"}); err == nil {
		t.Fatal("NewWithConfig accepted an enabled channel without a from address")
	}

	c, err = NewWithConfig(config.EmailConfig{Host: "smtp.example.com", From: "noreply@example.com"})
	if err != nil || c == nil {
		t.Fatalf("valid config = (%v, %v), want client", c, err)
	}
}

// TestSendBuildsMessageAndWrapsErrors 钉住发送路径：字段透传、无收件人拒发、
// 后端错误带 mail: 前缀包装、nil 客户端明确报错。
func TestSendBuildsMessageAndWrapsErrors(t *testing.T) {
	t.Parallel()

	fake := &fakeSender{}
	c := &Client{s: fake}

	msg := Message{To: []string{"ops@example.com"}, Subject: "alert", Text: "boom"}
	if err := c.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(fake.got) != 1 || fake.got[0].To[0] != "ops@example.com" ||
		fake.got[0].Subject != "alert" || fake.got[0].Text != "boom" {
		t.Fatalf("sent message = %+v", fake.got)
	}

	if err := c.Send(context.Background(), Message{Subject: "x"}); err == nil {
		t.Fatal("Send accepted a message without recipients")
	}

	fake.fail = errors.New("smtp down")
	if err := c.Send(context.Background(), msg); err == nil || !strings.Contains(err.Error(), "mail: send") {
		t.Fatalf("backend failure = %v, want wrapped mail: send error", err)
	}

	var nilClient *Client
	if err := nilClient.Send(context.Background(), msg); err == nil {
		t.Fatal("nil client Send returned nil error")
	}
}
