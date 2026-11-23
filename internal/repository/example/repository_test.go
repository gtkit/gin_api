package example

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newExampleRepository(t *testing.T) (*Repository, sqlmock.Sqlmock) {
	t.Helper()
	conn, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		mock.ExpectClose()
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	db, err := gorm.Open(gormmysql.New(gormmysql.Config{Conn: conn, SkipInitializeWithVersion: true}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	return New(db), mock
}

func TestListTerminalPageSkipsCount(t *testing.T) {
	t.Parallel()

	repo, mock := newExampleRepository(t)
	mock.ExpectQuery("SELECT `id`,`title`,`created_at`,`updated_at` FROM `example_records`").
		WithArgs(int64(7), 20).
		WillReturnRows(sqlmock.NewRows([]string{"id", "title", "created_at", "updated_at"}).
			AddRow("id-1", "title", time.Now(), time.Now()))
	records, total, err := repo.List(t.Context(), 7, 0, 20)
	if err != nil || len(records) != 1 || total != 1 {
		t.Fatalf("terminal page = (%d records, %d total, %v)", len(records), total, err)
	}
}

func TestListDeepEmptyPageStillCounts(t *testing.T) {
	t.Parallel()

	repo, mock := newExampleRepository(t)
	mock.ExpectQuery("SELECT `id`,`title`,`created_at`,`updated_at` FROM `example_records`").
		WithArgs(int64(7), 20, 100).
		WillReturnRows(sqlmock.NewRows([]string{"id", "title", "created_at", "updated_at"}))
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `example_records` WHERE owner_id = \\?").
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	records, total, err := repo.List(t.Context(), 7, 100, 20)
	if err != nil || len(records) != 0 || total != 1 {
		t.Fatalf("deep empty page = (%d records, %d total, %v)", len(records), total, err)
	}
}
