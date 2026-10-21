// Package logging implements the default gtkit logger behind project log events.
package logging

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gtkit/gin-api/internal/core"
	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/plugin"
	sdk "github.com/gtkit/logger/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Factory is the instance constructor used only by this default implementation.
type Factory func(...sdk.Option) (*sdk.Logger, error)

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
	options                    Options
	lifecycle                  sync.Mutex
	initOnce                   sync.Once
	initErr                    error
	gate                       sync.Mutex
	writes                     sync.WaitGroup
	closing                    bool
	ready                      bool
	main, access               *sdk.Logger
	mainForward, accessForward *sdk.Logger
	previous                   *sdk.Logger
	release                    func()
	closeOnce                  sync.Once
	closeErr                   error
}

var _ core.Plugin = (*Plugin)(nil)
var _ plugin.Logger = (*Plugin)(nil)

// New selects factories without creating resources or reading configuration.
func New(options Options) *Plugin {
	if options.MainFactory == nil {
		options.MainFactory = sdk.New
	}
	if options.AccessFactory == nil {
		options.AccessFactory = sdk.New
	}
	return &Plugin{options: options}
}

func (*Plugin) Name() string { return "logging" }

func (p *Plugin) Init(ctx context.Context) error {
	p.initOnce.Do(func() {
		p.lifecycle.Lock()
		defer p.lifecycle.Unlock()
		p.gate.Lock()
		closed := p.closing
		p.gate.Unlock()
		if closed {
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
	p.previous = sdk.Default()
	opts, err := mainOptions(cfg)
	if err != nil {
		return err
	}
	main, err := p.options.MainFactory(opts...)
	p.gate.Lock()
	p.main = main
	p.gate.Unlock()
	if err != nil {
		return errors.Join(fmt.Errorf("build logger: %w", err), p.drainPartial(ctx))
	}
	if main == nil {
		return fmt.Errorf("build logger: %w: nil instance", core.ErrPluginContractViolation)
	}
	access, err := p.options.AccessFactory(accessOptions(cfg)...)
	p.gate.Lock()
	p.access = access
	p.gate.Unlock()
	if err != nil {
		return errors.Join(fmt.Errorf("build access logger: %w", err), p.drainPartial(ctx))
	}
	if access == nil {
		return errors.Join(fmt.Errorf("build access logger: %w: nil instance", core.ErrPluginContractViolation), p.drainPartial(ctx))
	}
	p.gate.Lock()
	p.mainForward = main.WithCallerSkip(4)
	p.accessForward = access.WithCallerSkip(2)
	release, err := applog.Install(p, p.Drain)
	if err != nil {
		p.gate.Unlock()
		return errors.Join(err, p.drainPartial(ctx))
	}
	p.ready = true
	p.release = release
	sdk.SetDefault(main)
	p.gate.Unlock()
	return nil
}

// drainPartial closes instances obtained before a failed construction. The SDK
// caches its own Sync result; the same owner handles the registry's later Stop.
func (p *Plugin) drainPartial(ctx context.Context) error {
	p.closeOnce.Do(func() { p.closeErr = p.closeOwned(ctx) })
	return p.closeErr
}

func (p *Plugin) Start(ctx context.Context) error {
	p.gate.Lock()
	defer p.gate.Unlock()
	if !p.ready || p.closing {
		return fmt.Errorf("start logging: %w", core.ErrPluginContractViolation)
	}
	return ctx.Err()
}

func (p *Plugin) Log(ctx context.Context, entry applog.LogEntry) {
	p.gate.Lock()
	if p.closing || !p.ready {
		p.gate.Unlock()
		return
	}
	target := p.accessForward
	if entry.Stream != applog.AccessStream {
		target = p.mainForward
		if entry.CallerSkip != 3 {
			target = p.main.WithCallerSkip(1 + entry.CallerSkip)
		}
	}
	p.writes.Add(1)
	p.gate.Unlock()
	defer p.writes.Done()
	level := zapcore.InfoLevel
	switch entry.Level {
	case applog.LevelDebug:
		level = zapcore.DebugLevel
	case applog.LevelWarn:
		level = zapcore.WarnLevel
	case applog.LevelError:
		level = zapcore.ErrorLevel
	}
	if !entry.Alert && !target.Zap().Core().Enabled(level) {
		return
	}
	if entry.Channel != "" {
		target = target.Channel(entry.Channel)
	}
	fields := make([]zap.Field, 0, len(entry.Fields))
	for _, field := range entry.Fields {
		if field.Key != "" {
			fields = append(fields, zap.Any(field.Key, field.Value))
		}
	}
	if id := applog.RequestIDFromContext(ctx); id != "" {
		ctx = sdk.ContextWithRequestID(ctx, id)
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

// Drain and Stop share one owner. No goroutine is abandoned to wrap SDK Sync.
func (p *Plugin) Drain(ctx context.Context) error {
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	p.closeOnce.Do(func() { p.closeErr = p.closeOwned(ctx) })
	return p.closeErr
}

// closeOwned runs under lifecycle ownership, including partial Init failures.
func (p *Plugin) closeOwned(ctx context.Context) error {
	p.gate.Lock()
	p.closing = true
	release := p.release
	p.gate.Unlock()
	if release != nil {
		release()
	}
	p.writes.Wait()
	if p.access != nil {
		p.access.Sync()
	}
	if p.main != nil {
		p.main.Sync()
	}
	if p.main != nil && sdk.Default() == p.main {
		sdk.SetDefault(p.previous)
	}
	return ctx.Err()
}

func (p *Plugin) Stop(ctx context.Context) error { return p.Drain(ctx) }
