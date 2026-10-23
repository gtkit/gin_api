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

// TestRollbackStackUnwindsInReverseAndAggregates 钉住回滚栈的核心契约：
// 逆序执行、单步失败不阻断其余、错误聚合并指名失败步骤、执行后清空。
func TestRollbackStackUnwindsInReverseAndAggregates(t *testing.T) {
	var order []string
	rb := &rollbackStack{}
	rb.push("first", func() error { order = append(order, "first"); return nil })
	rb.push("second", func() error { order = append(order, "second"); return errors.New("boom") })
	rb.push("third", func() error { order = append(order, "third"); return nil })

	err := rb.unwind()
	if err == nil {
		t.Fatal("unwind swallowed a step failure")
	}
	if !strings.Contains(err.Error(), "rollback second") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error %q does not name the failing step and its cause", err)
	}
	if want := []string{"third", "second", "first"}; len(order) != 3 ||
		order[0] != want[0] || order[1] != want[1] || order[2] != want[2] {
		t.Fatalf("unwind order = %v, want %v (reverse of push order)", order, want)
	}

	// 执行后清空：再次 unwind 不重复执行任何步骤。
	order = nil
	if err := rb.unwind(); err != nil {
		t.Fatalf("second unwind = %v, want nil", err)
	}
	if len(order) != 0 {
		t.Fatalf("second unwind re-executed steps: %v", order)
	}
}

// TestRollbackStackDiscardPreventsUnwind 钉住成功路径语义：
// discard 之后 unwind 不执行任何步骤——副作用的所有权已移交关停序列。
func TestRollbackStackDiscardPreventsUnwind(t *testing.T) {
	var calls int
	rb := &rollbackStack{}
	rb.push("step", func() error { calls++; return nil })
	rb.push("", nil) // nil 步骤直接忽略

	rb.discard()
	if err := rb.unwind(); err != nil {
		t.Fatalf("unwind after discard = %v, want nil", err)
	}
	if calls != 0 {
		t.Fatalf("unwind after discard executed %d step(s), want 0", calls)
	}
}

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
		{"modules", cleanupModules},
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
		{"normal", false, []string{"modules", "rate limits", "outbound http clients", "task producer", "telemetry", "db pool metrics", "worker metrics"}},
		{"rollback", true, []string{"worker metrics", "modules", "task producer", "outbound http clients", "rate limits", "db pool metrics", "telemetry"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var calls []string
			cleanup := newRecordingProviderCleanup(func(_ context.Context, name string) error {
				calls = append(calls, name)
				return nil
			})
			cleanup.push("ignored", cleanupModules, nil)
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
	acquisition := []string{"telemetry", "db pool metrics", "rate limits", "outbound http clients", "task producer", "modules", "worker metrics"}
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
				case "modules":
					panic("broken module cleanup")
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
			for _, name := range []string{"modules", "rate limits", "broken module cleanup"} {
				if !strings.Contains(err.Error(), name) {
					t.Errorf("cleanup error does not identify %q: %v", name, err)
				}
			}
			if len(calls) != 7 {
				t.Fatalf("cleanup stopped early: %v", calls)
			}
			if err := run(t.Context()); err != nil || len(calls) != 7 {
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
				if err := run(ctx); !errors.Is(err, ctx.Err()) || calls != 7 {
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
	cleanup.push("reentrant", cleanupModules, func(ctx context.Context) error {
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
