package notification_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/notification"
)

type channelSender func(context.Context, string, string) error

func (f channelSender) Send(ctx context.Context, target, text string) error {
	return f(ctx, target, text)
}

func TestNotificationChannelAdmissionAndSingleCleanup(t *testing.T) {
	var channel notification.NotificationChannel[news.WebhookSender]
	if err := channel.Start(t.Context()); !errors.Is(err, core.ErrComponentState) {
		t.Fatalf("Start before Init = %v", err)
	}
	var opens, sends, closes int
	open := func(context.Context) (notification.NotificationClient[news.WebhookSender], bool, error) {
		opens++
		return notification.NotificationClient[news.WebhookSender]{
			Sender:  channelSender(func(context.Context, string, string) error { sends++; return nil }),
			Cleanup: func(context.Context) error { closes++; return nil },
		}, true, nil
	}
	for range 2 {
		if err := channel.Init(t.Context(), open); err != nil {
			t.Fatal(err)
		}
	}
	if !channel.Configured() || opens != 1 {
		t.Fatalf("configured=%v opens=%d", channel.Configured(), opens)
	}
	use := func(ctx context.Context) error {
		return channel.Use(ctx, func(ctx context.Context, sender news.WebhookSender) error { return sender.Send(ctx, "target", "text") })
	}
	if err := use(t.Context()); !errors.Is(err, news.ErrNotRunning) {
		t.Fatalf("pre-start Send = %v", err)
	}
	if err := channel.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := use(t.Context()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := channel.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if sends != 1 || closes != 1 {
		t.Fatalf("sends=%d closes=%d", sends, closes)
	}
	if err := use(t.Context()); !errors.Is(err, news.ErrStopped) {
		t.Fatalf("post-stop Send = %v", err)
	}
	if err := channel.Init(t.Context(), open); !errors.Is(err, news.ErrStopped) {
		t.Fatalf("restart Init = %v", err)
	}
	if err := channel.Start(t.Context()); !errors.Is(err, news.ErrStopped) {
		t.Fatalf("restart Start = %v", err)
	}
}

func TestNotificationChannelPartialFactoryResults(t *testing.T) {
	openErr, closeErr := errors.New("open failure"), errors.New("close failure")
	for _, tc := range []struct {
		name                      string
		canceled, typedNil, panic bool
	}{
		{name: "error"}, {name: "late cancellation", canceled: true}, {name: "typed nil", typedNil: true}, {name: "panic", panic: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var channel notification.NotificationChannel[news.WebhookSender]
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			closes := 0
			err := channel.Init(ctx, func(context.Context) (notification.NotificationClient[news.WebhookSender], bool, error) {
				if tc.panic {
					panic("sensitive factory details")
				}
				var sender news.WebhookSender = channelSender(func(context.Context, string, string) error { return nil })
				if tc.typedNil {
					var empty channelSender
					sender = empty
				}
				var failure error
				if tc.canceled {
					cancel()
				} else if !tc.typedNil {
					failure = openErr
				}
				return notification.NotificationClient[news.WebhookSender]{Sender: sender, Cleanup: func(cleanupCtx context.Context) error {
					closes++
					if cleanupCtx.Err() != nil {
						t.Error("partial cleanup inherited canceled Init")
					}
					if _, ok := cleanupCtx.Deadline(); !ok {
						t.Error("partial cleanup has no deadline")
					}
					return closeErr
				}}, true, failure
			})
			want := openErr
			if tc.canceled {
				want = context.Canceled
			}
			if tc.typedNil {
				want = news.ErrSenderMissing
			}
			if tc.panic {
				want = news.ErrSenderPanic
			}
			if !errors.Is(err, want) {
				t.Fatalf("Init = %v, want %v", err, want)
			}
			if closes != 0 || errors.Is(err, closeErr) {
				t.Fatalf("Init performed cleanup outside caller budget: err=%v closes=%d", err, closes)
			}
			cleanupCtx, finish := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer finish()
			for range 2 {
				stopErr := channel.Stop(cleanupCtx)
				if (!tc.panic && !errors.Is(stopErr, closeErr)) || (tc.panic && stopErr != nil) {
					t.Errorf("cleanup=%v", stopErr)
				}
			}
			wantCloses := 1
			if tc.panic {
				wantCloses = 0
			}
			if closes != wantCloses {
				t.Fatalf("cleanup calls=%d want=%d", closes, wantCloses)
			}
			if channel.Configured() {
				t.Fatal("failed channel was published")
			}
		})
	}
}

func TestNotificationChannelDisabled(t *testing.T) {
	var channel notification.NotificationChannel[news.WebhookSender]
	if err := channel.Init(t.Context(), func(context.Context) (notification.NotificationClient[news.WebhookSender], bool, error) {
		return notification.NotificationClient[news.WebhookSender]{}, false, nil
	}); err != nil {
		t.Fatal(err)
	}
	if channel.Configured() {
		t.Fatal("disabled sender published")
	}
	if err := channel.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	err := channel.Use(t.Context(), func(context.Context, news.WebhookSender) error { t.Error("disabled callback ran"); return nil })
	if !errors.Is(err, news.ErrSenderDisabled) {
		t.Fatal(err)
	}
	if err := channel.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationChannelStopWaitsForActiveSend(t *testing.T) {
	var channel notification.NotificationChannel[news.WebhookSender]
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var closes atomic.Int32
	err := channel.Init(t.Context(), func(context.Context) (notification.NotificationClient[news.WebhookSender], bool, error) {
		return notification.NotificationClient[news.WebhookSender]{Sender: channelSender(func(ctx context.Context, _, _ string) error {
			close(entered)
			<-ctx.Done()
			close(canceled)
			<-release
			return ctx.Err()
		}), Cleanup: func(context.Context) error { closes.Add(1); return nil }}, true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = channel.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	sent, stopped := make(chan error, 1), make(chan error, 1)
	var workers sync.WaitGroup
	workers.Go(func() {
		sent <- channel.Use(t.Context(), func(ctx context.Context, s news.WebhookSender) error { return s.Send(ctx, "", "text") })
	})
	<-entered
	workers.Go(func() { stopped <- channel.Stop(t.Context()) })
	<-canceled
	if closes.Load() != 0 {
		t.Error("closed resources while send was active")
	}
	if err := channel.Use(t.Context(), func(context.Context, news.WebhookSender) error { t.Error("late callback ran"); return nil }); !errors.Is(err, news.ErrStopped) {
		t.Error(err)
	}
	close(release)
	if err := <-sent; !errors.Is(err, context.Canceled) {
		t.Error(err)
	}
	if err := <-stopped; err != nil {
		t.Error(err)
	}
	workers.Wait()
	if closes.Load() != 1 {
		t.Fatalf("closes=%d", closes.Load())
	}
}

func TestNotificationChannelUncooperativeSendRetainsResource(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var channel notification.NotificationChannel[news.WebhookSender]
		entered, release := make(chan struct{}), make(chan struct{})
		var closes atomic.Int32
		if err := channel.Init(t.Context(), func(context.Context) (notification.NotificationClient[news.WebhookSender], bool, error) {
			return notification.NotificationClient[news.WebhookSender]{Sender: channelSender(func(context.Context, string, string) error { close(entered); <-release; return nil }), Cleanup: func(context.Context) error { closes.Add(1); return nil }}, true, nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := channel.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		var workers sync.WaitGroup
		workers.Go(func() {
			if err := channel.Use(t.Context(), func(ctx context.Context, s news.WebhookSender) error { return s.Send(ctx, "", "text") }); !errors.Is(err, context.Canceled) {
				t.Errorf("canceled send=%v", err)
			}
		})
		<-entered
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		err := channel.Stop(ctx)
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, news.ErrNotDrained) {
			t.Fatalf("Stop=%v", err)
		}
		if closes.Load() != 0 {
			t.Fatal("active resource was closed")
		}
		close(release)
		workers.Wait()
		if err := channel.Stop(t.Context()); !errors.Is(err, news.ErrNotDrained) {
			t.Fatalf("repeated Stop=%v", err)
		}
		if closes.Load() != 0 {
			t.Fatal("failed Stop silently retried cleanup")
		}
	})
}

func TestNotificationChannelDrainReleaseAndInitContext(t *testing.T) {
	var channel notification.NotificationChannel[news.WebhookSender]
	initCtx, initCancel := context.WithCancel(t.Context())
	if err := channel.Init(initCtx, func(context.Context) (notification.NotificationClient[news.WebhookSender], bool, error) {
		return notification.NotificationClient[news.WebhookSender]{Sender: channelSender(func(context.Context, string, string) error { return nil })}, true, nil
	}); err != nil {
		t.Fatal(err)
	}
	initCancel()
	if err := channel.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	call := func() error {
		return channel.Use(t.Context(), func(ctx context.Context, s news.WebhookSender) error { return s.Send(ctx, "", "text") })
	}
	drain, cancel := context.WithCancel(t.Context())
	release := channel.BindDrain(drain)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(release)
	}
	workers.Wait()
	cancel()
	if err := call(); err != nil {
		t.Fatalf("released or Init cancellation leaked: %v", err)
	}
	expired, expire := context.WithCancel(t.Context())
	expire()
	release = channel.BindDrain(expired)
	if err := call(); !errors.Is(err, context.Canceled) {
		t.Fatalf("expired drain admitted send: %v", err)
	}
	release()
	release()
	if err := channel.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationChannelPanicReturnsAdmission(t *testing.T) {
	var channel notification.NotificationChannel[news.WebhookSender]
	if err := channel.Init(t.Context(), func(context.Context) (notification.NotificationClient[news.WebhookSender], bool, error) {
		return notification.NotificationClient[news.WebhookSender]{Sender: channelSender(func(context.Context, string, string) error { return nil })}, true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := channel.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := channel.Use(t.Context(), func(context.Context, news.WebhookSender) error { panic("private") }); !errors.Is(err, news.ErrSenderPanic) {
		t.Fatal(err)
	}
	if err := channel.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationChannelConcurrentStopSharesCleanup(t *testing.T) {
	var channel notification.NotificationChannel[news.WebhookSender]
	entered, release := make(chan struct{}), make(chan struct{})
	var closes atomic.Int32
	if err := channel.Init(t.Context(), func(context.Context) (notification.NotificationClient[news.WebhookSender], bool, error) {
		return notification.NotificationClient[news.WebhookSender]{Sender: channelSender(func(context.Context, string, string) error { return nil }), Cleanup: func(context.Context) error {
			closes.Add(1)
			close(entered)
			<-release
			return nil
		}}, true, nil
	}); err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	var workers sync.WaitGroup
	workers.Go(func() { first <- channel.Stop(t.Context()) })
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := channel.Stop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting Stop=%v", err)
	}
	if closes.Load() != 1 {
		t.Fatal("concurrent Stop duplicated cleanup")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	workers.Wait()
	if err := channel.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationChannelLateFactoryResultRetainedAfterExpiredStop(t *testing.T) {
	var channel notification.NotificationChannel[news.WebhookSender]
	entered, release := make(chan struct{}), make(chan struct{})
	var closes atomic.Int32
	initialized := make(chan error, 1)
	var workers sync.WaitGroup
	workers.Go(func() {
		initialized <- channel.Init(t.Context(), func(context.Context) (notification.NotificationClient[news.WebhookSender], bool, error) {
			close(entered)
			<-release
			return notification.NotificationClient[news.WebhookSender]{Sender: channelSender(func(context.Context, string, string) error { return nil }), Cleanup: func(context.Context) error { closes.Add(1); return nil }}, true, nil
		})
	})
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := channel.Stop(ctx); !errors.Is(err, news.ErrNotDrained) {
		t.Fatal(err)
	}
	close(release)
	if err := <-initialized; !errors.Is(err, news.ErrStopped) {
		t.Fatalf("late Init=%v", err)
	}
	workers.Wait()
	// An expired Stop retains ownership for process-exit reclamation; Init
	// must not start a new cleanup budget after the caller has stopped waiting.
	if channel.Configured() || closes.Load() != 0 {
		t.Fatalf("configured=%v closes=%d", channel.Configured(), closes.Load())
	}
}
