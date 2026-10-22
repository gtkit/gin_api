package notification_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/plugin/builtin/mail"
	"github.com/gtkit/gin-api/internal/plugin/builtin/notification"
	"github.com/gtkit/gin-api/internal/plugin/builtin/robot"
	"github.com/gtkit/gin-api/internal/plugin/builtin/sms"
)

type configuration struct {
	value *config.Config
	reads int
}

func (c *configuration) Value() (*config.Config, error) { c.reads++; return c.value, nil }
func (*configuration) Name() string                     { return "configuration" }
func (*configuration) Init(ctx context.Context) error   { return ctx.Err() }
func (*configuration) Start(ctx context.Context) error  { return ctx.Err() }
func (*configuration) Stop(ctx context.Context) error   { return ctx.Err() }

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

type fixtureOwner struct {
	stop     func(context.Context) error
	startErr error
}

func (*fixtureOwner) Name() string                     { return "logging.fixture" }
func (*fixtureOwner) Init(ctx context.Context) error   { return ctx.Err() }
func (f *fixtureOwner) Start(context.Context) error    { return f.startErr }
func (f *fixtureOwner) Stop(ctx context.Context) error { return f.stop(ctx) }

func enabledConfig() *config.Config {
	return &config.Config{Env: "test", News: config.NewsConfig{FSURL: "default", Platform: "feishu"}, Email: config.EmailConfig{Host: "smtp.example.invalid", From: "from@example.invalid", AlertTo: []string{"ops@example.invalid"}}, SMS: config.SMSConfig{Provider: "aliyun", AccessID: "test", AccessSecret: "test", SignName: "test", Templates: map[string]config.SMSTemplateConfig{"alert": {Code: "test", SignName: ""}}, AlertPhones: []string{"one", "two"}, AlertTemplate: "alert"}}
}

func TestNotificationRegistryCompositionAndOwnership(t *testing.T) {
	cfg := &configuration{value: enabledConfig()}
	opens := map[string]int{}
	closes := map[string]int{}
	var order []string
	var messages []news.MailMessage
	var targets []string
	var composite *notification.Plugin
	cleanup := func(name string) func(context.Context) error {
		return func(context.Context) error {
			closes[name]++
			if err := composite.Notify(t.Context(), "late", "body"); !errors.Is(err, news.ErrStopped) {
				t.Errorf("notification still admitted during %s cleanup: %v", name, err)
			}
			return nil
		}
	}
	r := robot.New(robot.Options{Configuration: cfg, Open: func(context.Context, config.NewsConfig) (news.WebhookSender, func(context.Context) error, error) {
		opens["robot"]++
		return webhookFunc(func(_ context.Context, target, text string) error {
			order = append(order, "robot")
			targets = append(targets, target)
			return nil
		}), cleanup("robot"), nil
	}})
	m := mail.New(mail.Options{Configuration: cfg, Open: func(context.Context, config.EmailConfig) (news.MailSender, func(context.Context) error, error) {
		opens["mail"]++
		return mailFunc(func(_ context.Context, message news.MailMessage) error {
			order = append(order, "mail")
			messages = append(messages, message)
			return nil
		}), cleanup("mail"), nil
	}})
	s := sms.New(sms.Options{Configuration: cfg, Open: func(context.Context, config.SMSConfig) (news.SMSSender, func(context.Context) error, error) {
		opens["sms"]++
		return smsFunc(func(_ context.Context, phone, template string, params map[string]string) error {
			order = append(order, phone)
			if template != "alert" || params["env"] != "test" {
				t.Errorf("SMS input changed")
			}
			return nil
		}), cleanup("sms"), nil
	}})
	composite = notification.New(notification.Options{Configuration: cfg, Robot: r, Mail: m, SMS: s, Report: func(news.Delivery) {}})
	owner := &fixtureOwner{stop: func(ctx context.Context) error { return composite.Notify(ctx, "", "shutdown") }}
	registry := core.NewPluginRegistry()
	for _, item := range []struct {
		value core.Plugin
		deps  []string
	}{
		{cfg, nil}, {r, []string{cfg.Name()}}, {m, []string{cfg.Name()}}, {s, []string{cfg.Name()}}, {composite, []string{cfg.Name(), r.Name(), m.Name(), s.Name()}}, {owner, []string{composite.Name()}},
	} {
		if err := registry.Register(item.value, item.deps...); err != nil {
			t.Fatal(err)
		}
	}
	if cfg.reads != 0 || len(opens) != 0 || len(order) != 0 {
		t.Fatal("construction or registration had effects")
	}
	if err := registry.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"robot", "mail", "sms"} {
		if opens[name] != 1 {
			t.Fatalf("%s opened %d times", name, opens[name])
		}
	}
	if len(order) != 0 {
		t.Fatal("initialization sent a notification")
	}
	cfg.value.News.FSURL = "changed"
	cfg.value.Email.AlertTo[0] = "changed"
	cfg.value.SMS.AlertPhones[0] = "changed"
	if err := composite.Notify(t.Context(), "override", "body"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(order, []string{"robot", "mail", "one", "two"}) {
		t.Fatalf("order=%v", order)
	}
	if messages[0].To[0] != "ops@example.invalid" || messages[0].Text != "body" {
		t.Fatalf("mail changed: %+v", messages)
	}
	if err := registry.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(targets, []string{"override", "default"}) {
		t.Fatalf("targets=%v", targets)
	}
	for _, name := range []string{"robot", "mail", "sms"} {
		if closes[name] != 1 {
			t.Fatalf("%s closed %d times", name, closes[name])
		}
	}
	if err := registry.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := composite.Notify(t.Context(), "late", "body"); !errors.Is(err, news.ErrStopped) {
		t.Fatal(err)
	}
}

func TestNotificationRejectsConfiguredMissingAndTypedNilProviders(t *testing.T) {
	for _, name := range []string{"robot", "mail", "sms", "typed nil"} {
		t.Run(name, func(t *testing.T) {
			cfg := &configuration{value: &config.Config{}}
			opts := notification.Options{Configuration: cfg, Report: func(news.Delivery) {}}
			switch name {
			case "robot":
				cfg.value.News.FSURL = "target"
			case "mail":
				cfg.value.Email.Host = "smtp"
			case "sms":
				cfg.value.SMS.Provider = "aliyun"
			case "typed nil":
				var r *robot.Plugin
				opts.Robot = r
			}
			p := notification.New(opts)
			if err := p.Init(t.Context()); !errors.Is(err, news.ErrSenderMissing) {
				t.Fatalf("Init=%v", err)
			}
			if err := p.Start(t.Context()); err == nil {
				t.Fatal("failed Init could start")
			}
			_ = p.Stop(t.Context())
		})
	}
}

func TestDisabledChannelFactoriesAreNotCalled(t *testing.T) {
	cfg := &configuration{value: &config.Config{}}
	mailCalls, smsCalls := 0, 0
	m := mail.New(mail.Options{Configuration: cfg, Open: func(context.Context, config.EmailConfig) (news.MailSender, func(context.Context) error, error) {
		mailCalls++
		return nil, nil, nil
	}})
	s := sms.New(sms.Options{Configuration: cfg, Open: func(context.Context, config.SMSConfig) (news.SMSSender, func(context.Context) error, error) {
		smsCalls++
		return nil, nil, nil
	}})
	for _, p := range []core.Plugin{m, s} {
		if err := p.Init(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := p.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
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
	for _, p := range []core.Plugin{m, s} {
		if err := p.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestChannelFactoryConfigurationIsCopied(t *testing.T) {
	cfg := &configuration{value: enabledConfig()}
	m := mail.New(mail.Options{Configuration: cfg, Open: func(_ context.Context, c config.EmailConfig) (news.MailSender, func(context.Context) error, error) {
		c.AlertTo[0] = "mutated"
		return mailFunc(func(context.Context, news.MailMessage) error { return nil }), nil, nil
	}})
	s := sms.New(sms.Options{Configuration: cfg, Open: func(_ context.Context, c config.SMSConfig) (news.SMSSender, func(context.Context) error, error) {
		c.AlertPhones[0] = "mutated"
		c.Templates["alert"] = config.SMSTemplateConfig{Code: "mutated"}
		return smsFunc(func(context.Context, string, string, map[string]string) error { return nil }), nil, nil
	}})
	for _, p := range []core.Plugin{m, s} {
		if err := p.Init(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if cfg.value.Email.AlertTo[0] != "ops@example.invalid" || cfg.value.SMS.AlertPhones[0] != "one" || cfg.value.SMS.Templates["alert"].Code != "test" {
		t.Fatal("factory mutated source configuration")
	}
	for _, p := range []core.Plugin{m, s} {
		if err := p.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRegistryRollsBackPartialFactoryAndUnstartedChannels(t *testing.T) {
	for _, startFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "factory failure", true: "start failure"}[startFailure], func(t *testing.T) {
			cfg := &configuration{value: enabledConfig()}
			failure := errors.New("factory or start failed")
			closeFailure := errors.New("cleanup failed")
			var robotClosed, mailClosed, smsOpened, smsClosed int
			r := robot.New(robot.Options{Configuration: cfg, Open: func(context.Context, config.NewsConfig) (news.WebhookSender, func(context.Context) error, error) {
				return webhookFunc(func(context.Context, string, string) error { t.Error("unstarted channel sent"); return nil }), func(context.Context) error { robotClosed++; return nil }, nil
			}})
			m := mail.New(mail.Options{Configuration: cfg, Open: func(context.Context, config.EmailConfig) (news.MailSender, func(context.Context) error, error) {
				var err error
				if !startFailure {
					err = failure
				}
				return mailFunc(func(context.Context, news.MailMessage) error { return nil }), func(context.Context) error {
					mailClosed++
					if !startFailure {
						return closeFailure
					}
					return nil
				}, err
			}})
			s := sms.New(sms.Options{Configuration: cfg, Open: func(context.Context, config.SMSConfig) (news.SMSSender, func(context.Context) error, error) {
				smsOpened++
				return smsFunc(func(context.Context, string, string, map[string]string) error { return nil }), func(context.Context) error { smsClosed++; return nil }, nil
			}})
			owner := &fixtureOwner{startErr: failure, stop: func(context.Context) error { return nil }}
			registry := core.NewPluginRegistry()
			for _, item := range []struct {
				value core.Plugin
				deps  []string
			}{{cfg, nil}, {r, []string{cfg.Name()}}, {m, []string{r.Name()}}, {s, []string{m.Name()}}, {owner, []string{s.Name()}}} {
				if err := registry.Register(item.value, item.deps...); err != nil {
					t.Fatal(err)
				}
			}
			err := registry.Start(t.Context())
			if !errors.Is(err, failure) {
				t.Fatalf("Start=%v", err)
			}
			if !startFailure && !errors.Is(err, closeFailure) {
				t.Fatalf("lost cleanup error: %v", err)
			}
			if robotClosed != 1 || mailClosed != 1 {
				t.Fatalf("cleanup robot=%d mail=%d", robotClosed, mailClosed)
			}
			if startFailure && (smsOpened != 1 || smsClosed != 1) {
				t.Fatalf("unstarted SMS opened=%d closed=%d", smsOpened, smsClosed)
			}
			if !startFailure && smsOpened != 0 {
				t.Fatal("factory after failed predecessor ran")
			}
			_ = registry.Stop(t.Context())
			if robotClosed != 1 || mailClosed != 1 {
				t.Fatal("rollback cleanup repeated")
			}
		})
	}
}

func TestNotificationDrainCancellationAndLateCallbacks(t *testing.T) {
	cfg := &configuration{value: &config.Config{News: config.NewsConfig{FSURL: "target"}}}
	var sends atomic.Int32
	r := robot.New(robot.Options{Configuration: cfg, Open: func(context.Context, config.NewsConfig) (news.WebhookSender, func(context.Context) error, error) {
		return webhookFunc(func(context.Context, string, string) error { sends.Add(1); return nil }), nil, nil
	}})
	if err := r.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	old := notification.New(notification.Options{Configuration: cfg, Robot: r, Report: func(news.Delivery) {}})
	fresh := notification.New(notification.Options{Configuration: cfg, Robot: r, Report: func(news.Delivery) {}})
	for _, p := range []*notification.Plugin{old, fresh} {
		if err := p.Init(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := p.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
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
	if err := old.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	old.Warn("target", "SDK late callback")
	if sends.Load() != 2 {
		t.Fatal("stopped composite touched channel")
	}
	if err := fresh.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCompositeAndChannelKeepNonCooperatingResources(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := &configuration{value: &config.Config{News: config.NewsConfig{FSURL: "target"}}}
		entered, release := make(chan struct{}), make(chan struct{})
		var closes atomic.Int32
		r := robot.New(robot.Options{Configuration: cfg, Open: func(context.Context, config.NewsConfig) (news.WebhookSender, func(context.Context) error, error) {
			return webhookFunc(func(context.Context, string, string) error { close(entered); <-release; return nil }), func(context.Context) error { closes.Add(1); return nil }, nil
		}})
		p := notification.New(notification.Options{Configuration: cfg, Robot: r, Report: func(news.Delivery) {}})
		for _, component := range []core.Plugin{r, p} {
			if err := component.Init(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := component.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		var workers sync.WaitGroup
		workers.Go(func() { _ = p.Notify(t.Context(), "", "body") })
		<-entered
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := p.Stop(ctx); !errors.Is(err, news.ErrNotDrained) {
			t.Fatalf("composite Stop=%v", err)
		}
		if err := r.Stop(ctx); !errors.Is(err, news.ErrNotDrained) {
			t.Fatalf("channel Stop=%v", err)
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
	cfg := &configuration{value: enabledConfig()}
	r := robot.New(robot.Options{Configuration: cfg})
	m := mail.New(mail.Options{Configuration: cfg})
	s := sms.New(sms.Options{Configuration: cfg})
	for _, component := range []core.Plugin{r, m, s} {
		if err := component.Init(t.Context()); err != nil {
			t.Fatalf("%s Init: %v", component.Name(), err)
		}
		if err := component.Start(t.Context()); err != nil {
			t.Fatalf("%s Start: %v", component.Name(), err)
		}
	}
	if r.Robot() == nil || m.Mail() == nil || s.SMS() == nil {
		t.Fatal("default capability not published")
	}
	for _, component := range []core.Plugin{r, m, s} {
		if err := component.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

type cancelingConfiguration struct {
	config *config.Config
	cancel context.CancelFunc
}

func (c cancelingConfiguration) Value() (*config.Config, error) { c.cancel(); return c.config, nil }

func TestCanceledConfigurationReadPreventsFactory(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	r := robot.New(robot.Options{Configuration: cancelingConfiguration{config: enabledConfig(), cancel: cancel}, Open: func(context.Context, config.NewsConfig) (news.WebhookSender, func(context.Context) error, error) {
		calls++
		return webhookFunc(func(context.Context, string, string) error { return nil }), nil, nil
	}})
	if err := r.Init(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("factory called after canceled configuration read")
	}
	if err := r.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}
