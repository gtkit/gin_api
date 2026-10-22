package bootstrap

import (
	"context"
	"sync"
	"time"
)

const cleanupBudget = 30 * time.Second

// cleanupContext preserves a tighter parent deadline and joins its one optional
// force observer. Callbacks run on their caller, never in an abandoned goroutine.
func cleanupContext(parent context.Context, force <-chan struct{}) (context.Context, func()) {
	ctx, cancel := context.WithTimeout(parent, cleanupBudget)
	ctx, finish := forceContext(ctx, force)
	return ctx, func() { cancel(); finish() }
}

// forceContext joins the signal observer without imposing a new phase budget.
func forceContext(parent context.Context, force <-chan struct{}) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	var observers sync.WaitGroup
	if force != nil {
		select {
		case <-force:
			cancel()
		default:
			observers.Go(func() {
				select {
				case <-force:
					cancel()
				case <-ctx.Done():
				}
			})
		}
	}
	return ctx, func() { cancel(); observers.Wait() }
}

// rollbackBudget is confined to sequential construction. Its deadline starts at
// the first failure and is shared by module/provider and resource cleanup.
type rollbackBudget struct {
	force  <-chan struct{}
	ctx    context.Context
	cancel func()
}

func (b *rollbackBudget) Context(parent context.Context) context.Context {
	if b.ctx == nil {
		b.ctx, b.cancel = cleanupContext(context.WithoutCancel(parent), b.force)
	}
	return b.ctx
}

func (b *rollbackBudget) Close() {
	if b.cancel != nil {
		b.cancel()
	}
}
