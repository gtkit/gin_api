package news

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gtkit/logger/v2"
)

func TestChannelOptionsAreIndependent(t *testing.T) {
	for selected := range 8 {
		t.Run(strconv.Itoa(selected), func(t *testing.T) {
			var calls [3]int
			opts := []Option{WithReport(func(Delivery) {})}
			if selected&1 != 0 {
				opts = append(opts, WithDefaultURL("target"), WithWebhook(injectedWebhook(func(context.Context, string, string) error {
					calls[0]++
					return nil
				})))
			}
			if selected&2 != 0 {
				opts = append(opts, WithMail(injectedMail(func(context.Context, MailMessage) error {
					calls[1]++
					return nil
				}), "recipient"))
			}
			if selected&4 != 0 {
				opts = append(opts, WithSMS(injectedSMS(func(context.Context, string, string, map[string]string) error {
					calls[2]++
					return nil
				}), "alert", "phone"))
			}
			n, err := New(opts...)
			if err != nil {
				t.Fatal(err)
			}
			if calls != [3]int{} {
				t.Fatal("construction sent a notification")
			}
			if err := n.SendContext(t.Context(), "", "body"); err != nil {
				t.Fatal(err)
			}
			want := [3]int{selected & 1, (selected >> 1) & 1, (selected >> 2) & 1}
			if calls != want {
				t.Fatalf("calls=%v want=%v", calls, want)
			}
		})
	}
}

func TestOptionsSnapshotDestinationsBeforeReuse(t *testing.T) {
	recipients, phones := []string{"original@example.invalid"}, []string{"original-phone"}
	var gotRecipients, gotPhones []string
	mail := WithMail(injectedMail(func(_ context.Context, message MailMessage) error {
		gotRecipients = append(gotRecipients, message.To...)
		message.To[0] = "sender-mutation"
		return nil
	}), recipients...)
	sms := WithSMS(injectedSMS(func(_ context.Context, phone, _ string, _ map[string]string) error {
		gotPhones = append(gotPhones, phone)
		return nil
	}), "alert", phones...)
	recipients[0], phones[0] = "caller-mutation", "caller-mutation"
	for range 2 {
		n, err := New(mail, sms)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := n.SendContext(t.Context(), "", "body"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !slices.Equal(gotRecipients, []string{"original@example.invalid", "original@example.invalid", "original@example.invalid", "original@example.invalid"}) ||
		!slices.Equal(gotPhones, []string{"original-phone", "original-phone", "original-phone", "original-phone"}) {
		t.Fatalf("recipients=%v phones=%v", gotRecipients, gotPhones)
	}
}

func TestOptionsUseLastValueAndNilDefaults(t *testing.T) {
	var target, text string
	stamp := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
	n, err := New(
		WithDefaultURL("old"), WithDefaultURL("new"),
		WithEnvironment("old"), WithEnvironment("new"),
		WithClock(nil), WithClock(func() time.Time { return stamp }),
		WithReport(nil),
		WithWebhook(nil), WithWebhook(injectedWebhook(func(_ context.Context, gotTarget, gotText string) error {
			target, text = gotTarget, gotText
			return nil
		})),
		WithMail(injectedMail(func(context.Context, MailMessage) error { t.Error("disabled mail used"); return nil }), "old"), WithMail(nil),
		WithSMS(injectedSMS(func(context.Context, string, string, map[string]string) error {
			t.Error("disabled SMS used")
			return nil
		}), "old", "old"), WithSMS(nil, ""),
	)
	if err != nil {
		t.Fatal(err)
	}
	n.Send("body")
	if target != "new" || text != "new time: 2026-11-03 12:00:00 body" {
		t.Fatalf("target=%q text=%q", target, text)
	}
	if _, err := New(WithClock(nil), WithReport(nil)); err != nil {
		t.Fatal(err)
	}
}

func TestOptionsRejectNilValues(t *testing.T) {
	if n, err := New(nil); err == nil || n != nil {
		t.Fatalf("nil option: notifier=%v error=%v", n, err)
	}
	var webhook injectedWebhook
	var mail injectedMail
	var sms injectedSMS
	for _, option := range []Option{WithWebhook(webhook), WithMail(mail), WithSMS(sms, "alert")} {
		if n, err := New(option); !errors.Is(err, ErrSenderMissing) || n != nil {
			t.Fatalf("typed nil: notifier=%v error=%v", n, err)
		}
	}
}

func TestNotifierPlugsDirectlyIntoLogger(t *testing.T) {
	type message struct{ target, text string }
	deliveries := make(chan message, 2)
	n, err := New(WithDefaultURL("default"), WithWebhook(injectedWebhook(func(_ context.Context, target, text string) error {
		deliveries <- message{target, text}
		return nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	instance := logger.MustNew(logger.WithConsole(true), logger.WithFile(false), logger.WithReplaceGlobals(false), logger.WithMessager(n))
	t.Cleanup(instance.Sync)
	instance.HError("default body")
	instance.HErrorTo("explicit", "explicit body")
	instance.Sync()
	for _, want := range []message{{"default", "default body"}, {"explicit", "explicit body"}} {
		select {
		case got := <-deliveries:
			if got.target != want.target || !strings.HasSuffix(got.text, want.text) {
				t.Fatalf("delivery=%v want=%v", got, want)
			}
		default:
			t.Fatal("SDK did not drain the notification")
		}
	}
}
