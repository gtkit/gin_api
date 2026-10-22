// Package logging implements the default gtkit logger behind project log events.
package logging

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/gtkit/gin-api/internal/core"
	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/logger/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Factory is the instance constructor used only by this default implementation.
type Factory func(...logger.Option) (*logger.Logger, error)

// Options selects validated configuration and optional instance factories.
type Options struct {
	Config *config.Config
	// Notification is borrowed. Nil selects logging without outbound alerts.
	Notification  news.NotificationSink
	MainFactory   Factory
	AccessFactory Factory
}

// Owner owns main/access outputs. It rejects new writes before draining the SDK.
// SDK Sync has no context or error return: deadlines are cooperative and true I/O
// failures are reported by that SDK to stderr, not observable as a return value.
type Owner struct {
	notification news.NotificationSink
	lifecycle    sync.Mutex
	main, access *logger.Logger
	previous     *logger.Logger
	release      func()
	closeOnce    sync.Once
	closeErr     error
	current      atomic.Pointer[logSnapshot]
	fields       sync.Pool
	gate         sync.Mutex
	writes       sync.WaitGroup
}

const pooledFields = 8

type fieldBuffer struct{ fields [pooledFields]zap.Field }

// Logger views are immutable after publication. Closing is an advisory atomic
// flag for level queries; gate serializes the authoritative check and writes.Add
// with closing, so Close cannot begin Wait while another Add is still possible.
type logSnapshot struct {
	main                 *logger.Logger
	mainCallers          [4]*logger.Logger
	accessForward        *logger.Logger
	mainCore, accessCore zapcore.Core
	closing              atomic.Bool
}

func (s *logSnapshot) enabled(level applog.Level, stream applog.Stream) bool {
	if s.closing.Load() {
		return false
	}
	core := s.mainCore
	if stream == applog.AccessStream {
		core = s.accessCore
	}
	return core.Enabled(zapLevel(level))
}

var _ applog.LevelEnabler = (*Owner)(nil)

// Open constructs both streams and installs their complete views in the facade.
// A failed factory releases every logger already obtained by this owner.
func Open(ctx context.Context, options Options) (result *Owner, err error) {
	owner := &Owner{notification: options.Notification}
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("open logging panic: %w: %v", core.ErrComponentState, value)
		}
		if err != nil {
			err = errors.Join(err, owner.Drain(ctx))
			result = nil
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.Config == nil {
		return nil, fmt.Errorf("logging configuration is required: %w", core.ErrComponentState)
	}
	if options.MainFactory == nil {
		options.MainFactory = logger.New
	}
	if options.AccessFactory == nil {
		options.AccessFactory = logger.New
	}
	if options.Notification != nil {
		if err := news.ValidateSender(options.Notification); err != nil {
			return nil, fmt.Errorf("logging notification: %w", err)
		}
	}
	owner.lifecycle.Lock()
	defer owner.lifecycle.Unlock()
	if err := owner.initialize(options); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return owner, nil
}

func (p *Owner) initialize(options Options) error {
	cfg := options.Config
	p.previous = logger.Default()
	opts := mainOptions(cfg, p.notification)
	main, err := options.MainFactory(opts...)
	p.main = main
	if err != nil {
		return fmt.Errorf("build logger: %w", err)
	}
	if main == nil {
		return fmt.Errorf("build logger: %w: nil instance", core.ErrComponentState)
	}
	access, err := options.AccessFactory(accessOptions(cfg)...)
	p.access = access
	if err != nil {
		return fmt.Errorf("build access logger: %w", err)
	}
	if access == nil {
		return fmt.Errorf("build access logger: %w: nil instance", core.ErrComponentState)
	}
	snapshot := &logSnapshot{
		main: main, accessForward: access.WithCallerSkip(2),
		mainCore: main.Zap().Core(), accessCore: access.Zap().Core(),
	}
	for i := range snapshot.mainCallers {
		snapshot.mainCallers[i] = main.WithCallerSkip(1 + i)
	}
	// Publish complete views before exposing this owner through the facade.
	p.current.Store(snapshot)
	release, err := applog.Install(p, p.Drain)
	if err != nil {
		return err
	}
	p.release = release
	logger.SetDefault(main)
	return nil
}

func (p *Owner) Log(ctx context.Context, entry applog.LogEntry) {
	snapshot := p.current.Load()
	if snapshot == nil || (!entry.Alert && !snapshot.enabled(entry.Level, entry.Stream)) {
		return
	}
	p.gate.Lock()
	if snapshot.closing.Load() {
		p.gate.Unlock()
		return
	}
	p.writes.Add(1)
	p.gate.Unlock()
	defer p.writes.Done()
	target := snapshot.accessForward
	if entry.Stream != applog.AccessStream {
		if entry.CallerSkip >= 0 && entry.CallerSkip < len(snapshot.mainCallers) {
			target = snapshot.mainCallers[entry.CallerSkip]
		} else {
			target = snapshot.main.WithCallerSkip(1 + entry.CallerSkip)
		}
	}
	if entry.Channel != "" {
		target = target.Channel(entry.Channel)
	}
	var fields []zap.Field
	if count := len(entry.Fields); count > 0 {
		if count <= pooledFields {
			buffer, ok := p.fields.Get().(*fieldBuffer)
			if !ok || buffer == nil {
				buffer = new(fieldBuffer)
			}
			defer p.releaseFields(buffer)
			fields = buffer.fields[:0]
		} else {
			fields = make([]zap.Field, 0, count)
		}
	}
	for _, field := range entry.Fields {
		if field.Key != "" {
			fields = append(fields, zap.Any(field.Key, field.Value))
		}
	}
	if id := applog.RequestIDFromContext(ctx); id != "" {
		ctx = logger.ContextWithRequestID(ctx, id)
	}
	message := entry.Text()
	if entry.Alert {
		target.HError(message, fields...)
		return
	}
	switch entry.Level {
	case applog.LevelDebug:
		target.DebugCtx(ctx, message, fields...)
	case applog.LevelWarn:
		target.WarnCtx(ctx, message, fields...)
	case applog.LevelError:
		target.ErrorCtx(ctx, message, fields...)
	default:
		target.InfoCtx(ctx, message, fields...)
	}
}

func (p *Owner) releaseFields(buffer *fieldBuffer) {
	// The fixed array bounds retained capacity; clearing releases user values.
	clear(buffer.fields[:])
	p.fields.Put(buffer)
}

// Enabled queries the dynamic SDK core without cloning a zap logger.
func (p *Owner) Enabled(level applog.Level, stream applog.Stream) bool {
	snapshot := p.current.Load()
	return snapshot != nil && snapshot.enabled(level, stream)
}

func zapLevel(level applog.Level) zapcore.Level {
	switch level {
	case applog.LevelDebug:
		return zapcore.DebugLevel
	case applog.LevelWarn:
		return zapcore.WarnLevel
	case applog.LevelError:
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}

// Drain and Close share one owner. No goroutine is abandoned to wrap SDK Sync.
func (p *Owner) Drain(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	p.closeOnce.Do(func() { p.closeErr = p.closeOwned(ctx) })
	return p.closeErr
}

// closeOwned runs once, including cleanup after a failed Open.
func (p *Owner) closeOwned(ctx context.Context) error {
	p.gate.Lock()
	snapshot := p.current.Load()
	if snapshot != nil {
		snapshot.closing.Store(true)
	}
	p.gate.Unlock()
	if p.release != nil {
		p.release()
	}
	// Bind the actual cleanup budget before waiting for writes or the SDK queue.
	// The borrowed capability owns cancellation, never resource closure here.
	if p.notification != nil && (p.main != nil || p.access != nil) {
		release := p.notification.BindDrain(ctx)
		defer release()
	}
	p.writes.Wait()
	if p.access != nil {
		p.access.Sync()
	}
	if p.main != nil {
		p.main.Sync()
	}
	if p.main != nil && logger.Default() == p.main {
		logger.SetDefault(p.previous)
	}
	return ctx.Err()
}

func (p *Owner) Close(ctx context.Context) error { return p.Drain(ctx) }
