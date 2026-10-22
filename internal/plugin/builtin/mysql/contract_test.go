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

type configurationValue struct {
	cfg   *config.Config
	err   error
	calls int
}

func (c *configurationValue) Value() (*config.Config, error) { c.calls++; return c.cfg, c.err }
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
		{name: "success"}, {name: "partial error", partial: true}, {name: "late cancellation", cancel: true}, {name: "partial plus close error", partial: true, closeFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, mock := ownedClient(t)
			cfg := &config.Config{Database: config.DatabaseConfig{Host: "unused", Username: "runtime"}}
			provider := &configurationValue{cfg: cfg}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			root := errors.New("open failed")
			closeErr := errors.New("close failed")
			opens := 0
			p := New(Options{Configuration: provider, SelectCredentials: func(config.DatabaseConfig) config.DBCredentials { return config.DBCredentials{Username: "selected"} }, Open: func(callCtx context.Context, c config.DatabaseConfig, creds config.DBCredentials, logger gormlogger.Interface) (Client, error) {
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
			if provider.calls != 0 || opens != 0 {
				t.Fatal("constructor performed I/O")
			}
			expected := mock.ExpectClose()
			if tc.closeFailure {
				expected.WillReturnError(closeErr)
			}
			err := p.Init(ctx)
			if tc.partial && !errors.Is(err, root) {
				t.Fatalf("missing open root: %v", err)
			}
			if tc.cancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("missing cancellation: %v", err)
			}
			if tc.closeFailure && !errors.Is(err, closeErr) {
				t.Fatalf("missing close error: %v", err)
			}
			if !tc.partial && !tc.cancel {
				if err != nil {
					t.Fatal(err)
				}
				if err := p.Start(t.Context()); err != nil {
					t.Fatal(err)
				}
			} else if p.DB() != nil {
				t.Fatal("failed resource published")
			}
			if again := p.Init(t.Context()); again != err {
				t.Fatalf("Init result changed: first=%v second=%v", err, again)
			}
			if opens != 1 || provider.calls != 1 {
				t.Fatalf("reopened: %d reads %d", opens, provider.calls)
			}
			var group sync.WaitGroup
			for range 16 {
				group.Go(func() {
					stopErr := p.Stop(context.Background())
					if tc.closeFailure {
						if !errors.Is(stopErr, closeErr) {
							t.Errorf("Stop missing close cause: %v", stopErr)
						}
					} else if stopErr != nil {
						t.Errorf("Stop: %v", stopErr)
					}
				})
			}
			group.Wait()
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			if err := p.Start(t.Context()); err == nil {
				t.Fatal("started after stop")
			}
		})
	}
}
func TestConfigRejectionAndTypedNil(t *testing.T) {
	failure := errors.New("configuration failed")
	for _, tc := range []struct {
		name     string
		provider *configurationValue
		typedNil bool
		want     error
	}{
		{"missing", nil, false, nil}, {"provider error", &configurationValue{err: failure}, false, failure}, {"nil config", &configurationValue{}, false, core.ErrPluginContractViolation}, {"typed nil", &configurationValue{cfg: &config.Config{}}, true, core.ErrPluginContractViolation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opens := 0
			opts := Options{Open: func(context.Context, config.DatabaseConfig, config.DBCredentials, gormlogger.Interface) (Client, error) {
				opens++
				var client *ormx.Client
				return client, nil
			}}
			if tc.provider != nil {
				opts.Configuration = tc.provider
			}
			p := New(opts)
			err := p.Init(t.Context())
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("init=%v", err)
			}
			if !tc.typedNil && opens != 0 {
				t.Fatal("opened without configuration")
			}
			if err := p.Stop(t.Context()); err != nil {
				t.Errorf("Stop: %v", err)
			}
		})
	}
	p := New(Options{})
	if err := p.Stop(t.Context()); err != nil {
		t.Errorf("Stop: %v", err)
	}
	if !errors.Is(p.Init(t.Context()), core.ErrPluginContractViolation) {
		t.Fatal("restarted after stop")
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
