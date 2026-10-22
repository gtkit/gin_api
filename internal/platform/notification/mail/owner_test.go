package mail

import (
	"context"
	"errors"
	"testing"

	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
)

type sendFunc func(context.Context, news.MailMessage) error

func (f sendFunc) Send(ctx context.Context, message news.MailMessage) error { return f(ctx, message) }

func TestMailBorrowedViewDoesNotExposeCallerRecipients(t *testing.T) {
	cfg := &config.Config{Email: config.EmailConfig{Host: "smtp.example.invalid"}}
	opens, closes, calls := 0, 0, 0
	p, err := Open(t.Context(), Options{Config: cfg, Open: func(context.Context, config.EmailConfig) (news.MailSender, func(context.Context) error, error) {
		opens++
		return sendFunc(func(_ context.Context, message news.MailMessage) error {
			calls++
			message.To[0] = "changed"
			return nil
		}), func(context.Context) error { closes++; return nil }, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	view := p.Mail()
	if view == nil {
		t.Fatal("host must enable channel even without alert recipients")
	}
	message := news.MailMessage{To: []string{"original"}, Subject: "subject", Text: "body"}
	if err := view.Send(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	if message.To[0] != "original" {
		t.Fatal("sender changed caller recipients")
	}
	for range 2 {
		if err := p.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if err := view.Send(t.Context(), message); !errors.Is(err, news.ErrStopped) {
		t.Fatal(err)
	}
	if opens != 1 || closes != 1 || calls != 1 {
		t.Fatalf("opens=%d closes=%d calls=%d", opens, closes, calls)
	}
}
