package bootstrap

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gtkit/gin-api/internal/core"
)

func TestMigrationRuntimeDoesNotWireUnrelatedInfra(t *testing.T) {
	src := readSourceFile(t, "migrateruntime.go")
	for _, symbol := range []string{"NewResources", "initJWTWithConfig", "initProcessRuntime", "NewRuntime(", "initFileWithConfig", "initNewsWithConfig", "plugin/builtin/"} {
		if strings.Contains(src, symbol) {
			t.Fatalf("migration wired unrelated %s", symbol)
		}
	}
}

func TestMigrationRuntimeWiresRequiredParts(t *testing.T) {
	f := newLifecycleFixture(t)
	runtime, err := NewMigrationRuntime(t.Context(), MigrationOptions{RegisterPlugins: f.migration})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Config() != f.components[0].cfg || runtime.DB() != f.components[2].db {
		t.Fatal("migration did not borrow selected config/database")
	}
	if len(f.events) != 6 {
		t.Fatalf("unexpected components: %v", f.events)
	}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.assertStopped(t, "database", "logging", "config")
}

func TestMigrationRejectsMissingAndInvalidCapabilities(t *testing.T) {
	if _, err := NewMigrationRuntime(t.Context(), MigrationOptions{}); err == nil {
		t.Fatal("missing registrar accepted")
	}
	f := newLifecycleFixture(t)
	_, err := NewMigrationRuntime(t.Context(), MigrationOptions{RegisterPlugins: func(r *core.PluginRegistry) (MigrationPluginSet, error) {
		set, err := f.migration(r)
		set.Database = (*lifecycleComponent)(nil)
		return set, err
	}})
	if err == nil || len(f.events) != 0 {
		t.Fatalf("typed nil accepted or callbacks ran: %v %v", err, f.events)
	}
}

func TestMigrationStartupUsesFifteenSecondsAndFreshRollback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newLifecycleFixture(t)
		// Core limits one startup callback to 10s. Two 8s callbacks demonstrate
		// that migration's aggregate 15s budget is shared, not reset per callback.
		f.components[0].init = func(ctx context.Context) error { time.Sleep(8 * time.Second); return ctx.Err() }
		f.components[1].init = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
		var cleanupDeadline time.Time
		f.components[1].stop = func(ctx context.Context) error {
			if ctx.Err() != nil {
				t.Error("startup cancellation leaked into rollback")
			}
			cleanupDeadline, _ = ctx.Deadline()
			return nil
		}
		begin := time.Now()
		_, err := NewMigrationRuntime(t.Context(), MigrationOptions{RegisterPlugins: f.migration})
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(begin) != 15*time.Second {
			t.Fatalf("startup=%v elapsed=%v", err, time.Since(begin))
		}
		if time.Until(cleanupDeadline) != 30*time.Second {
			t.Fatalf("rollback remaining=%v", time.Until(cleanupDeadline))
		}
		f.assertStopped(t, "logging", "config")
	})
}

func TestMigrationClosePreservesCanceledParent(t *testing.T) {
	f := newLifecycleFixture(t)
	r, err := NewMigrationRuntime(t.Context(), MigrationOptions{RegisterPlugins: f.migration})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("stop cancellation lost: %v", err)
	}
	f.assertStopped(t, "database", "logging", "config")
}

func readSourceFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
