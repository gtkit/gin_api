package bootstrap

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
)

func TestMigrationRuntimeDoesNotWireUnrelatedInfra(t *testing.T) {
	src := readSourceFile(t, "migrateruntime.go")
	for _, symbol := range []string{"initJWTWithConfig", "initProcessRuntime", "NewRuntime(", "initFileWithConfig", "initNewsWithConfig", "plugin/builtin/"} {
		if strings.Contains(src, symbol) {
			t.Fatalf("migration wired unrelated %s", symbol)
		}
	}
}
func TestMigrationRuntimeWiresRequiredParts(t *testing.T) {
	f := newLifecycleFixture(t)
	r, err := NewMigrationRuntime(t.Context(), f.migrationOptions())
	if err != nil {
		t.Fatal(err)
	}
	if r.Config() != f.components[0].cfg || r.DB() != f.components[2].db {
		t.Fatal("migration did not borrow opened config/database")
	}
	if !slices.Equal(f.events, []string{"open:configuration", "open:logging", "open:database"}) {
		t.Fatalf("unexpected components=%v", f.events)
	}
	for range 2 {
		if err := r.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	f.assertStopped(t, "database", "logging")
}
func TestMigrationRejectsNilDatabaseAndRollsBack(t *testing.T) {
	for _, typedNil := range []bool{false, true} {
		f := newLifecycleFixture(t)
		factories := f.factories()
		factories.database = func(context.Context, *config.Config, bool) (databaseOwner, error) {
			if typedNil {
				return (*lifecycleComponent)(nil), nil
			}
			return nil, nil
		}
		if r, err := NewMigrationRuntime(t.Context(), MigrationOptions{factories: &factories}); r != nil || err == nil {
			t.Fatal("nil database accepted")
		}
		f.assertStopped(t, "logging")
	}
}
func TestMigrationStartupUsesFifteenSecondsAndFreshRollback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newLifecycleFixture(t)
		f.components[0].init = func(ctx context.Context) error { time.Sleep(8 * time.Second); return ctx.Err() }
		f.components[1].init = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
		var deadline time.Time
		f.components[1].stop = func(ctx context.Context) error {
			if ctx.Err() != nil {
				t.Error("startup cancellation leaked into rollback")
			}
			deadline, _ = ctx.Deadline()
			return nil
		}
		begin := time.Now()
		_, err := NewMigrationRuntime(t.Context(), f.migrationOptions())
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(begin) != 15*time.Second {
			t.Fatalf("startup=%v elapsed=%v", err, time.Since(begin))
		}
		if time.Until(deadline) != 30*time.Second {
			t.Fatalf("rollback remaining=%v", time.Until(deadline))
		}
		f.assertStopped(t, "logging")
	})
}
func TestMigrationClosePreservesCanceledParent(t *testing.T) {
	f := newLifecycleFixture(t)
	r, err := NewMigrationRuntime(t.Context(), f.migrationOptions())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("stop cancellation lost=%v", err)
	}
	f.assertStopped(t, "database", "logging")
}

func readSourceFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
