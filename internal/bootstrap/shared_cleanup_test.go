package bootstrap

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestOnceCleanupSharesResultWithIndependentWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cleanup onceCleanup
		var calls atomic.Int32
		started, release := make(chan struct{}), make(chan struct{})
		ownerResult := make(chan error, 1)
		failure := errors.New("component close failed")
		go func() {
			ownerResult <- cleanup.do(t.Context(), nil, func(context.Context) error {
				calls.Add(1)
				close(started)
				<-release
				return failure
			})
		}()
		<-started
		unexpectedOwner := func(context.Context) error {
			calls.Add(1)
			return nil
		}

		// A canceled or forced waiter leaves the first owner's operation intact.
		canceled, cancel := context.WithCancel(t.Context())
		cancel()
		if err := cleanup.do(canceled, nil, unexpectedOwner); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled waiter = %v", err)
		}
		forced := make(chan struct{})
		close(forced)
		if err := cleanup.do(t.Context(), forced, unexpectedOwner); !errors.Is(err, context.Canceled) {
			t.Fatalf("forced waiter = %v", err)
		}

		const waiters = 16
		results := make(chan error, waiters)
		var group sync.WaitGroup
		for range waiters {
			group.Go(func() { results <- cleanup.do(t.Context(), nil, unexpectedOwner) })
		}
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatalf("cleanup owners = %d", calls.Load())
		}
		close(release)
		if err := <-ownerResult; !errors.Is(err, failure) {
			t.Fatalf("owner result = %v", err)
		}
		group.Wait()
		for range waiters {
			if err := <-results; !errors.Is(err, failure) {
				t.Fatalf("shared result = %v", err)
			}
		}
		// Once completed, the stored result takes precedence over a late caller's
		// cancellation and force signal. No caller may execute cleanup again.
		if err := cleanup.do(canceled, forced, unexpectedOwner); !errors.Is(err, failure) {
			t.Fatalf("completed result = %v", err)
		}
		if calls.Load() != 1 {
			t.Fatalf("cleanup executed %d times", calls.Load())
		}
	})
}

func TestOnceCleanupForceReachesOwnerAndPreservesFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cleanup onceCleanup
		force := make(chan struct{})
		failure := errors.New("component failure before forced shutdown")
		err := cleanup.do(t.Context(), force, func(ctx context.Context) error {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) != cleanupBudget {
				t.Errorf("cleanup deadline = %v, present=%v", deadline, ok)
			}
			close(force)
			<-ctx.Done()
			return failure
		})
		if !errors.Is(err, failure) || !errors.Is(err, context.Canceled) {
			t.Fatalf("forced result lost component or cancellation cause: %v", err)
		}
	})
}

func TestOnceCleanupRetainsEarlierCallerDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cleanup onceCleanup
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		started := time.Now()
		err := cleanup.do(ctx, nil, func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) != time.Second {
			t.Fatalf("deadline result = %v after %v", err, time.Since(started))
		}
	})
}
