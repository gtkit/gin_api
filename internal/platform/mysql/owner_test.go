package mysql

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/ormx"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// OpenWithDB borrows its pool; this test adapter explicitly owns that pool.
type ownedSQLClient struct{ *ormx.Client }

func (c ownedSQLClient) Close() error { return errors.Join(c.Client.Close(), c.SQLDB().Close()) }
func TestOpenCancellationPrecedesFactory(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p, err := Open(ctx, Options{Config: &config.Config{}, Open: func(context.Context, config.DatabaseConfig, config.DBCredentials, gormlogger.Interface) (Client, error) {
		t.Fatal("opened with canceled context")
		return nil, nil
	}})
	if p != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("owner=%v err=%v", p, err)
	}
}
func TestClosedAccessorsAreSafe(t *testing.T) {
	p := new(Owner)
	if p.DB() != nil || p.SQLDB() != nil || p.PingContext(t.Context()) == nil {
		t.Fatal("empty owner published a client")
	}
	if err := p.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type unavailableClient struct{ closes atomic.Int32 }

func (*unavailableClient) DB() *gorm.DB                      { return nil }
func (*unavailableClient) SQLDB() *sql.DB                    { return nil }
func (*unavailableClient) PingContext(context.Context) error { return nil }
func (c *unavailableClient) Close() error                    { c.closes.Add(1); return nil }

func TestOpenRejectsUnavailableDatabaseAndClosesIt(t *testing.T) {
	client := new(unavailableClient)
	p, err := Open(t.Context(), Options{Config: &config.Config{}, Open: func(context.Context, config.DatabaseConfig, config.DBCredentials, gormlogger.Interface) (Client, error) {
		return client, nil
	}})
	if p != nil || !errors.Is(err, core.ErrComponentState) || client.closes.Load() != 1 {
		t.Fatalf("owner=%v err=%v closes=%d", p, err, client.closes.Load())
	}
}

func TestOpenDefaultsToRuntimeCredentials(t *testing.T) {
	client, mock := ownedClient(t)
	mock.ExpectClose()
	cfg := &config.Config{Database: config.DatabaseConfig{Username: "runtime", Password: "runtime-password"}}
	p, err := Open(t.Context(), Options{Config: cfg, Open: func(_ context.Context, database config.DatabaseConfig, credentials config.DBCredentials, sqlLogger gormlogger.Interface) (Client, error) {
		if credentials != database.RuntimeCredentials() || sqlLogger == nil {
			t.Fatal("runtime identity or SQL logger changed")
		}
		return client, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
