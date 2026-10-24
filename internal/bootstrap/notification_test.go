package bootstrap

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/platform/logging"
	"github.com/gtkit/gin-api/internal/platform/notification/dispatch"
	"github.com/gtkit/gin-api/internal/platform/notification/mail"
	"github.com/gtkit/gin-api/internal/platform/notification/robot"
	"github.com/gtkit/gin-api/internal/platform/notification/sms"
	"github.com/gtkit/gin-api/internal/testutil"
	"github.com/gtkit/logger/v2"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type notificationTrace struct {
	mu     sync.Mutex
	events []string
}

func (r *notificationTrace) add(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}
func (r *notificationTrace) stops() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []string
	for _, event := range r.events {
		if name, ok := strings.CutPrefix(event, "stop:"); ok {
			result = append(result, name)
		}
	}
	return result
}

type notificationSender struct{ sends *atomic.Int32 }

func (s notificationSender) Send(context.Context, string, string) error { s.sends.Add(1); return nil }

type notificationMailSender struct{ sends *atomic.Int32 }

func (s notificationMailSender) Send(context.Context, news.MailMessage) error {
	s.sends.Add(1)
	return nil
}

type notificationSMSSender struct{ sends *atomic.Int32 }

func (s notificationSMSSender) Send(context.Context, string, string, map[string]string) error {
	s.sends.Add(1)
	return nil
}

type notificationFixture struct {
	base            *lifecycleFixture
	trace           notificationTrace
	logging         *logging.Owner
	loggingMain     *logger.Logger
	notification    *dispatch.Owner
	options         Options
	sends, cleanups [3]atomic.Int32
}

func newNotificationFixture(t *testing.T) *notificationFixture {
	t.Helper()
	f := &notificationFixture{base: newLifecycleFixture(t)}
	cfg := f.base.components[0].cfg
	cfg.Log.ConsoleStdout = 1
	cfg.News.FSURL = "fixture-target"
	cfg.Email = config.EmailConfig{Host: "fixture", AlertTo: []string{"fixture-recipient"}}
	cfg.SMS = config.SMSConfig{Provider: "aliyun", AlertTemplate: "fixture", AlertPhones: []string{"fixture-phone"}}
	cleanup := func(i int) func(context.Context) error {
		return func(ctx context.Context) error {
			if f.logging != nil && logger.Default() == f.loggingMain {
				t.Error("channel closed before logging drained")
			}
			if f.notification != nil {
				if err := f.notification.Notify(ctx, "", "cleanup probe"); !errors.Is(err, news.ErrStopped) {
					t.Errorf("channel closed before notification admission stopped: %v", err)
				}
			}
			f.trace.add("stop:" + []string{"robot", "mail", "sms"}[i])
			f.cleanups[i].Add(1)
			return nil
		}
	}
	factories := f.base.factories()
	factories.logs = func(ctx context.Context, cfg *config.Config) (logOwner, error) {
		p := &processLogs{}
		var err error
		p.robot, err = robot.Open(ctx, robot.Options{Config: cfg, Open: func(context.Context, config.NewsConfig) (news.WebhookSender, func(context.Context) error, error) {
			return notificationSender{&f.sends[0]}, cleanup(0), nil
		}})
		if err != nil {
			return p, err
		}
		p.mail, err = mail.Open(ctx, mail.Options{Config: cfg, Open: func(context.Context, config.EmailConfig) (news.MailSender, func(context.Context) error, error) {
			return notificationMailSender{&f.sends[1]}, cleanup(1), nil
		}})
		if err != nil {
			return p, err
		}
		p.sms, err = sms.Open(ctx, sms.Options{Config: cfg, Open: func(context.Context, config.SMSConfig) (news.SMSSender, func(context.Context) error, error) {
			return notificationSMSSender{&f.sends[2]}, cleanup(2), nil
		}})
		if err != nil {
			return p, err
		}
		p.notification, err = dispatch.Open(ctx, dispatch.Options{Config: cfg, Robot: p.robot, Mail: p.mail, SMS: p.sms})
		f.notification = p.notification
		if err != nil {
			return p, err
		}
		p.logger, err = logging.Open(ctx, logging.Options{Config: cfg, Notification: p.notification})
		f.logging = p.logger
		if p.logger != nil {
			f.loggingMain = logger.Default()
		}
		return p, err
	}
	f.options = Options{factories: &factories}
	return f
}

func (f *notificationFixture) assertClosed(t *testing.T) {
	t.Helper()
	stops := f.trace.stops()
	if !slices.Equal(stops, []string{"sms", "mail", "robot"}) {
		t.Fatalf("channel close order=%v", stops)
	}
	for i := range f.cleanups {
		if f.cleanups[i].Load() != 1 {
			t.Fatalf("channel %d cleanups=%d, want one", i, f.cleanups[i].Load())
		}
	}
}

func TestNotificationRuntimeRollsBackAndDrainsBeforeChannels(t *testing.T) {
	for _, failure := range []string{"none", "open", "open with alert"} {
		t.Run(failure, func(t *testing.T) {
			f := newNotificationFixture(t)
			cause := errors.New("fixture failure")
			if failure == "open" {
				f.base.components[2].init = func(context.Context) error { return cause }
			}
			if failure == "open with alert" {
				f.base.components[2].init = func(context.Context) error { logger.HError("startup failure alert"); return cause }
			}
			r, err := NewRuntime(t.Context(), f.options)
			if failure == "none" {
				if err != nil {
					t.Fatal(err)
				}
				logger.HError("shutdown alert")
				if err := r.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := r.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
			} else if r != nil || !errors.Is(err, cause) {
				t.Fatalf("runtime=%v err=%v", r, err)
			}
			f.assertClosed(t)
			want := int32(1)
			if failure == "open" {
				want = 0
			}
			for i := range f.sends {
				if f.sends[i].Load() != want {
					t.Fatalf("channel %d sends=%d want=%d", i, f.sends[i].Load(), want)
				}
			}
			if err := f.notification.Notify(t.Context(), "", "late"); !errors.Is(err, news.ErrStopped) {
				t.Fatalf("late notify=%v", err)
			}
		})
	}
}

func TestNotificationAppExitErrorIsSentBeforeCleanup(t *testing.T) {
	testutil.RequireLoopback(t)
	f := newNotificationFixture(t)
	r, err := NewRuntime(t.Context(), f.options)
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("worker prepare failed")
	app := newTestApp(t, newTestHTTPServer(), &funcWorker{name: "notification-worker", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true, prepare: func(context.Context) error { return cause }})
	app.runtime = r
	if err := app.Run(t.Context()); !errors.Is(err, cause) {
		t.Fatalf("run=%v", err)
	}
	f.assertClosed(t)
	for i := range f.sends {
		if f.sends[i].Load() != 1 {
			t.Fatalf("exit channel %d sends=%d", i, f.sends[i].Load())
		}
	}
}

func TestNewAppNotificationRollbackPreservesCleanupErrors(t *testing.T) {
	f := newNotificationFixture(t)
	cfg := f.base.components[0].cfg
	cfg.Auth.Mode = config.AuthModeExternal
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database := f.base.components[2]
	database.db, database.sql = db, sqlDB
	closing := errors.New("database close failed")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cfg.Proxy.TrustedProxies = []string{"invalid-proxy"}
	database.stop = func(ctx context.Context) error {
		cancel()
		if ctx.Err() != nil {
			t.Error("rollback inherited startup cancellation")
		}
		return errors.Join(sqlDB.Close(), closing)
	}
	mock.ExpectClose()
	app, err := NewApp(ctx, f.options)
	if parseErr, ok := errors.AsType[*net.ParseError](err); app != nil || !ok || parseErr == nil || !errors.Is(err, closing) || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("app=%v err=%v", app, err)
	}
	f.assertClosed(t)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationLiveWorkerOnlyDrainsLogging(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newNotificationFixture(t)
		r, err := NewRuntime(t.Context(), f.options)
		if err != nil {
			t.Fatal(err)
		}
		release := make(chan struct{})
		releaseWorker := sync.OnceFunc(func() { close(release) })
		defer releaseWorker()
		worker := &funcWorker{name: "still-running", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true, run: func(context.Context) error { <-release; return nil }}
		manager, err := NewWorkerManager(testWorkerBudget(), worker)
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		stopCtx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := manager.StopKind(stopCtx, KindConsumer); !errors.Is(err, ErrWorkerNotExited) {
			t.Fatalf("stop worker=%v", err)
		}
		app := &App{runtime: r, workers: manager, shutdown: NewShutdownCoordinator()}
		logger.HError("live worker exit alert")
		if err := app.closeResources(t.Context()); !errors.Is(err, ErrWorkerNotExited) {
			t.Fatalf("close resources=%v", err)
		}
		if len(f.trace.stops()) != 0 {
			t.Fatalf("notification channels closed while worker alive: %v", f.trace.stops())
		}
		for i := range f.cleanups {
			if f.cleanups[i].Load() != 0 {
				t.Fatal("closed live worker resource")
			}
		}
		if err := f.notification.Notify(t.Context(), "", "still available"); err != nil {
			t.Fatal(err)
		}
		releaseWorker()
		synctest.Wait()
		if err := manager.StopAll(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := app.closeResources(t.Context()); err != nil {
			t.Fatal(err)
		}
		f.assertClosed(t)
	})
}
