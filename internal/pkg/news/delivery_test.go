package news

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type injectedWebhook func(context.Context, string, string) error

func (f injectedWebhook) Send(text string) { f.SendTo("", text) }

func (f injectedWebhook) SendTo(target, text string) {
	if err := f(context.Background(), target, text); err != nil {
		panic(err) // Unexpected failures in the direct SDK test entry point must surface.
	}
}

func (f injectedWebhook) SendContext(ctx context.Context, target, text string) error {
	return f(ctx, target, text)
}

type injectedMail func(context.Context, MailMessage) error

func (f injectedMail) Send(ctx context.Context, message MailMessage) error { return f(ctx, message) }

type injectedSMS func(context.Context, string, string, map[string]string) error

func (f injectedSMS) Send(ctx context.Context, phone, template string, params map[string]string) error {
	return f(ctx, phone, template, params)
}

func TestInjectedNotifierMessagesAndSnapshots(t *testing.T) {
	mailTo, phones := []string{"ops@example.invalid"}, []string{"first", "second"}
	var order, targets, texts []string
	var messages []MailMessage
	var paramsSeen []string
	stamp := time.Date(2026, time.October, 22, 12, 0, 0, 0, time.UTC)
	n, err := New(
		WithDefaultURL("default"), WithEnvironment("test"), WithClock(func() time.Time { return stamp }),
		WithWebhook(injectedWebhook(func(_ context.Context, target, text string) error {
			order = append(order, "webhook")
			targets = append(targets, target)
			texts = append(texts, text)
			return nil
		})),
		WithMail(injectedMail(func(_ context.Context, m MailMessage) error {
			order = append(order, "mail")
			messages = append(messages, MailMessage{To: slices.Clone(m.To), Subject: m.Subject, Text: m.Text})
			m.To[0] = "mutated"
			return nil
		}), mailTo...),
		WithSMS(injectedSMS(func(_ context.Context, phone, template string, params map[string]string) error {
			order = append(order, phone)
			if template != "alert" {
				t.Errorf("template=%q", template)
			}
			paramsSeen = append(paramsSeen, params["env"])
			params["env"] = "mutated"
			return nil
		}), "alert", phones...),
	)
	if err != nil {
		t.Fatal(err)
	}
	mailTo[0], phones[0] = "changed", "changed"
	if err := n.WarnContext(t.Context(), "override", "hello", "world"); err != nil {
		t.Fatal(err)
	}
	if err := n.WarnContext(t.Context(), "", "again"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(order, []string{"webhook", "mail", "first", "second", "webhook", "mail", "first", "second"}) {
		t.Fatalf("order=%v", order)
	}
	if !slices.Equal(targets, []string{"override", "default"}) || texts[0] != "test time: 2026-10-22 12:00:00 hello world" {
		t.Fatalf("targets=%v texts=%v", targets, texts)
	}
	for _, message := range messages {
		if message.To[0] != "ops@example.invalid" || message.Subject != "[test] service alert" {
			t.Fatalf("mail=%+v", message)
		}
	}
	if messages[0].Text != "hello world" {
		t.Fatalf("mail body=%q", messages[0].Text)
	}
	if !slices.Equal(paramsSeen, []string{"test", "test", "test", "test"}) {
		t.Fatalf("parameters=%v", paramsSeen)
	}
}

func TestInjectedNotifierEmptyTargetAndMissingCapability(t *testing.T) {
	var webhookCalls, mailCalls int
	n, err := New(WithWebhook(injectedWebhook(func(context.Context, string, string) error { webhookCalls++; return nil })), WithMail(injectedMail(func(context.Context, MailMessage) error { mailCalls++; return nil }), "recipient"))
	if err != nil {
		t.Fatal(err)
	}
	if err := n.WarnContext(t.Context(), "", "body"); err != nil {
		t.Fatal(err)
	}
	if webhookCalls != 0 || mailCalls != 1 {
		t.Fatalf("calls webhook=%d mail=%d", webhookCalls, mailCalls)
	}
	if err := n.WarnContext(t.Context(), "explicit", "body"); err != nil {
		t.Fatal(err)
	}
	if webhookCalls != 1 || mailCalls != 2 {
		t.Fatalf("calls webhook=%d mail=%d", webhookCalls, mailCalls)
	}
	var events []Delivery
	missing, err := New(WithReport(func(d Delivery) { events = append(events, d) }))
	if err != nil {
		t.Fatal(err)
	}
	if err := missing.WarnContext(t.Context(), "explicit", "body"); !errors.Is(err, ErrSenderMissing) {
		t.Fatal(err)
	}
	if events[0].Result != "missing" {
		t.Fatalf("events=%v", events)
	}
	var nilWebhook injectedWebhook
	if _, err := New(WithWebhook(nilWebhook)); !errors.Is(err, ErrSenderMissing) {
		t.Fatalf("typed nil=%v", err)
	}
}

func TestInjectedNotifierFailureAndPanicIsolation(t *testing.T) {
	for _, panicSender := range []bool{false, true} {
		t.Run(fmt.Sprint(panicSender), func(t *testing.T) {
			var order []string
			var events []Delivery
			failure := errors.New("https://secret.invalid/token phone password")
			n, err := New(WithDefaultURL("target"),
				WithWebhook(injectedWebhook(func(context.Context, string, string) error {
					order = append(order, "webhook")
					if panicSender {
						panic("secret token")
					}
					return failure
				})),
				WithMail(injectedMail(func(context.Context, MailMessage) error { order = append(order, "mail"); return failure }), "recipient"),
				WithSMS(injectedSMS(func(_ context.Context, phone, _ string, _ map[string]string) error {
					order = append(order, phone)
					if phone == "one" {
						panic("private phone")
					}
					return nil
				}), "alert", "one", "two"),
				WithReport(func(d Delivery) { events = append(events, d) }),
			)
			if err != nil {
				t.Fatal(err)
			}
			err = n.WarnContext(t.Context(), "", "body")
			if !errors.Is(err, failure) || !errors.Is(err, ErrSenderPanic) {
				t.Fatalf("joined errors=%v", err)
			}
			if !slices.Equal(order, []string{"webhook", "mail", "one", "two"}) {
				t.Fatalf("order=%v", order)
			}
			if len(events) != 4 || events[2].Result != "panic" || events[3].Result != "success" {
				t.Fatalf("events=%v", events)
			}
			if strings.Contains(fmt.Sprint(events), "secret") || strings.Contains(fmt.Sprint(events), "recipient") {
				t.Fatalf("diagnostic leaked payload: %v", events)
			}
		})
	}
}

func TestInjectedNotifierIndependentTenSecondBudgets(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var deadlines []time.Duration
		wait := func(ctx context.Context) error {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Error("missing deadline")
			}
			deadlines = append(deadlines, time.Until(deadline))
			<-ctx.Done()
			return ctx.Err()
		}
		n, err := New(WithDefaultURL("target"),
			WithWebhook(injectedWebhook(func(ctx context.Context, _, _ string) error { return wait(ctx) })),
			WithMail(injectedMail(func(ctx context.Context, _ MailMessage) error { return wait(ctx) }), "recipient"),
			WithSMS(injectedSMS(func(ctx context.Context, _, _ string, _ map[string]string) error { return wait(ctx) }), "alert", "one", "two"), WithReport(func(Delivery) {}),
		)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if err := n.WarnContext(t.Context(), "", "body"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		if time.Since(start) != 40*time.Second || !slices.Equal(deadlines, []time.Duration{10 * time.Second, 10 * time.Second, 10 * time.Second, 10 * time.Second}) {
			t.Fatalf("elapsed=%s deadlines=%v", time.Since(start), deadlines)
		}
	})
}

func TestInjectedNotifierCancellationDoesNotInvokeSenders(t *testing.T) {
	var calls int
	n, err := New(WithDefaultURL("target"), WithWebhook(injectedWebhook(func(context.Context, string, string) error { calls++; return nil })), WithReport(func(Delivery) {}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := n.WarnContext(ctx, "", "body"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("canceled notification invoked sender")
	}
}
