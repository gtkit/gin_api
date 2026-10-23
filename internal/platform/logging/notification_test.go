package logging

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	notification "github.com/gtkit/gin-api/internal/platform/notification/dispatch"
	"github.com/gtkit/gin-api/internal/platform/notification/robot"
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
	p := mustOpen(t, Options{Config: cfg})
	logger.HError(fmt.Sprintf("local alert"))
	if err := p.Close(t.Context()); err != nil {
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
	p := mustOpen(t, Options{Config: &config.Config{Log: config.LogConfig{ConsoleStdout: 1}}, Notification: sink})
	t.Cleanup(func() {
		unblock()
		if err := p.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	logger.HError(fmt.Sprintf("first"))
	<-entered
	for range 64 {
		logger.HError(fmt.Sprintf("queued"))
	}
	logger.HError(fmt.Sprintf("overflow"))
	if got := p.main.DroppedMessages(); got != 1 {
		t.Fatalf("queue dropped=%d, want one after 64 pending", got)
	}
	unblock()
	if err := p.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 65 {
		t.Fatalf("delivered=%d want first plus 64 queued", calls.Load())
	}
}

func TestTypedNilNotificationFailsBeforeConstruction(t *testing.T) {
	var sink *callbackNotification
	var constructions int
	p, err := Open(t.Context(), Options{Config: &config.Config{}, Notification: sink, MainFactory: func(...logger.Option) (*logger.Logger, error) { constructions++; return nil, nil }})
	if p != nil || !errors.Is(err, news.ErrSenderMissing) {
		t.Fatalf("init=%v", err)
	}
	if constructions != 0 {
		t.Fatal("constructed logger with typed nil notification")
	}
}

func TestDrainAndPartialOpenBindTheRealCleanupContext(t *testing.T) {
	failure := errors.New("factory failure")
	for _, stage := range []string{"stop", "partial-main", "partial-access", "panic-access"} {
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
			p, err := Open(ctx, Options{Config: &config.Config{Log: config.LogConfig{ConsoleStdout: 1}}, Notification: sink,
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
					if stage == "panic-access" {
						panic("access factory")
					}
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
			if stage == "stop" {
				if err != nil {
					t.Fatal(err)
				}
			} else if stage == "panic-access" {
				if p != nil || !errors.Is(err, core.ErrComponentState) {
					t.Fatalf("panic cleanup: owner=%v err=%v", p, err)
				}
			} else if p != nil || !errors.Is(err, failure) {
				t.Fatalf("init=%v", err)
			}
			if err := p.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if p != nil {
				if err := p.Close(ctx); err != nil {
					t.Fatal(err)
				}
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
		configuration := &config.Config{Log: config.LogConfig{ConsoleStdout: 1}, News: config.NewsConfig{FSURL: "test-target"}}
		entered := make(chan struct{})
		var cleanup atomic.Int32
		channel := mustOpenRobot(t, robot.Options{Config: configuration, Open: func(context.Context, config.NewsConfig) (news.WebhookSender, func(context.Context) error, error) {
			return webhookFunc(func(ctx context.Context, _, _ string) error { close(entered); <-ctx.Done(); return ctx.Err() }), func(context.Context) error { cleanup.Add(1); return nil }, nil
		}})
		notifier := mustOpenNotification(t, notification.Options{Config: configuration, Robot: channel})
		p := mustOpen(t, Options{Config: configuration, Notification: notifier})

		logger.HError(fmt.Sprintf("in flight"))
		<-entered
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := p.Close(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("drain=%v", err)
		}
		if cleanup.Load() != 0 {
			t.Fatal("logging closed a borrowed channel")
		}
		if err := notifier.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := channel.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		if cleanup.Load() != 1 {
			t.Fatal("sender not cleaned exactly once")
		}
	})
}

type delayedMessager struct {
	sink             *notification.Owner
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
		configuration := &config.Config{Log: config.LogConfig{ConsoleStdout: 1}, News: config.NewsConfig{FSURL: "test-target"}}
		var sends, cleanups atomic.Int32
		channel := mustOpenRobot(t, robot.Options{Config: configuration, Open: func(context.Context, config.NewsConfig) (news.WebhookSender, func(context.Context) error, error) {
			return webhookFunc(func(context.Context, string, string) error { sends.Add(1); return nil }), func(context.Context) error { cleanups.Add(1); return nil }, nil
		}})
		notifier := mustOpenNotification(t, notification.Options{Config: configuration, Robot: channel})
		delayed := &delayedMessager{sink: notifier, entered: make(chan struct{}), release: make(chan struct{})}
		p := mustOpen(t, Options{Config: configuration, Notification: notifier, MainFactory: func(opts ...logger.Option) (*logger.Logger, error) {
			return logger.New(append(opts, logger.WithMessager(delayed))...)
		}})

		logger.HError(fmt.Sprintf("first"))
		<-delayed.entered
		logger.HError(fmt.Sprintf("queued"))
		done := make(chan error, 1)
		var group sync.WaitGroup
		group.Go(func() { done <- p.Close(t.Context()) })
		synctest.Sleep(6 * time.Second)
		stopChannels := func() {
			if err := notifier.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := channel.Close(t.Context()); err != nil {
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
	// SDK defaults capture their output at construction. Select a real previous
	// instance here so restoration is observable without mutating SDK internals.
	previousDefault := logger.Default()
	previousStdout := os.Stdout
	os.Stdout = stderr
	restored := logger.MustNew(logger.WithConsole(true), logger.WithFile(false), logger.WithReplaceGlobals(false))
	os.Stdout = previousStdout
	logger.SetDefault(restored)
	t.Cleanup(func() {
		logger.SetDefault(previousDefault)
		restored.Sync()
		os.Stderr = previous
		if err := stderr.Close(); err != nil {
			t.Error(err)
		}
	})
	configuration := &config.Config{Log: config.LogConfig{Path: filepath.Join(dir, "log"), FileStdout: 1}, News: config.NewsConfig{FSURL: "test-target"}}
	var sends atomic.Int32
	secret := "https://private.invalid/token recipient@example.invalid 13812345678"
	channel := mustOpenRobot(t, robot.Options{Config: configuration, Open: func(context.Context, config.NewsConfig) (news.WebhookSender, func(context.Context) error, error) {
		return webhookFunc(func(context.Context, string, string) error { sends.Add(1); return errors.New(secret) }), nil, nil
	}})
	notifier := mustOpenNotification(t, notification.Options{Config: configuration, Robot: channel})
	p := mustOpen(t, Options{Config: configuration, Notification: notifier})

	if err := notifier.Notify(t.Context(), "", "first"); err == nil {
		t.Fatal("provider failure disappeared")
	}
	if err := p.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The native default has been restored before closing: late diagnostics use it.
	if err := notifier.Notify(t.Context(), "", "second"); err == nil {
		t.Fatal("provider failure disappeared after drain")
	}
	if err := notifier.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := channel.Close(t.Context()); err != nil {
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

func mustOpenRobot(t *testing.T, options robot.Options) *robot.Owner {
	t.Helper()
	p, err := robot.Open(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func mustOpenNotification(t *testing.T, options notification.Options) *notification.Owner {
	t.Helper()
	p, err := notification.Open(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAbnormalFlushDoesNotDrainOrCancelNotification(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		var first sync.Once
		var binds, delivered atomic.Int32
		sink := &callbackNotification{
			warn: func(string, ...string) { first.Do(func() { close(entered) }); <-release; delivered.Add(1) },
			bind: func(context.Context) func() { binds.Add(1); return func() {} },
		}
		// logrotate owns a process-global worker, so this synctest bubble uses
		// console output; file flush behavior is covered outside the bubble.
		cfg := &config.Config{Log: config.LogConfig{ConsoleStdout: 1}}
		p := mustOpen(t, Options{Config: cfg, Notification: sink})
		main := logger.Default()
		logger.HError("before flush")
		<-entered
		canceled, cancel := context.WithCancel(t.Context())
		cancel()
		if err := p.Drain(canceled); !errors.Is(err, context.Canceled) {
			t.Fatalf("flush=%v", err)
		}
		if binds.Load() != 0 || delivered.Load() != 0 || logger.Default() != main {
			t.Fatal("flush drained or canceled notification")
		}
		logger.HError("after flush")
		unblock()
		if err := p.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		if delivered.Load() != 2 || binds.Load() != 1 {
			t.Fatalf("delivered=%d binds=%d", delivered.Load(), binds.Load())
		}
	})
}
