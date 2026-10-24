// Package logging owns the process's native gtkit logger instances.
package logging

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/logger/v2"
)

// Factory constructs an owned native logger during initialization.
type Factory func(...logger.Option) (*logger.Logger, error)

// Options selects validated configuration and optional instance factories.
type Options struct {
	Config *config.Config
	// Notification is borrowed. Nil selects logging without outbound alerts.
	Notification  news.NotificationSink
	MainFactory   Factory
	AccessFactory Factory
}

// Owner manages initialization and shutdown, never individual log writes.
// Close requires all producers to have stopped. SDK Sync has no error/context
// return; deadlines remain cooperative and cannot interrupt file I/O.
type Owner struct {
	notification news.NotificationSink
	lifecycle    sync.Mutex
	main, access *logger.Logger
	previous     *logger.Logger
	closed       bool
	closeErr     error
}

// Ownership is reserved only during Open/Close, not on the logging hot path.
var ownership struct {
	sync.Mutex
	current *Owner
}

// Open constructs both streams before publishing the main default instance.
// Failed construction closes any instances already returned by the factories.
func Open(ctx context.Context, options Options) (result *Owner, err error) {
	owner := &Owner{notification: options.Notification}
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("open logging panic: %w: %v", core.ErrComponentState, value)
		}
		if err != nil {
			err = errors.Join(err, owner.Close(ctx))
			result = nil
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.Config == nil {
		return nil, fmt.Errorf("logging configuration is required: %w", core.ErrComponentState)
	}
	if options.Notification != nil {
		if err := news.ValidateSender(options.Notification); err != nil {
			return nil, fmt.Errorf("logging notification: %w", err)
		}
	}
	if options.MainFactory == nil {
		options.MainFactory = logger.New
	}
	if options.AccessFactory == nil {
		options.AccessFactory = logger.New
	}
	ownership.Lock()
	if ownership.current != nil {
		ownership.Unlock()
		return nil, fmt.Errorf("logging owner already open: %w", core.ErrComponentState)
	}
	ownership.current = owner
	ownership.Unlock()

	owner.previous = logger.Default()
	owner.main, err = options.MainFactory(mainOptions(options.Config, owner.notification)...)
	if err != nil {
		return nil, fmt.Errorf("build logger: %w", err)
	}
	if owner.main == nil {
		return nil, fmt.Errorf("build logger: %w: nil instance", core.ErrComponentState)
	}
	owner.access, err = options.AccessFactory(accessOptions(options.Config)...)
	if err != nil {
		return nil, fmt.Errorf("build access logger: %w", err)
	}
	if owner.access == nil {
		return nil, fmt.Errorf("build access logger: %w: nil instance", core.ErrComponentState)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	logger.SetDefault(owner.main)
	return owner, nil
}

// AccessLogger returns the native instance for one-time router assembly.
// Borrowers may log through it, but must not close it.
func (p *Owner) AccessLogger() *logger.Logger { return p.access }

// Drain flushes file buffers while producers may still be running. It neither
// closes SDK resources nor drains notification queues, and does not consume Close.
func (p *Owner) Drain(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	if p.closed {
		return p.closeErr
	}
	var errs []error
	if p.access != nil {
		if err := p.access.Zap().Sync(); err != nil {
			errs = append(errs, fmt.Errorf("flush access logger: %w", err))
		}
	}
	if p.main != nil {
		if err := p.main.Zap().Sync(); err != nil {
			errs = append(errs, fmt.Errorf("flush main logger: %w", err))
		}
	}
	return errors.Join(append(errs, ctx.Err())...)
}

// Close releases owned instances once, after the caller has stopped producers.
func (p *Owner) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	if p.closed {
		return p.closeErr
	}
	p.closed = true
	// Redirect subsequent package-level diagnostics before SDK queue draining.
	// This is not a barrier for callers that already hold a native instance.
	if p.main != nil && logger.Default() == p.main {
		logger.SetDefault(p.previous)
	}
	if p.notification != nil && (p.main != nil || p.access != nil) {
		release := p.notification.BindDrain(ctx)
		defer release()
	}
	if p.access != nil {
		p.access.Sync()
	}
	if p.main != nil {
		p.main.Sync()
	}
	p.closeErr = ctx.Err()
	ownership.Lock()
	if ownership.current == p {
		ownership.current = nil
	}
	ownership.Unlock()
	return p.closeErr
}
