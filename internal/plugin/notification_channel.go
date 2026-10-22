package plugin

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/pkg/news"
)

// NotificationClient transfers a sender and its optional cleanup to one owner.
// Borrowers receive only the channel's protected sender view, never Cleanup.
type NotificationClient[S any] struct {
	Sender  S
	Cleanup func(context.Context) error
}

// NotificationChannel shares the notification owners' admission/drain protocol.
// Its zero value is ready for Init. It starts no worker and is not a registry.
type NotificationChannel[S any] struct {
	mu         sync.Mutex
	initDone   chan struct{}
	initErr    error
	configured bool
	running    bool
	client     NotificationClient[S]
	life       context.Context
	cancel     context.CancelCauseFunc
	active     int
	idle       chan struct{}
	stopping   bool
	stopDone   chan struct{}
	stopErr    error
	bindings   map[*drainBinding]struct{}
}

// Init runs open once. Disabled channels need no sender and do no external work.
func (c *NotificationChannel[S]) Init(ctx context.Context, open func(context.Context) (NotificationClient[S], bool, error)) error {
	c.mu.Lock()
	if c.stopping {
		c.mu.Unlock()
		return news.ErrStopped
	}
	if c.initDone != nil {
		done := c.initDone
		c.mu.Unlock()
		if err := awaitNotification(ctx, done); err != nil {
			return err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.initErr
	}
	c.initDone = make(chan struct{})
	c.life, c.cancel = context.WithCancelCause(context.Background())
	life := c.life
	c.mu.Unlock()

	openCtx, cancel := context.WithCancelCause(ctx)
	release := linkNotificationCancel(life, cancel)
	if cause := context.Cause(life); cause != nil {
		cancel(cause)
	}
	client, configured, err := openNotification(openCtx, open)
	err = errors.Join(err, openCtx.Err())
	release()
	cancel(nil)
	if err == nil && configured {
		err = news.ValidateSender(client.Sender)
	}

	c.mu.Lock()
	if c.stopping {
		err = errors.Join(err, news.ErrStopped)
	}
	err = errors.Join(err, ctx.Err())
	c.client = client
	c.configured = configured
	if err == nil {
		c.initErr = nil
		close(c.initDone)
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	// Init owns partial results, including results returned after cancellation.
	// The independent 30s limit is the plan's existing rollback budget.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	cleanupErr := closeNotification(cleanupCtx, client.Cleanup)
	cleanupCancel()
	c.mu.Lock()
	c.client.Cleanup = nil
	c.initErr = errors.Join(err, cleanupErr)
	c.stopErr = errors.Join(c.stopErr, cleanupErr)
	c.cancel(c.initErr)
	close(c.initDone)
	c.mu.Unlock()
	return errors.Join(err, cleanupErr)
}

func openNotification[S any](ctx context.Context, open func(context.Context) (NotificationClient[S], bool, error)) (client NotificationClient[S], configured bool, err error) {
	defer func() {
		if recover() != nil {
			err = news.ErrSenderPanic
		}
	}()
	if err := ctx.Err(); err != nil {
		return client, false, err
	}
	return open(ctx)
}

// Start opens admission only after a successful Init.
func (c *NotificationChannel[S]) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopping {
		return news.ErrStopped
	}
	if c.initDone == nil {
		return fmt.Errorf("start notification before Init: %w", core.ErrPluginContractViolation)
	}
	select {
	case <-c.initDone:
	default:
		return fmt.Errorf("start notification during Init: %w", core.ErrPluginContractViolation)
	}
	if c.initErr != nil {
		return c.initErr
	}
	if err := ctx.Err(); err != nil {
		c.initErr = err
		c.cancel(err)
		return err
	}
	c.running = true
	return nil
}

// Configured reports whether Init published an enabled, usable sender.
func (c *NotificationChannel[S]) Configured() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.initDone == nil {
		return false
	}
	select {
	case <-c.initDone:
		return c.initErr == nil && c.configured && !c.stopping
	default:
		return false
	}
}

// Use registers admission before exposing the sender to a synchronous callback.
func (c *NotificationChannel[S]) Use(ctx context.Context, send func(context.Context, S) error) (err error) {
	c.mu.Lock()
	switch {
	case c.stopping:
		err = news.ErrStopped
	case !c.running:
		err = news.ErrNotRunning
	case !c.configured:
		err = news.ErrSenderDisabled
	case ctx.Err() != nil:
		err = ctx.Err()
	case c.life.Err() != nil:
		err = context.Cause(c.life)
	}
	if err != nil {
		c.mu.Unlock()
		return err
	}
	if c.active == 0 {
		c.idle = make(chan struct{})
	}
	c.active++
	client, life := c.client.Sender, c.life
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.active--
		if c.active == 0 {
			close(c.idle)
		}
		c.mu.Unlock()
		if recover() != nil {
			err = news.ErrSenderPanic
		}
	}()

	callCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	release := linkNotificationCancel(life, cancel)
	defer release()
	if cause := context.Cause(life); cause != nil {
		cancel(cause)
	}
	if err := callCtx.Err(); err != nil {
		return err
	}
	return errors.Join(send(callCtx, client), callCtx.Err())
}

// Stop rejects new work before cancellation, then waits without a waiter worker.
// A timed-out drain retains the client and cleanup for process-exit reclamation.
// Repeated calls observe the first Stop result and never repeat cleanup.
func (c *NotificationChannel[S]) Stop(ctx context.Context) error {
	c.mu.Lock()
	if c.stopping {
		done := c.stopDone
		c.mu.Unlock()
		if err := awaitNotification(ctx, done); err != nil {
			return err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.stopErr
	}
	c.stopping = true
	c.running = false
	c.stopDone = make(chan struct{})
	initDone := c.initDone
	cancel := c.cancel
	bindings := make([]*drainBinding, 0, len(c.bindings))
	for binding := range c.bindings {
		bindings = append(bindings, binding)
	}
	c.mu.Unlock()
	if cancel != nil {
		cancel(news.ErrStopped)
	}
	for _, binding := range bindings {
		binding.release()
	}
	if initDone != nil {
		if err := awaitNotification(ctx, initDone); err != nil {
			return c.finishStop(errors.Join(news.ErrNotDrained, err))
		}
	}
	c.mu.Lock()
	idle, active, cleanup := c.idle, c.active, c.client.Cleanup
	c.mu.Unlock()
	if active != 0 {
		if err := awaitNotification(ctx, idle); err != nil {
			return c.finishStop(errors.Join(news.ErrNotDrained, err))
		}
	}
	if err := ctx.Err(); err != nil && cleanup != nil {
		return c.finishStop(errors.Join(news.ErrNotDrained, err))
	}
	err := closeNotification(ctx, cleanup)
	c.mu.Lock()
	c.client.Cleanup = nil
	var zero S
	c.client.Sender = zero
	c.mu.Unlock()
	return c.finishStop(errors.Join(err, ctx.Err()))
}

func (c *NotificationChannel[S]) finishStop(err error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopErr = errors.Join(c.stopErr, err)
	close(c.stopDone)
	return c.stopErr
}

func closeNotification(ctx context.Context, cleanup func(context.Context) error) (err error) {
	defer func() {
		if recover() != nil {
			err = news.ErrSenderPanic
		}
	}()
	if cleanup != nil {
		return cleanup(ctx)
	}
	return nil
}

func awaitNotification(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type drainBinding struct{ release func() }

// BindDrain links only cancellation. Its release joins its own short callback.
func (c *NotificationChannel[S]) BindDrain(ctx context.Context) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel == nil || c.stopping {
		return func() {}
	}
	binding := &drainBinding{}
	unlink := linkNotificationCancel(ctx, c.cancel)
	if cause := context.Cause(ctx); cause != nil {
		c.cancel(cause)
	}
	binding.release = sync.OnceFunc(func() {
		unlink()
		c.mu.Lock()
		delete(c.bindings, binding)
		c.mu.Unlock()
	})
	if c.bindings == nil {
		c.bindings = make(map[*drainBinding]struct{})
	}
	c.bindings[binding] = struct{}{}
	return binding.release
}

func linkNotificationCancel(ctx context.Context, cancel context.CancelCauseFunc) func() {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		cancel(context.Cause(ctx))
	})
	return sync.OnceFunc(func() {
		if stop() {
			close(done)
		}
		<-done
	})
}
