//go:build integration

package bootstrap

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/gin-api/internal/repository/dbtx"
	"github.com/gtkit/gin-api/internal/testutil"
)

// This opt-in test reads the existing local databases; it creates no schemas or tables.
func TestNamedMySQLUsesRealDatabaseSelection(t *testing.T) {
	if os.Getenv("NAMED_CONNECTIONS_INTEGRATION") != "1" {
		t.Skip("set NAMED_CONNECTIONS_INTEGRATION=1 for the existing local MySQL")
	}
	live, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if live.Database.Host != "127.0.0.1" && live.Database.Host != "localhost" && live.Database.Host != "::1" {
		t.Fatal("this test only connects to the existing loopback MySQL")
	}
	f := newLifecycleFixture(t)
	cfg := f.components[0].cfg
	cfg.Database = live.Database
	metadata := live.Database
	metadata.Name = "information_schema"
	cfg.DatabaseConnections = map[string]config.DatabaseConfig{"metadata": metadata}
	factories := f.factories()
	factories.database = defaultRuntimeFactories().database
	runtime, err := NewRuntime(t.Context(), Options{factories: &factories})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	if err := runtime.instrumentConnections(); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{config.DefaultConnection: live.Database.Name, "metadata": metadata.Name} {
		db, err := runtime.Database(name)
		if err != nil {
			t.Fatal(err)
		}
		var selected string
		if err := db.WithContext(t.Context()).Raw("SELECT DATABASE()").Scan(&selected).Error; err != nil {
			t.Fatal(err)
		}
		if selected != want {
			t.Fatalf("connection %s selected the wrong database", name)
		}
	}
	primary, err := runtime.Database(config.DefaultConnection)
	if err != nil {
		t.Fatal(err)
	}
	other, err := runtime.Database("metadata")
	if err != nil {
		t.Fatal(err)
	}
	err = dbtx.NewManager(primary).Do(t.Context(), func(ctx context.Context) error {
		var selected string
		return dbtx.DB(ctx, other).Raw("SELECT DATABASE()").Scan(&selected).Error
	})
	if !errors.Is(err, dbtx.ErrForeignTransaction) {
		t.Fatalf("cross-database transaction was not rejected: %v", err)
	}
	if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Database("metadata"); err == nil {
		t.Fatal("closed database is still published")
	}
}

func TestNamedRedisUsesRealLocalServer(t *testing.T) {
	client := testutil.RealRedisOrSkip(t)
	options := client.Options()
	f := newLifecycleFixture(t)
	cfg := f.components[0].cfg
	cfg.RedisConnections = map[string]config.RedisConfig{
		"live": {Addr: options.Addr, Password: options.Password, DBs: []int{options.DB}},
	}
	factories := f.factories()
	openRedis := defaultRuntimeFactories().redis
	factories.redis = func(ctx context.Context, selected *config.Config) (redisOwner, error) {
		if selected == cfg {
			return f.components[3], nil
		}
		return openRedis(ctx, selected)
	}
	runtime, err := NewRuntime(t.Context(), Options{factories: &factories})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	if err := data.InstrumentRedisTracing(runtime.owners.redisConnections["live"]); err != nil {
		t.Fatal(err)
	}
	connection, err := runtime.Redis("live", options.DB)
	if err != nil {
		t.Fatal(err)
	}
	key := testutil.UniqueKeyPrefix(t, connection) + "named"
	if err := connection.Set(t.Context(), key, "value", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if got, err := connection.Get(t.Context(), key).Result(); err != nil || got != "value" {
		t.Fatalf("named Redis round trip=%q error=%v", got, err)
	}
}
