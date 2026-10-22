package dispatch_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/platform/notification/dispatch"
	"github.com/gtkit/gin-api/internal/platform/notification/mail"
	"github.com/gtkit/gin-api/internal/platform/notification/robot"
	"github.com/gtkit/gin-api/internal/platform/notification/sms"
)

type webhookFunc func(context.Context, string, string) error

func (f webhookFunc) Send(ctx context.Context, target, text string) error {
	return f(ctx, target, text)
}

type mailFunc func(context.Context, news.MailMessage) error

func (f mailFunc) Send(ctx context.Context, message news.MailMessage) error { return f(ctx, message) }

type smsFunc func(context.Context, string, string, map[string]string) error

func (f smsFunc) Send(ctx context.Context, phone, template string, params map[string]string) error {
	return f(ctx, phone, template, params)
}

type closer interface{ Close(context.Context) error }

func closeOwners(t *testing.T, owners ...closer) {
	t.Helper()
	for _, owner := range owners {
		if err := owner.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}
func enabledConfig() *config.Config {
	return &config.Config{Env: "test", News: config.NewsConfig{FSURL: "default", Platform: "feishu"}, Email: config.EmailConfig{Host: "smtp.example.invalid", From: "from@example.invalid", AlertTo: []string{"ops@example.invalid"}}, SMS: config.SMSConfig{Provider: "aliyun", AccessID: "test", AccessSecret: "test", SignName: "test", Templates: map[string]config.SMSTemplateConfig{"alert": {Code: "test"}}, AlertPhones: []string{"one", "two"}, AlertTemplate: "alert"}}
}

func TestNotificationCompositionAndOwnership(t *testing.T) {
	cfg := enabledConfig()
	opens := map[string]int{}
	closes := map[string]int{}
	var order, targets []string
	var messages []news.MailMessage
	var composite *dispatch.Owner
	cleanup := func(name string) func(context.Context) error {
		return func(ctx context.Context) error {
			closes[name]++
			if err := composite.Notify(ctx, "late", "body"); !errors.Is(err, news.ErrStopped) {
				t.Errorf("notification admitted during %s cleanup: %v", name, err)
			}
			return nil
		}
	}
	r, err := robot.Open(t.Context(), robot.Options{Config: cfg, Open: func(context.Context, config.NewsConfig) (news.WebhookSender, func(context.Context) error, error) {
		opens["robot"]++
		return webhookFunc(func(_ context.Context, target, text string) error {
			order = append(order, "robot")
			targets = append(targets, target)
			return nil
		}), cleanup("robot"), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.Open(t.Context(), mail.Options{Config: cfg, Open: func(context.Context, config.EmailConfig) (news.MailSender, func(context.Context) error, error) {
		opens["mail"]++
		return mailFunc(func(_ context.Context, message news.MailMessage) error {
			order = append(order, "mail")
			messages = append(messages, message)
			return nil
		}), cleanup("mail"), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := sms.Open(t.Context(), sms.Options{Config: cfg, Open: func(context.Context, config.SMSConfig) (news.SMSSender, func(context.Context) error, error) {
		opens["sms"]++
		return smsFunc(func(_ context.Context, phone, template string, params map[string]string) error {
			order = append(order, phone)
			if template != "alert" || params["env"] != "test" {
				t.Error("SMS input changed")
			}
			return nil
		}), cleanup("sms"), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	composite, err = dispatch.Open(t.Context(), dispatch.Options{Config: cfg, Robot: r, Mail: m, SMS: s, Report: func(news.Delivery) {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 0 {
		t.Fatal("Open sent a notification")
	}
	for _, name := range []string{"robot", "mail", "sms"} {
		if opens[name] != 1 {
			t.Fatalf("%s opens=%d", name, opens[name])
		}
	}
	cfg.News.FSURL = "changed"
	cfg.Email.AlertTo[0] = "changed"
	cfg.SMS.AlertPhones[0] = "changed"
	if err := composite.Notify(t.Context(), "override", "body"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(order, []string{"robot", "mail", "one", "two"}) {
		t.Fatalf("order=%v", order)
	}
	if messages[0].To[0] != "ops@example.invalid" || messages[0].Text != "body" {
		t.Fatalf("mail changed: %+v", messages)
	}
	// The logging owner must be drained before the dispatcher and its borrowed channels.
	if err := composite.Notify(t.Context(), "", "shutdown"); err != nil {
		t.Fatal(err)
	}
	closeOwners(t, composite, s, m, r)
	closeOwners(t, composite, s, m, r)
	if !slices.Equal(targets, []string{"override", "default"}) {
		t.Fatalf("targets=%v", targets)
	}
	for _, name := range []string{"robot", "mail", "sms"} {
		if closes[name] != 1 {
			t.Fatalf("%s closes=%d", name, closes[name])
		}
	}
	if err := composite.Notify(t.Context(), "late", "body"); !errors.Is(err, news.ErrStopped) {
		t.Fatal(err)
	}
}

func TestNotificationRejectsConfiguredMissingAndTypedNilProviders(t *testing.T) {
	for _, name := range []string{"robot", "mail", "sms", "typed nil"} {
		t.Run(name, func(t *testing.T) {
			cfg := &config.Config{}
			opts := dispatch.Options{Config: cfg, Report: func(news.Delivery) {}}
			switch name {
			case "robot":
				cfg.News.FSURL = "target"
			case "mail":
				cfg.Email.Host = "smtp"
			case "sms":
				cfg.SMS.Provider = "aliyun"
			case "typed nil":
				var r *robot.Owner
				opts.Robot = r
			}
			owner, err := dispatch.Open(t.Context(), opts)
			if owner == nil || !errors.Is(err, news.ErrSenderMissing) {
				t.Fatalf("Open=(%v,%v)", owner, err)
			}
			closeOwners(t, owner)
		})
	}
}

func TestDisabledChannelFactoriesAreNotCalled(t *testing.T) {
	cfg := &config.Config{}
	mailCalls, smsCalls := 0, 0
	m, err := mail.Open(t.Context(), mail.Options{Config: cfg, Open: func(context.Context, config.EmailConfig) (news.MailSender, func(context.Context) error, error) {
		mailCalls++
		return nil, nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := sms.Open(t.Context(), sms.Options{Config: cfg, Open: func(context.Context, config.SMSConfig) (news.SMSSender, func(context.Context) error, error) {
		smsCalls++
		return nil, nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if m.Mail() != nil || s.SMS() != nil || mailCalls != 0 || smsCalls != 0 {
		t.Fatal("disabled channel constructed or published")
	}
	if err := m.Send(t.Context(), news.MailMessage{}); !errors.Is(err, news.ErrSenderDisabled) {
		t.Fatal(err)
	}
	if err := s.Send(t.Context(), "", "", nil); !errors.Is(err, news.ErrSenderDisabled) {
		t.Fatal(err)
	}
	closeOwners(t, s, m)
}

func TestChannelFactoryConfigurationIsCopied(t *testing.T) {
	cfg := enabledConfig()
	m, err := mail.Open(t.Context(), mail.Options{Config: cfg, Open: func(_ context.Context, c config.EmailConfig) (news.MailSender, func(context.Context) error, error) {
		c.AlertTo[0] = "mutated"
		return mailFunc(func(context.Context, news.MailMessage) error { return nil }), nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := sms.Open(t.Context(), sms.Options{Config: cfg, Open: func(_ context.Context, c config.SMSConfig) (news.SMSSender, func(context.Context) error, error) {
		c.AlertPhones[0] = "mutated"
		c.Templates["alert"] = config.SMSTemplateConfig{Code: "mutated"}
		return smsFunc(func(context.Context, string, string, map[string]string) error { return nil }), nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Email.AlertTo[0] != "ops@example.invalid" || cfg.SMS.AlertPhones[0] != "one" || cfg.SMS.Templates["alert"].Code != "test" {
		t.Fatal("factory mutated source configuration")
	}
	closeOwners(t, s, m)
}

func TestFailedOpenRetainsPartialOwnerForCallerRollback(t *testing.T) {
	failure, closeFailure := errors.New("factory failed"), errors.New("cleanup failed")
	closed := 0
	owner, err := mail.Open(t.Context(), mail.Options{Config: enabledConfig(), Open: func(context.Context, config.EmailConfig) (news.MailSender, func(context.Context) error, error) {
		return mailFunc(func(context.Context, news.MailMessage) error { return nil }), func(ctx context.Context) error {
			closed++
			if ctx.Err() != nil {
				t.Error("rollback inherited cancellation")
			}
			return closeFailure
		}, failure
	}})
	if owner == nil || !errors.Is(err, failure) || closed != 0 {
		t.Fatalf("Open=(%v,%v) closes=%d", owner, err, closed)
	}
	for range 2 {
		if err := owner.Close(t.Context()); !errors.Is(err, closeFailure) {
			t.Fatalf("Close=%v", err)
		}
	}
	if closed != 1 {
		t.Fatalf("closes=%d", closed)
	}
}

func TestFailedOpenCancellationUsesCallerRollbackContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	closed := 0
	owner, err := mail.Open(ctx, mail.Options{Config: enabledConfig(), Open: func(context.Context, config.EmailConfig) (news.MailSender, func(context.Context) error, error) {
		cancel()
		return mailFunc(func(context.Context, news.MailMessage) error { return nil }), func(ctx context.Context) error { closed++; return ctx.Err() }, nil
	}})
	if owner == nil || !errors.Is(err, context.Canceled) || closed != 0 {
		t.Fatalf("Open=(%v,%v) closes=%d", owner, err, closed)
	}
	closeOwners(t, owner)
	if closed != 1 {
		t.Fatalf("closes=%d", closed)
	}
}

func openRobot(t *testing.T, cfg *config.Config, send webhookFunc, cleanup func(context.Context) error) *robot.Owner {
	t.Helper()
	r, err := robot.Open(t.Context(), robot.Options{Config: cfg, Open: func(context.Context, config.NewsConfig) (news.WebhookSender, func(context.Context) error, error) {
		return send, cleanup, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func openDispatch(t *testing.T, cfg *config.Config, r *robot.Owner) *dispatch.Owner {
	t.Helper()
	p, err := dispatch.Open(t.Context(), dispatch.Options{Config: cfg, Robot: r, Report: func(news.Delivery) {}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNotificationDrainCancellationAndLateCallbacks(t *testing.T) {
	cfg := &config.Config{News: config.NewsConfig{FSURL: "target"}}
	var sends atomic.Int32
	r := openRobot(t, cfg, func(context.Context, string, string) error { sends.Add(1); return nil }, nil)
	old, fresh := openDispatch(t, cfg, r), openDispatch(t, cfg, r)
	drain, cancel := context.WithCancel(t.Context())
	release := old.BindDrain(drain)
	release()
	cancel()
	if err := old.Notify(t.Context(), "", "live"); err != nil {
		t.Fatal(err)
	}
	expired, expire := context.WithCancel(t.Context())
	expire()
	release = old.BindDrain(expired)
	if err := old.Notify(t.Context(), "", "late"); !errors.Is(err, context.Canceled) {
		t.Fatalf("late=%v", err)
	}
	release()
	release()
	if err := fresh.Notify(t.Context(), "", "fresh"); err != nil {
		t.Fatal(err)
	}
	if sends.Load() != 2 {
		t.Fatalf("sends=%d", sends.Load())
	}
	closeOwners(t, old)
	old.Warn("target", "SDK late callback")
	if sends.Load() != 2 {
		t.Fatal("stopped composite touched channel")
	}
	closeOwners(t, fresh, r)
}

func TestCompositeAndChannelKeepNonCooperatingResources(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := &config.Config{News: config.NewsConfig{FSURL: "target"}}
		entered, release := make(chan struct{}), make(chan struct{})
		var closes atomic.Int32
		r := openRobot(t, cfg, func(context.Context, string, string) error { close(entered); <-release; return nil }, func(context.Context) error { closes.Add(1); return nil })
		p := openDispatch(t, cfg, r)
		var workers sync.WaitGroup
		workers.Go(func() {
			if err := p.Notify(t.Context(), "", "body"); !errors.Is(err, context.Canceled) {
				t.Errorf("canceled notify=%v", err)
			}
		})
		<-entered
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := p.Close(ctx); !errors.Is(err, news.ErrNotDrained) {
			t.Fatalf("composite Close=%v", err)
		}
		if err := r.Close(ctx); !errors.Is(err, news.ErrNotDrained) {
			t.Fatalf("channel Close=%v", err)
		}
		if closes.Load() != 0 {
			t.Fatal("resource closed during send")
		}
		close(release)
		workers.Wait()
		if closes.Load() != 0 {
			t.Fatal("timed out owner released resource behind caller")
		}
	})
}

func TestDefaultChannelsConstructWithoutSending(t *testing.T) {
	cfg := enabledConfig()
	r, err := robot.Open(t.Context(), robot.Options{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.Open(t.Context(), mail.Options{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	s, err := sms.Open(t.Context(), sms.Options{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if r.Robot() == nil || m.Mail() == nil || s.SMS() == nil {
		t.Fatal("default capability not published")
	}
	closeOwners(t, s, m, r)
}

func TestCanceledOpenPreventsFactory(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls := 0
	owner, err := robot.Open(ctx, robot.Options{Config: enabledConfig(), Open: func(context.Context, config.NewsConfig) (news.WebhookSender, func(context.Context) error, error) {
		calls++
		return webhookFunc(func(context.Context, string, string) error { return nil }), nil, nil
	}})
	if owner == nil || !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("Open=(%v,%v) calls=%d", owner, err, calls)
	}
	closeOwners(t, owner)
}

func TestStartupCancellationDoesNotCancelOpenedChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cfg := enabledConfig()
	calls := 0
	owner, err := robot.Open(ctx, robot.Options{Config: cfg, Open: func(context.Context, config.NewsConfig) (news.WebhookSender, func(context.Context) error, error) {
		return webhookFunc(func(ctx context.Context, _, _ string) error { calls++; return ctx.Err() }), nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := owner.Send(t.Context(), "target", "body"); err != nil || calls != 1 {
		t.Fatalf("Send=%v calls=%d", err, calls)
	}
	closeOwners(t, owner)
}
