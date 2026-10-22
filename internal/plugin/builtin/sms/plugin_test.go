package sms

import (
	"context"
	"errors"
	"testing"

	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
)

type configuration struct{ value config.Config }

func (c *configuration) Value() (*config.Config, error) { return &c.value, nil }

type sendFunc func(context.Context, string, string, map[string]string) error

func (f sendFunc) Send(ctx context.Context, phone, template string, params map[string]string) error {
	return f(ctx, phone, template, params)
}

func TestSMSBorrowedViewCopiesCallerParameters(t *testing.T) {
	cfg := &configuration{value: config.Config{SMS: config.SMSConfig{Provider: "aliyun"}}}
	opens, closes, calls := 0, 0, 0
	p := New(Options{Configuration: cfg, Open: func(context.Context, config.SMSConfig) (news.SMSSender, func(context.Context) error, error) {
		opens++
		return sendFunc(func(_ context.Context, phone, template string, params map[string]string) error {
			calls++
			if phone != "recipient" || template != "logical" {
				t.Error("logical SMS contract changed")
			}
			params["env"] = "changed"
			return nil
		}), func(context.Context) error { closes++; return nil }, nil
	}})
	if err := p.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	view := p.SMS()
	if view == nil {
		t.Fatal("configured provider was not published")
	}
	if err := p.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	params := map[string]string{"env": "original"}
	if err := view.Send(t.Context(), "recipient", "logical", params); err != nil {
		t.Fatal(err)
	}
	if params["env"] != "original" {
		t.Fatal("sender changed caller parameters")
	}
	for range 2 {
		if err := p.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if err := view.Send(t.Context(), "recipient", "logical", params); !errors.Is(err, news.ErrStopped) {
		t.Fatal(err)
	}
	if opens != 1 || closes != 1 || calls != 1 {
		t.Fatalf("opens=%d closes=%d calls=%d", opens, closes, calls)
	}
}
