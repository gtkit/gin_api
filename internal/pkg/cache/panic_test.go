package cache

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// Run the dangerous callback in a child so a regression cannot kill the suite.
func TestLoaderPanicKeepsProcessAndCapacity(t *testing.T) {
	if os.Getenv("GIN_API_CACHE_PANIC_TEST_CHILD") == "1" {
		c := newTestCache(t, 1, time.Minute, WithMaxInflight[string](1))
		const secret = "private loader panic value"
		_, err := c.GetOrLoad(t.Context(), "key", func(context.Context) (string, error) {
			panic(errors.New(secret))
		})
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("panic must return a controlled error: %v", err)
		}
		for _, key := range []string{"key", "other"} {
			value, loadErr := c.GetOrLoad(t.Context(), key, func(context.Context) (string, error) {
				return "recovered", nil
			})
			if loadErr != nil || value != "recovered" {
				t.Fatalf("load after panic: value=%q, err=%v", value, loadErr)
			}
		}
		return
	}
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestLoaderPanicKeepsProcessAndCapacity$", "-test.count=1")
	child.Env = append(os.Environ(), "GIN_API_CACHE_PANIC_TEST_CHILD=1")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("loader panic terminated isolated child: %v\n%s", err, output)
	}
}

func TestLoaderPanicIsSharedAndDoesNotPoisonTheKey(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newTestCache(t, 1, time.Minute, WithMaxInflight[string](1))
		release := make(chan struct{})
		cause := errors.New("private upstream failure")
		const waiters = 8
		errs := make([]error, waiters)
		var callers sync.WaitGroup
		for i := range waiters {
			callers.Go(func() {
				_, errs[i] = c.GetOrLoad(t.Context(), "key", func(context.Context) (string, error) {
					<-release
					panic(cause)
				})
			})
		}
		synctest.Wait()
		close(release)
		callers.Wait()
		for _, err := range errs {
			if !errors.Is(err, ErrLoaderPanic) || !errors.Is(err, cause) {
				t.Fatalf("panic lost its identity or cause: %v", err)
			}
			if strings.Contains(err.Error(), cause.Error()) {
				t.Fatal("panic details escaped the controlled error")
			}
		}
		value, err := c.GetOrLoad(t.Context(), "key", func(context.Context) (string, error) {
			return "next", nil
		})
		if err != nil || value != "next" {
			t.Fatalf("subsequent load: %q, %v", value, err)
		}
	})
}
