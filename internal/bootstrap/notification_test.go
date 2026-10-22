package bootstrap

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/core"
	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/plugin"
	"github.com/gtkit/gin-api/internal/plugin/builtin/logging"
	"github.com/gtkit/gin-api/internal/plugin/builtin/mail"
	"github.com/gtkit/gin-api/internal/plugin/builtin/notification"
	"github.com/gtkit/gin-api/internal/plugin/builtin/robot"
	"github.com/gtkit/gin-api/internal/plugin/builtin/sms"
	"github.com/gtkit/gin-api/internal/testutil"
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

type tracedNotificationPlugin struct {
	core.Plugin
	trace *notificationTrace
}

func (p tracedNotificationPlugin) Init(ctx context.Context) error {
	p.trace.add("init:" + p.Name())
	return p.Plugin.Init(ctx)
}
func (p tracedNotificationPlugin) Start(ctx context.Context) error {
	p.trace.add("start:" + p.Name())
	return p.Plugin.Start(ctx)
}
func (p tracedNotificationPlugin) Stop(ctx context.Context) error {
	p.trace.add("stop:" + p.Name())
	return p.Plugin.Stop(ctx)
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
	logging         *logging.Plugin
	notification    *notification.Plugin
	channels        []core.Plugin
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
	configuration := f.base.components[0]
	cleanup := func(i int) func(context.Context) error {
		return func(context.Context) error { f.cleanups[i].Add(1); return nil }
	}
	r := robot.New(robot.Options{Configuration: configuration, Open: func(context.Context, config.NewsConfig) (news.WebhookSender, func(context.Context) error, error) {
		return notificationSender{&f.sends[0]}, cleanup(0), nil
	}})
	m := mail.New(mail.Options{Configuration: configuration, Open: func(context.Context, config.EmailConfig) (news.MailSender, func(context.Context) error, error) {
		return notificationMailSender{&f.sends[1]}, cleanup(1), nil
	}})
	s := sms.New(sms.Options{Configuration: configuration, Open: func(context.Context, config.SMSConfig) (news.SMSSender, func(context.Context) error, error) {
		return notificationSMSSender{&f.sends[2]}, cleanup(2), nil
	}})
	f.channels = []core.Plugin{r, m, s}
	f.notification = notification.New(notification.Options{Configuration: configuration, Robot: r, Mail: m, SMS: s})
	f.logging = logging.New(logging.Options{Configuration: configuration, Notification: f.notification})
	return f
}

func (f *notificationFixture) register(r *core.PluginRegistry) (PluginSet, error) {
	register := func(p core.Plugin, deps ...string) error {
		return r.Register(tracedNotificationPlugin{Plugin: p, trace: &f.trace}, deps...)
	}
	p := f.base.components
	if err := register(p[0]); err != nil {
		return PluginSet{}, err
	}
	for _, channel := range f.channels {
		if err := register(channel, p[0].Name()); err != nil {
			return PluginSet{}, err
		}
	}
	if err := register(f.notification, p[0].Name(), f.channels[0].Name(), f.channels[1].Name(), f.channels[2].Name()); err != nil {
		return PluginSet{}, err
	}
	if err := register(f.logging, p[0].Name(), f.notification.Name()); err != nil {
		return PluginSet{}, err
	}
	previous := f.logging.Name()
	for _, component := range p[2:] {
		if err := register(component, previous); err != nil {
			return PluginSet{}, err
		}
		previous = component.Name()
	}
	return PluginSet{Configuration: p[0], Logging: f.logging, Database: p[2], Redis: p[3], Cache: p[4], Middleware: p[5], DrainLogs: f.logging.Drain}, nil
}

func (f *notificationFixture) assertClosed(t *testing.T) {
	t.Helper()
	stops := f.trace.stops()
	logIndex := slices.Index(stops, "logging")
	notificationIndex := slices.Index(stops, "notification")
	if logIndex < 0 || notificationIndex <= logIndex {
		t.Fatalf("invalid stop order=%v", stops)
	}
	for i, channel := range f.channels {
		if slices.Index(stops, channel.Name()) <= notificationIndex || f.cleanups[i].Load() != 1 {
			t.Fatalf("invalid channel cleanup: stops=%v cleanups=%d", stops, f.cleanups[i].Load())
		}
	}
}

func TestNotificationRuntimeRollsBackAndDrainsBeforeChannels(t *testing.T) {
	for _, failure := range []string{"none", "init", "start"} {
		t.Run(failure, func(t *testing.T) {
			f := newNotificationFixture(t)
			cause := errors.New("fixture failure")
			if failure == "init" {
				f.base.components[2].init = func(context.Context) error { return cause }
			}
			if failure == "start" {
				f.base.components[2].start = func(context.Context) error { applog.HErrorf("startup failure alert"); return cause }
			}
			r, err := NewRuntime(t.Context(), Options{RegisterPlugins: f.register})
			if failure == "none" {
				if err != nil {
					t.Fatal(err)
				}
				applog.HErrorf("shutdown alert")
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
			if failure == "init" {
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
	r, err := NewRuntime(t.Context(), Options{RegisterPlugins: f.register})
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
	cfg.Modules.AI = config.AIModuleConfig{}
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
	cause, closing := errors.New("middleware construction failed"), errors.New("database close failed")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.base.components[5].build = func(plugin.MiddlewareDeps) ([]gin.HandlerFunc, error) { cancel(); return nil, cause }
	database.stop = func(ctx context.Context) error {
		if ctx.Err() != nil {
			t.Error("rollback inherited startup cancellation")
		}
		return errors.Join(sqlDB.Close(), closing)
	}
	mock.ExpectClose()
	app, err := NewApp(ctx, Options{RegisterPlugins: f.register})
	if app != nil || !errors.Is(err, cause) || !errors.Is(err, closing) {
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
		r, err := NewRuntime(t.Context(), Options{RegisterPlugins: f.register})
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
		applog.HErrorf("live worker exit alert")
		if err := app.closeResources(t.Context()); !errors.Is(err, ErrWorkerNotExited) {
			t.Fatalf("close resources=%v", err)
		}
		if len(f.trace.stops()) != 0 {
			t.Fatalf("registry stopped while worker alive: %v", f.trace.stops())
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
