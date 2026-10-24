package cache

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestInvalidateBetweenLoaderReturnAndCommit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, err := New[int](4, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		var writes atomic.Int32
		c.jitter = func(ttl time.Duration, _ float64) time.Duration {
			if writes.Add(1) == 1 {
				// This callback runs after the loader returned, before cache commit.
				c.Invalidate("key")
			}
			return ttl
		}
		value, err := c.GetOrLoad(t.Context(), "key", func(context.Context) (int, error) { return 1, nil })
		if err != nil || value != 1 || c.Len() != 0 {
			t.Fatalf("invalidated load was cached: value=%d len=%d err=%v", value, c.Len(), err)
		}
		value, err = c.GetOrLoad(t.Context(), "key", func(context.Context) (int, error) { return 2, nil })
		if err != nil || value != 2 || c.Len() != 1 {
			t.Fatalf("new generation did not cache: value=%d len=%d err=%v", value, c.Len(), err)
		}
		value, err = c.GetOrLoad(t.Context(), "key", func(context.Context) (int, error) { t.Error("cache miss after fresh commit"); return 0, nil })
		if err != nil || value != 2 {
			t.Fatalf("fresh value=%d err=%v", value, err)
		}
	})
}

func TestSharedLoadPublishesSuccessErrorPanicAndNil(t *testing.T) {
	for _, outcome := range []string{"success", "error", "panic", "nil"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, err := New[any](4, time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				release := make(chan struct{})
				cause := errors.New("load failed")
				var calls atomic.Int32
				loader := func(context.Context) (any, error) {
					calls.Add(1)
					<-release
					switch outcome {
					case "error":
						return nil, cause
					case "panic":
						panic(cause)
					case "nil":
						return nil, nil
					default:
						return 42, nil
					}
				}
				var waiters sync.WaitGroup
				for range 100 {
					waiters.Go(func() {
						value, err := c.GetOrLoad(t.Context(), "shared", loader)
						switch outcome {
						case "panic":
							if !errors.Is(err, ErrLoaderPanic) || !errors.Is(err, cause) {
								t.Errorf("panic result: value=%v err=%v", value, err)
							}
						case "error":
							if !errors.Is(err, cause) {
								t.Errorf("shared error=%v", err)
							}
						case "nil":
							if err != nil || value != nil {
								t.Errorf("legal nil result=%v err=%v", value, err)
							}
						default:
							if err != nil || value != 42 {
								t.Errorf("shared success=%v err=%v", value, err)
							}
						}
					})
				}
				synctest.Wait()
				if calls.Load() != 1 {
					t.Errorf("concurrent loaders=%d, want 1", calls.Load())
				}
				close(release)
				waiters.Wait()
				c.loadMu.Lock()
				pending := len(c.loading)
				c.loadMu.Unlock()
				wantItems := 1
				if outcome == "error" || outcome == "panic" {
					wantItems = 0
				}
				if pending != 0 || c.Len() != wantItems {
					t.Fatalf("pending=%d cached=%d want=%d", pending, c.Len(), wantItems)
				}
			})
		})
	}
}

func TestCanceledWaitersDoNotRetainGrowingHeap(t *testing.T) {
	// Keep this test serial: heap samples describe this controlled workload,
	// rather than allocations from concurrently running package tests.
	synctest.Test(t, func(t *testing.T) {
		c, err := New[int](4, time.Minute, WithLoadTimeout[int](time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		release, ownerDone := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(ownerDone)
			if _, err := c.GetOrLoad(t.Context(), "blocked", func(context.Context) (int, error) { <-release; return 1, nil }); err != nil {
				t.Error(err)
			}
		}()
		synctest.Wait()
		c.loadMu.Lock()
		state := c.loading["blocked"]
		c.loadMu.Unlock()
		results := make(chan error)
		var samples [3]runtime.MemStats
		for batch := range samples {
			for range 10000 {
				ctx, cancel := context.WithCancel(t.Context())
				go func() {
					_, err := c.GetOrLoad(ctx, "blocked", func(context.Context) (int, error) { t.Error("duplicate loader"); return 0, nil })
					results <- err
				}()
				synctest.Wait() // The waiter has joined the existing load before cancellation.
				cancel()
				if err := <-results; !errors.Is(err, context.Canceled) {
					t.Errorf("waiter returned %v", err)
				}
			}
			synctest.Wait()
			runtime.GC()
			runtime.ReadMemStats(&samples[batch])
			c.loadMu.Lock()
			retained := len(c.loading) == 1 && c.loading["blocked"] == state
			c.loadMu.Unlock()
			if !retained {
				t.Error("canceled waiters changed the single in-flight load")
			}
			t.Logf("batch=%d canceled=%d live_heap_bytes=%d live_objects=%d total_alloc=%d", batch+1, (batch+1)*10000, samples[batch].HeapAlloc, samples[batch].HeapObjects, samples[batch].TotalAlloc)
		}
		// One retained channel per canceled caller grows by >1 MB per batch.
		// A wide allowance covers runtime bookkeeping without asserting a percentage.
		if samples[2].HeapAlloc > samples[0].HeapAlloc+512*1024 || samples[2].HeapObjects > samples[0].HeapObjects+1000 {
			t.Errorf("live heap grows with canceled callers: first=(%d,%d) last=(%d,%d)", samples[0].HeapAlloc, samples[0].HeapObjects, samples[2].HeapAlloc, samples[2].HeapObjects)
		}
		close(release)
		<-ownerDone
		runtime.KeepAlive(c)
	})
}

func TestIgnoringCancellationKeepsDefaultInflightSlots(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, err := New[int](128, time.Minute, WithLoadTimeout[int](time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		release := make(chan struct{})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var callers sync.WaitGroup
		var loads atomic.Int32
		results := make(chan error, 64)
		for key := range 64 {
			callers.Go(func() {
				_, err := c.GetOrLoad(ctx, fmt.Sprint(key), func(context.Context) (int, error) { loads.Add(1); <-release; return 1, nil })
				results <- err
			})
		}
		synctest.Wait()
		c.loadMu.Lock()
		pending := len(c.loading)
		c.loadMu.Unlock()
		if loads.Load() != 64 || pending != 64 || len(results) != 0 {
			t.Errorf("default bound rejected an admitted key: loaders=%d pending=%d early_returns=%d", loads.Load(), pending, len(results))
		}
		cancel()
		callers.Wait()
		for range 64 {
			if err := <-results; !errors.Is(err, context.Canceled) {
				t.Errorf("admitted caller returned %v", err)
			}
		}
		time.Sleep(time.Second) // Virtual time exceeds every independent load timeout.
		if _, err := c.GetOrLoad(t.Context(), "65", func(context.Context) (int, error) { t.Error("saturated load started"); return 0, nil }); !errors.Is(err, ErrLoadsSaturated) {
			t.Errorf("65th load bypassed the default bound: %v", err)
		}
		close(release)
		synctest.Wait()
		value, err := c.GetOrLoad(t.Context(), "65", func(context.Context) (int, error) { return 65, nil })
		if err != nil || value != 65 {
			t.Fatalf("completed loaders did not return slots: value=%d err=%v", value, err)
		}
	})
}
