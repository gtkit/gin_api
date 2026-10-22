package logging

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/plugin/builtin/notification"
	"github.com/gtkit/gin-api/internal/plugin/builtin/robot"
	"github.com/gtkit/logger/v2"
)

type callbackNotification struct {
	warn func(string, ...string)
	bind func(context.Context) func()
}

func (n *callbackNotification) Warn(target string, args ...string) { n.warn(target, args...) }
func (n *callbackNotification) BindDrain(ctx context.Context) func() {
	if n.bind != nil {
		return n.bind(ctx)
	}
	return func() {}
}

func TestPureLoggingDoesNotConstructNotificationClients(t *testing.T) {
	// These notification settings cannot construct default clients. A logging-only
	// composition must neither validate them nor create any outbound resources.
	cfg := &config.Config{Log: config.LogConfig{ConsoleStdout: 1}, News: config.NewsConfig{Platform: "unknown"}, Email: config.EmailConfig{Host: "invalid"}, SMS: config.SMSConfig{Provider: "unknown"}}
	p := New(Options{Configuration: fixedConfiguration{cfg}})
	if err := p.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	applog.HErrorf("local alert")
	if err := p.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAlertQueueRemainsBoundedAt64(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var first sync.Once
	var calls atomic.Int32
	unblock := sync.OnceFunc(func() { close(release) })
	sink := &callbackNotification{warn: func(string, ...string) {
		first.Do(func() { close(entered) })
		<-release
		calls.Add(1)
	}}
	p := New(Options{Configuration: fixedConfiguration{&config.Config{Log: config.LogConfig{ConsoleStdout: 1}}}, Notification: sink})
	if err := p.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		unblock()
		if err := p.Stop(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	applog.HErrorf("first")
	<-entered
	for range 64 {
		applog.HErrorf("queued")
	}
	applog.HErrorf("overflow")
	if got := p.main.DroppedMessages(); got != 1 {
		t.Fatalf("queue dropped=%d, want one after 64 pending", got)
	}
	unblock()
	if err := p.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 65 {
		t.Fatalf("delivered=%d want first plus 64 queued", calls.Load())
	}
}

func TestTypedNilNotificationFailsBeforeConstruction(t *testing.T) {
	var sink *callbackNotification
	var constructions int
	p := New(Options{Configuration: fixedConfiguration{&config.Config{}}, Notification: sink, MainFactory: func(...logger.Option) (*logger.Logger, error) { constructions++; return nil, nil }})
	if err := p.Init(t.Context()); !errors.Is(err, news.ErrSenderMissing) {
		t.Fatalf("init=%v", err)
	}
	if constructions != 0 {
		t.Fatal("constructed logger with typed nil notification")
	}
}

func TestDrainAndPartialInitBindTheRealCleanupContext(t *testing.T) {
	failure := errors.New("factory failure")
	for _, stage := range []string{"stop", "partial-main", "partial-access"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			bound := make(chan struct{})
			var binds, releases, calls atomic.Int32
			sink := &callbackNotification{
				warn: func(string, ...string) { <-bound; calls.Add(1) },
				bind: func(got context.Context) func() {
					if got != ctx {
						t.Error("cleanup context was replaced")
					}
					binds.Add(1)
					close(bound)
					return func() { releases.Add(1) }
				},
			}
			p := New(Options{Configuration: fixedConfiguration{&config.Config{Log: config.LogConfig{ConsoleStdout: 1}}}, Notification: sink,
				MainFactory: func(opts ...logger.Option) (*logger.Logger, error) {
					instance, err := logger.New(opts...)
					if err != nil {
						return nil, err
					}
					instance.HError("queued before drain")
					if stage == "partial-main" {
						return instance, failure
					}
					return instance, nil
				},
				AccessFactory: func(opts ...logger.Option) (*logger.Logger, error) {
					instance, err := logger.New(opts...)
					if err != nil {
						return nil, err
					}
					if stage == "partial-access" {
						return instance, failure
					}
					return instance, nil
				},
			})
			err := p.Init(ctx)
			if stage == "stop" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, failure) {
				t.Fatalf("init=%v", err)
			}
			if err := p.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			if err := p.Drain(ctx); err != nil {
				t.Fatal(err)
			}
			if binds.Load() != 1 || releases.Load() != 1 || calls.Load() != 1 {
				t.Fatalf("bind/release/calls=%d/%d/%d", binds.Load(), releases.Load(), calls.Load())
			}
		})
	}
}

type webhookFunc func(context.Context, string, string) error

func (f webhookFunc) Send(ctx context.Context, target, text string) error {
	return f(ctx, target, text)
}

func TestCleanupCancellationReachesRealQueuedSend(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		configuration := fixedConfiguration{&config.Config{Log: config.LogConfig{ConsoleStdout: 1}, News: config.NewsConfig{FSURL: "test-target"}}}
		entered := make(chan struct{})
		var cleanup atomic.Int32
		channel := robot.New(robot.Options{Configuration: configuration, Open: func(context.Context, config.NewsConfig) (news.WebhookSender, func(context.Context) error, error) {
			return webhookFunc(func(ctx context.Context, _, _ string) error { close(entered); <-ctx.Done(); return ctx.Err() }), func(context.Context) error { cleanup.Add(1); return nil }, nil
		}})
		notifier := notification.New(notification.Options{Configuration: configuration, Robot: channel})
		p := New(Options{Configuration: configuration, Notification: notifier})
		for _, component := range []interface{ Init(context.Context) error }{channel, notifier, p} {
			if err := component.Init(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		for _, component := range []interface{ Start(context.Context) error }{channel, notifier, p} {
			if err := component.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		applog.HErrorf("in flight")
		<-entered
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := p.Drain(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("drain=%v", err)
		}
		if cleanup.Load() != 0 {
			t.Fatal("logging closed a borrowed channel")
		}
		if err := notifier.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := channel.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		if cleanup.Load() != 1 {
			t.Fatal("sender not cleaned exactly once")
		}
	})
}

type delayedMessager struct {
	sink             *notification.Plugin
	entered, release chan struct{}
	once             sync.Once
}

func (m *delayedMessager) Send(text string) {
	m.once.Do(func() { close(m.entered) })
	<-m.release
	m.sink.Warn("", text)
}
func (m *delayedMessager) SendTo(target, text string) { m.Send(text) }

func TestResolvedSDKLateCallbacksCannotUseStoppedChannels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		configuration := fixedConfiguration{&config.Config{Log: config.LogConfig{ConsoleStdout: 1}, News: config.NewsConfig{FSURL: "test-target"}}}
		var sends, cleanups atomic.Int32
		channel := robot.New(robot.Options{Configuration: configuration, Open: func(context.Context, config.NewsConfig) (news.WebhookSender, func(context.Context) error, error) {
			return webhookFunc(func(context.Context, string, string) error { sends.Add(1); return nil }), func(context.Context) error { cleanups.Add(1); return nil }, nil
		}})
		notifier := notification.New(notification.Options{Configuration: configuration, Robot: channel})
		delayed := &delayedMessager{sink: notifier, entered: make(chan struct{}), release: make(chan struct{})}
		p := New(Options{Configuration: configuration, Notification: notifier, MainFactory: func(opts ...logger.Option) (*logger.Logger, error) {
			return logger.New(append(opts, logger.WithMessager(delayed))...)
		}})
		for _, component := range []interface {
			Init(context.Context) error
			Start(context.Context) error
		}{channel, notifier, p} {
			if err := component.Init(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := component.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		applog.HErrorf("first")
		<-delayed.entered
		applog.HErrorf("queued")
		done := make(chan error, 1)
		var group sync.WaitGroup
		group.Go(func() { done <- p.Drain(t.Context()) })
		synctest.Sleep(6 * time.Second)
		stopChannels := func() {
			if err := notifier.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := channel.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			t.Log("resolved SDK returned while its callback was blocked")
			stopChannels()
			close(delayed.release)
			group.Wait()
			synctest.Wait()
			if sends.Load() != 0 {
				t.Fatal("late callback touched closed sender")
			}
		default:
			t.Log("resolved SDK waits for callback completion")
			close(delayed.release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			group.Wait()
			if sends.Load() != 2 {
				t.Fatalf("pre-stop deliveries=%d", sends.Load())
			}
			stopChannels()
		}
		before := sends.Load()
		delayed.Send("after stop")
		if sends.Load() != before || cleanups.Load() != 1 {
			t.Fatal("stopped sender reused or cleanup count changed")
		}
	})
}

func TestNotificationFailureDiagnosticsAreRedactedAndNonrecursive(t *testing.T) {
	dir := t.TempDir()
	stderr, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = stderr
	t.Cleanup(func() {
		os.Stderr = previous
		if err := stderr.Close(); err != nil {
			t.Error(err)
		}
	})
	configuration := fixedConfiguration{&config.Config{Log: config.LogConfig{Path: filepath.Join(dir, "log"), FileStdout: 1}, News: config.NewsConfig{FSURL: "test-target"}}}
	var sends atomic.Int32
	secret := "https://private.invalid/token recipient@example.invalid 13812345678"
	channel := robot.New(robot.Options{Configuration: configuration, Open: func(context.Context, config.NewsConfig) (news.WebhookSender, func(context.Context) error, error) {
		return webhookFunc(func(context.Context, string, string) error { sends.Add(1); return errors.New(secret) }), nil, nil
	}})
	notifier := notification.New(notification.Options{Configuration: configuration, Robot: channel})
	p := New(Options{Configuration: configuration, Notification: notifier})
	for _, component := range []interface {
		Init(context.Context) error
		Start(context.Context) error
	}{channel, notifier, p} {
		if err := component.Init(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := component.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if err := notifier.Notify(t.Context(), "", "first"); err == nil {
		t.Fatal("provider failure disappeared")
	}
	if err := p.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The facade has been released: diagnostics now use its ordinary fallback.
	if err := notifier.Notify(t.Context(), "", "second"); err == nil {
		t.Fatal("provider failure disappeared after drain")
	}
	if err := notifier.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := channel.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if sends.Load() != 2 {
		t.Fatalf("recursive or missing sends=%d", sends.Load())
	}
	contents := readLogs(t, dir)
	if strings.Contains(contents, secret) || strings.Contains(contents, "recipient@example.invalid") || strings.Contains(contents, "13812345678") {
		t.Fatal("diagnostic leaked a private target or error")
	}
	if strings.Count(contents, "provider_error") != 2 {
		t.Fatalf("ordinary and fallback diagnostics=%q", contents)
	}
}
