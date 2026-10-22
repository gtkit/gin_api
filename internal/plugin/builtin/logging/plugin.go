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
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/plugin"
	"github.com/gtkit/logger/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Factory is the instance constructor used only by this default implementation.
type Factory func(...logger.Option) (*logger.Logger, error)

// Options defers configuration reads and instance construction to Init.
type Options struct {
	Configuration plugin.ConfigurationProvider
	MainFactory   Factory
	AccessFactory Factory
}

// Plugin owns main/access outputs. It rejects new writes before draining the SDK.
// SDK Sync has no context or error return: deadlines are cooperative and true I/O
// failures are reported by that SDK to stderr, not observable as a return value.
type Plugin struct {
	options      Options
	lifecycle    sync.Mutex
	initOnce     sync.Once
	initErr      error
	stopped      bool
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
// with closing, so Stop cannot begin Wait while another Add is still possible.
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

var _ core.Plugin = (*Plugin)(nil)
var _ plugin.Logger = (*Plugin)(nil)
var _ applog.LevelEnabler = (*Plugin)(nil)

// New selects factories without creating resources or reading configuration.
func New(options Options) *Plugin {
	if options.MainFactory == nil {
		options.MainFactory = logger.New
	}
	if options.AccessFactory == nil {
		options.AccessFactory = logger.New
	}
	return &Plugin{options: options}
}

func (*Plugin) Name() string { return "logging" }

func (p *Plugin) Init(ctx context.Context) error {
	p.initOnce.Do(func() {
		p.lifecycle.Lock()
		defer p.lifecycle.Unlock()
		if p.stopped {
			p.initErr = fmt.Errorf("init logging after stop: %w", core.ErrPluginContractViolation)
			return
		}
		if err := ctx.Err(); err != nil {
			p.initErr = err
			return
		}
		if p.options.Configuration == nil {
			p.initErr = errors.New("logging configuration provider is required")
			return
		}
		cfg, err := p.options.Configuration.Value()
		if err != nil {
			p.initErr = fmt.Errorf("read logging config: %w", err)
			return
		}
		if cfg == nil {
			p.initErr = fmt.Errorf("read logging config: %w", core.ErrPluginContractViolation)
			return
		}
		p.initErr = p.initialize(ctx, cfg)
		if ctxErr := ctx.Err(); ctxErr != nil {
			p.initErr = errors.Join(p.initErr, ctxErr, p.drainPartial(ctx))
		}
	})
	return p.initErr
}

func (p *Plugin) initialize(ctx context.Context, cfg *config.Config) error {
	p.previous = logger.Default()
	opts, err := mainOptions(cfg)
	if err != nil {
		return err
	}
	main, err := p.options.MainFactory(opts...)
	p.main = main
	if err != nil {
		return errors.Join(fmt.Errorf("build logger: %w", err), p.drainPartial(ctx))
	}
	if main == nil {
		return fmt.Errorf("build logger: %w: nil instance", core.ErrPluginContractViolation)
	}
	access, err := p.options.AccessFactory(accessOptions(cfg)...)
	p.access = access
	if err != nil {
		return errors.Join(fmt.Errorf("build access logger: %w", err), p.drainPartial(ctx))
	}
	if access == nil {
		return errors.Join(fmt.Errorf("build access logger: %w: nil instance", core.ErrPluginContractViolation), p.drainPartial(ctx))
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
		return errors.Join(err, p.drainPartial(ctx))
	}
	p.release = release
	logger.SetDefault(main)
	return nil
}

// drainPartial closes instances obtained before a failed construction. The SDK
// caches its own Sync result; the same owner handles the registry's later Stop.
func (p *Plugin) drainPartial(ctx context.Context) error {
	p.closeOnce.Do(func() { p.closeErr = p.closeOwned(ctx) })
	return p.closeErr
}

func (p *Plugin) Start(ctx context.Context) error {
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	if p.stopped || p.current.Load() == nil {
		return fmt.Errorf("start logging: %w", core.ErrPluginContractViolation)
	}
	return ctx.Err()
}

func (p *Plugin) Log(ctx context.Context, entry applog.LogEntry) {
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

func (p *Plugin) releaseFields(buffer *fieldBuffer) {
	// The fixed array bounds retained capacity; clearing releases user values.
	clear(buffer.fields[:])
	p.fields.Put(buffer)
}

// Enabled queries the dynamic SDK core without cloning a zap logger.
func (p *Plugin) Enabled(level applog.Level, stream applog.Stream) bool {
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

// Drain and Stop share one owner. No goroutine is abandoned to wrap SDK Sync.
func (p *Plugin) Drain(ctx context.Context) error {
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	p.closeOnce.Do(func() { p.closeErr = p.closeOwned(ctx) })
	return p.closeErr
}

// closeOwned runs under lifecycle ownership, including partial Init failures.
func (p *Plugin) closeOwned(ctx context.Context) error {
	p.stopped = true
	p.gate.Lock()
	snapshot := p.current.Load()
	if snapshot != nil {
		snapshot.closing.Store(true)
	}
	p.gate.Unlock()
	if p.release != nil {
		p.release()
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

func (p *Plugin) Stop(ctx context.Context) error { return p.Drain(ctx) }
