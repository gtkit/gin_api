package mysql

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/ormx"
	gormmysql "gorm.io/driver/mysql"
)

func TestDriverInitializesFromActualVersion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, version string
		legacy, tidb  bool
	}{
		{name: "source distribution", version: "8.0.28"},
		{name: "ubuntu", version: "8.0.36-0ubuntu0.22.04.1"},
		{name: "percona", version: "8.0.35-27"},
		{name: "mysql 5.7", version: "5.7.44", legacy: true},
		{name: "mariadb", version: "10.11.6-MariaDB", legacy: true},
		{name: "tidb", version: "8.0.11-TiDB-v8.1.2", tidb: true},
		{name: "custom build", version: "custom-build"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, migration := range []bool{false, true} {
				sqlDB, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					mock.ExpectClose()
					if err := sqlDB.Close(); err != nil {
						t.Error(err)
					}
				})
				mock.ExpectPing()
				mock.ExpectQuery("^SELECT VERSION\\(\\)$").WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(tc.version))
				opts := serverOpenOptions(testDBConfig(3600), config.DBCredentials{})
				if migration {
					opts = migrationOpenOptions(testDBConfig(3600), config.DBCredentials{})
				}
				client, err := ormx.OpenWithDB(t.Context(), sqlDB, opts...)
				if err != nil {
					t.Fatal(err)
				}
				if err := client.Close(); err != nil {
					t.Fatal(err)
				}
				dialect, ok := client.DB().Dialector.(*gormmysql.Dialector)
				if !ok || dialect.ServerVersion != tc.version || dialect.SkipInitializeWithVersion {
					t.Fatalf("driver did not negotiate the actual version: %#v", dialect)
				}
				if dialect.DontSupportRenameColumn != tc.legacy || dialect.DontSupportRenameColumnUnique != tc.tidb {
					t.Fatalf("wrong compatibility flags for %s: %#v", tc.version, dialect.Config)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestDriverInitializationPreservesFailure(t *testing.T) {
	t.Parallel()
	sqlDB, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		mock.ExpectClose()
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	failure := errors.New("version query failed")
	mock.ExpectPing()
	mock.ExpectQuery("^SELECT VERSION\\(\\)$").WillReturnError(failure)
	client, err := ormx.OpenWithDB(t.Context(), sqlDB, serverOpenOptions(testDBConfig(3600), config.DBCredentials{})...)
	if client != nil || !errors.Is(err, failure) {
		t.Fatalf("client=%v err=%v", client, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
