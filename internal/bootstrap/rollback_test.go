package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/core"
)

func newRecordingProviderCleanup(visit func(context.Context, string) error) *providerCleanup {
	cleanup := &providerCleanup{}
	for _, step := range []struct {
		name  string
		phase providerCleanupPhase
	}{
		{"telemetry", cleanupTelemetry},
		{"db pool metrics", cleanupMetrics},
		{"rate limits", cleanupExtensions},
		{"outbound http clients", cleanupExtensions},
		{"task producer", cleanupExtensions},
		{"worker metrics", cleanupMetrics},
	} {
		cleanup.push(step.name, step.phase, func(ctx context.Context) error { return visit(ctx, step.name) })
	}
	return cleanup
}

func TestProviderCleanupOrderAndSingleConsumption(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		rollback bool
		want     []string
	}{
		{"normal", false, []string{"rate limits", "outbound http clients", "task producer", "telemetry", "db pool metrics", "worker metrics"}},
		{"rollback", true, []string{"worker metrics", "task producer", "outbound http clients", "rate limits", "db pool metrics", "telemetry"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var calls []string
			cleanup := newRecordingProviderCleanup(func(_ context.Context, name string) error {
				calls = append(calls, name)
				return nil
			})
			cleanup.push("ignored", cleanupExtensions, nil)
			run := cleanup.close
			if tt.rollback {
				run = cleanup.rollback
			}
			if err := run(t.Context()); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(calls, tt.want) {
				t.Fatalf("cleanup order = %v, want %v", calls, tt.want)
			}
			if err := cleanup.close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := cleanup.rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(calls) != len(tt.want) || len(cleanup.steps) != 0 {
				t.Fatalf("cleanup consumed again: calls=%v pending=%d", calls, len(cleanup.steps))
			}
		})
	}
}

func TestProviderCleanupRollsBackOnlyAcquiredSteps(t *testing.T) {
	t.Parallel()
	acquisition := []string{"telemetry", "db pool metrics", "rate limits", "outbound http clients", "task producer", "worker metrics"}
	for count := range len(acquisition) + 1 {
		t.Run(fmt.Sprintf("acquired=%d", count), func(t *testing.T) {
			t.Parallel()
			cleanup := &providerCleanup{}
			var calls []string
			for _, name := range acquisition[:count] {
				cleanup.push(name, cleanupExtensions, func(context.Context) error {
					calls = append(calls, name)
					return nil
				})
			}
			if err := cleanup.rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			want := slices.Clone(acquisition[:count])
			slices.Reverse(want)
			if !slices.Equal(calls, want) {
				t.Fatalf("partial rollback = %v, want %v", calls, want)
			}
		})
	}
}

func TestProviderCleanupContinuesAfterErrorsAndPanics(t *testing.T) {
	t.Parallel()
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback=%t", rollback), func(t *testing.T) {
			t.Parallel()
			failure := errors.New("rate limit close failed")
			var calls []string
			cleanup := newRecordingProviderCleanup(func(_ context.Context, name string) error {
				calls = append(calls, name)
				switch name {
				case "rate limits":
					return failure
				case "task producer":
					panic("broken task producer cleanup")
				}
				return nil
			})
			run := cleanup.close
			if rollback {
				run = cleanup.rollback
			}
			err := run(t.Context())
			if !errors.Is(err, failure) || !errors.Is(err, core.ErrComponentState) {
				t.Fatalf("cleanup lost error or panic: %v", err)
			}
			for _, name := range []string{"task producer", "rate limits", "broken task producer cleanup"} {
				if !strings.Contains(err.Error(), name) {
					t.Errorf("cleanup error does not identify %q: %v", name, err)
				}
			}
			if len(calls) != 6 {
				t.Fatalf("cleanup stopped early: %v", calls)
			}
			if err := run(t.Context()); err != nil || len(calls) != 6 {
				t.Fatalf("failed cleanup was retried: calls=%v err=%v", calls, err)
			}
		})
	}
}

func TestProviderCleanupUsesExecutionContext(t *testing.T) {
	t.Parallel()
	for _, rollback := range []bool{false, true} {
		for _, canceled := range []bool{false, true} {
			t.Run(fmt.Sprintf("rollback=%t/canceled=%t", rollback, canceled), func(t *testing.T) {
				t.Parallel()
				type key struct{}
				ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), key{}, t.Name()), time.Minute)
				defer cancel()
				if canceled {
					cancel()
				}
				calls := 0
				cleanup := newRecordingProviderCleanup(func(got context.Context, _ string) error {
					calls++
					deadline, ok := got.Deadline()
					wantDeadline, _ := ctx.Deadline()
					if got != ctx || got.Value(key{}) != t.Name() || got.Err() != ctx.Err() || !ok || !deadline.Equal(wantDeadline) {
						t.Error("cleanup changed its execution context")
					}
					return nil
				})
				run := cleanup.close
				if rollback {
					run = cleanup.rollback
				}
				if err := run(ctx); !errors.Is(err, ctx.Err()) || calls != 6 {
					t.Fatalf("cleanup error=%v calls=%d", err, calls)
				}
			})
		}
	}
}

func TestProviderCleanupConsumesBeforeReentry(t *testing.T) {
	t.Parallel()
	cleanup := &providerCleanup{}
	calls := 0
	cleanup.push("reentrant", cleanupExtensions, func(ctx context.Context) error {
		calls++
		return cleanup.rollback(ctx)
	})
	if err := cleanup.close(t.Context()); err != nil || calls != 1 {
		t.Fatalf("reentrant cleanup: calls=%d err=%v", calls, err)
	}
	var empty *providerCleanup
	if err := empty.close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := empty.rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
}
