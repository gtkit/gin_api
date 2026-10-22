package mysql

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/ormx"
	gormlogger "gorm.io/gorm/logger"
)

func TestMySQLPluginLifecycle(t *testing.T) {
	t.Parallel()

	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("sqlDB.Close(): %v", err)
		}
	})
	client, err := ormx.OpenWithDB(t.Context(), sqlDB,
		ormx.WithSkipInitializeWithVersion(true),
		ormx.WithServerVersion("8.0.36"),
		ormx.WithStartupPing(false),
	)
	if err != nil {
		t.Fatalf("ormx.OpenWithDB: %v", err)
	}

	called := false
	plugin := New(Options{Configuration: testConfiguration{},
		Open: func(context.Context, config.DatabaseConfig, config.DBCredentials, gormlogger.Interface) (Client, error) {
			called = true
			return ownedSQLClient{client}, nil
		},
	})
	if err := plugin.Init(t.Context()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !called || plugin.DB() == nil || plugin.SQLDB() == nil {
		t.Fatalf("plugin did not publish the injected client: called=%v db=%v sql=%v", called, plugin.DB(), plugin.SQLDB())
	}
	if err := plugin.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	mock.ExpectClose()
	if err := plugin.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if plugin.DB() != nil || plugin.SQLDB() != nil {
		t.Fatal("Stop left the owned client published")
	}
	if err := plugin.Stop(t.Context()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLPluginRejectsNilAndPropagatesOpenError(t *testing.T) {
	t.Parallel()

	openErr := errors.New("open failed")
	plugin := New(Options{Configuration: testConfiguration{},
		Open: func(context.Context, config.DatabaseConfig, config.DBCredentials, gormlogger.Interface) (Client, error) {
			return nil, openErr
		},
	})
	if err := plugin.Init(t.Context()); !errors.Is(err, openErr) {
		t.Fatalf("Init error = %v, want open error", err)
	}

	nilPlugin := New(Options{Configuration: testConfiguration{},
		Open: func(context.Context, config.DatabaseConfig, config.DBCredentials, gormlogger.Interface) (Client, error) {
			return nil, nil
		},
	})
	if err := nilPlugin.Init(t.Context()); err == nil {
		t.Fatal("Init accepted a nil client")
	}
}

func TestMySQLPluginZeroValueAndCancellationContracts(t *testing.T) {
	t.Parallel()

	plugin := New(Options{Configuration: testConfiguration{}})
	if plugin.Name() != "mysql" {
		t.Fatalf("Name() = %q, want mysql", plugin.Name())
	}
	if err := plugin.Start(t.Context()); err == nil {
		t.Fatal("Start accepted an uninitialized plugin")
	}
	if err := plugin.PingContext(t.Context()); err == nil {
		t.Fatal("PingContext accepted an uninitialized plugin")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := plugin.Init(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Init error = %v, want context.Canceled", err)
	}
	if err := plugin.Stop(t.Context()); err != nil {
		t.Fatalf("Stop without a client: %v", err)
	}
}

type testConfiguration struct{}

func (testConfiguration) Value() (*config.Config, error) { return &config.Config{}, nil }

// OpenWithDB borrows its pool; this test adapter explicitly owns that pool.
type ownedSQLClient struct{ *ormx.Client }

func (c ownedSQLClient) Close() error { return errors.Join(c.Client.Close(), c.SQLDB().Close()) }
