package mysql

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/ormx"
	gormlogger "gorm.io/gorm/logger"
)

func ownedClient(t *testing.T) (ownedSQLClient, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close SQL: %v", err)
		}
	})
	client, err := ormx.OpenWithDB(t.Context(), db, ormx.WithSkipInitializeWithVersion(true), ormx.WithServerVersion("8.0.36"), ormx.WithStartupPing(false))
	if err != nil {
		t.Fatal(err)
	}
	return ownedSQLClient{client}, mock
}
func TestFactoryOwnershipAndConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		partial, cancel, closeFailure bool
	}{
		{name: "success"}, {name: "close error", closeFailure: true}, {name: "partial error", partial: true}, {name: "late cancellation", cancel: true}, {name: "partial plus close error", partial: true, closeFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, mock := ownedClient(t)
			cfg := &config.Config{Database: config.DatabaseConfig{Host: "unused", Username: "runtime"}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			root, closeErr := errors.New("open failed"), errors.New("close failed")
			expected := mock.ExpectClose()
			if tc.closeFailure {
				expected.WillReturnError(closeErr)
			}
			opens := 0
			p, err := Open(ctx, Options{Config: cfg, SelectCredentials: func(config.DatabaseConfig) config.DBCredentials { return config.DBCredentials{Username: "selected"} }, Open: func(callCtx context.Context, c config.DatabaseConfig, creds config.DBCredentials, logger gormlogger.Interface) (Client, error) {
				opens++
				if callCtx != ctx || c.Host != "unused" || creds.Username != "selected" || logger == nil {
					t.Fatal("factory arguments drifted")
				}
				if tc.cancel {
					cancel()
				}
				if tc.partial {
					return client, root
				}
				return client, nil
			}})
			if tc.partial && !errors.Is(err, root) {
				t.Fatalf("lost root: %v", err)
			}
			if tc.cancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if tc.partial && tc.closeFailure && !errors.Is(err, closeErr) {
				t.Fatalf("lost cleanup error: %v", err)
			}
			if tc.partial || tc.cancel {
				if p != nil {
					t.Fatal("failed owner published")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if p.DB() == nil || p.SQLDB() == nil {
					t.Fatal("client not published")
				}
				var group sync.WaitGroup
				for range 16 {
					group.Go(func() {
						closeResult := p.Close(context.WithoutCancel(t.Context()))
						if tc.closeFailure {
							if !errors.Is(closeResult, closeErr) {
								t.Errorf("lost close error: %v", closeResult)
							}
						} else if closeResult != nil {
							t.Error(closeResult)
						}
					})
				}
				group.Wait()
				if p.DB() != nil || p.SQLDB() != nil {
					t.Fatal("closed client published")
				}
			}
			if opens != 1 {
				t.Fatalf("opens=%d", opens)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestOpenRejectsInvalidResultsAndPanics(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *config.Config
		open Opener
	}{
		{name: "missing config"},
		{name: "nil client", cfg: &config.Config{}, open: func(context.Context, config.DatabaseConfig, config.DBCredentials, gormlogger.Interface) (Client, error) {
			return nil, nil
		}},
		{name: "typed nil", cfg: &config.Config{}, open: func(context.Context, config.DatabaseConfig, config.DBCredentials, gormlogger.Interface) (Client, error) {
			var client *ormx.Client
			return client, nil
		}},
		{name: "panic", cfg: &config.Config{}, open: func(context.Context, config.DatabaseConfig, config.DBCredentials, gormlogger.Interface) (Client, error) {
			panic("factory")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Open(t.Context(), Options{Config: tc.cfg, Open: tc.open})
			if p != nil || !errors.Is(err, core.ErrComponentState) {
				t.Fatalf("owner=%v err=%v", p, err)
			}
		})
	}
}
func TestPostOpenVerificationClosesOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name, version, comment   string
		versionErr, sessionErr   bool
		badSession, closeFailure bool
	}{
		{name: "success", version: "8.0.36", comment: "MySQL"},
		{name: "version query failure", versionErr: true},
		{name: "unsupported vendor", version: "10.6.16-MariaDB", comment: "MariaDB"},
		{name: "too old", version: "5.7.44", comment: "MySQL"},
		{name: "session query failure", version: "8.0.36", comment: "Percona Server", sessionErr: true},
		{name: "session mismatch", version: "8.0.36", comment: "MySQL", badSession: true},
		{name: "verification and close fail", versionErr: true, closeFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, mock := ownedClient(t)
			failure := errors.New("query failed")
			closeErr := errors.New("close failed")
			query := mock.ExpectQuery("SELECT VERSION\\(\\)")
			if tc.versionErr {
				query.WillReturnError(failure)
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"version", "comment"}).AddRow(tc.version, tc.comment))
			}
			validVersion := tc.version == "8.0.36" && !tc.versionErr
			if validVersion {
				query := mock.ExpectQuery("SELECT @@session.time_zone")
				if tc.sessionErr {
					query.WillReturnError(failure)
				} else {
					zone := "+00:00"
					if tc.badSession {
						zone = "+08:00"
					}
					query.WillReturnRows(sqlmock.NewRows([]string{"time_zone", "charset", "collation", "sql_mode"}).AddRow(zone, "utf8mb4", config.DatabaseConfig{}.EffectiveCollation(), "STRICT_TRANS_TABLES"))
				}
			}
			closeExpectation := mock.ExpectClose()
			if tc.closeFailure {
				closeExpectation.WillReturnError(closeErr)
			}
			minVersion, err := parseMinVersion("8.0")
			if err != nil {
				t.Fatal(err)
			}
			err = verifyOpenedClient(t.Context(), client, config.DatabaseConfig{}, minVersion)
			success := validVersion && !tc.sessionErr && !tc.badSession
			if success {
				if err != nil {
					t.Fatal(err)
				}
				if err := client.Close(); err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("verification unexpectedly passed")
			}
			if (tc.versionErr || tc.sessionErr) && !errors.Is(err, failure) {
				t.Fatalf("lost query error %v", err)
			}
			if tc.closeFailure && !errors.Is(err, closeErr) {
				t.Fatalf("lost close error %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
