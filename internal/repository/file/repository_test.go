package file

import (
	"errors"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/gtkit/gin-api/internal/models"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func newMockRepo(t *testing.T) (*Repository, sqlmock.Sqlmock) {
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
	db, err := gorm.Open(gormmysql.New(gormmysql.Config{Conn: conn, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	return New(db), mock
}

func fileRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "owner_id", "storage_key", "driver", "created_at"}).
		AddRow("f-1", int64(7), "user-7/a.txt", "local", time.Now())
}

const deepPageQuery = "FROM `files` JOIN (SELECT id AS page_id,created_at AS page_created_at FROM `files` " +
	"WHERE owner_id = ? AND `files`.`deleted_at` IS NULL ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?) " +
	"AS file_page ON file_page.page_id = files.id WHERE `files`.`deleted_at` IS NULL " +
	"ORDER BY file_page.page_created_at DESC,file_page.page_id DESC"

func TestFindScopesOwnerAndMissingIsCommaOK(t *testing.T) {
	t.Parallel()

	repo, mock := newMockRepo(t)
	mock.ExpectQuery("SELECT .* FROM `files` WHERE \\(owner_id = \\? AND id = \\?\\)").
		WithArgs(int64(7), "f-1", 1).WillReturnRows(fileRows())
	record, found, err := repo.Find(t.Context(), 7, "f-1")
	if err != nil || !found || record.StorageKey != "user-7/a.txt" {
		t.Fatalf("Find() = (%+v, %v, %v)", record, found, err)
	}

	mock.ExpectQuery("SELECT .* FROM `files` WHERE \\(owner_id = \\? AND id = \\?\\)").
		WithArgs(int64(8), "f-1", 1).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	if _, found, err := repo.Find(t.Context(), 8, "f-1"); err != nil || found {
		t.Fatalf("cross-owner Find() = (%v, %v), want (false, nil)", found, err)
	}
}

func TestCreateDuplicateKeyIsIdempotent(t *testing.T) {
	t.Parallel()

	repo, mock := newMockRepo(t)
	record := models.File{ID: "f-1", OwnerID: 7, StorageKey: "user-7/a.txt", Driver: "local"}

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `files`").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	stored, err := repo.Create(t.Context(), record)
	if err != nil || stored.ID != record.ID {
		t.Fatalf("Create() = %v", err)
	}

	// 重复键：INSERT 失败后按 (owner, storage_key) 查到已有记录 → 幂等成功。
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `files`").WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
	mock.ExpectRollback()
	mock.ExpectQuery("SELECT .* FROM `files` WHERE \\(owner_id = \\? AND storage_key = \\?\\)").
		WithArgs(int64(7), "user-7/a.txt", 1).WillReturnRows(fileRows())
	stored, err = repo.Create(t.Context(), record)
	if err != nil || stored.ID != "f-1" {
		t.Fatalf("duplicate Create() = (%+v, %v), want existing record", stored, err)
	}

	// 真实故障：非重复 INSERT 失败直接上抛，不再额外查询存储键。
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `files`").WillReturnError(errors.New("connection refused"))
	mock.ExpectRollback()
	if _, err := repo.Create(t.Context(), record); err == nil {
		t.Fatal("failed Create() = nil, want original error")
	}
}

func TestListClampsLimit(t *testing.T) {
	t.Parallel()

	repo, mock := newMockRepo(t)
	mock.ExpectQuery("SELECT .* FROM `files` WHERE owner_id = \\?").
		WithArgs(int64(7), 100).WillReturnRows(fileRows())
	records, total, err := repo.List(t.Context(), 7, 0, 500)
	if err != nil || total != 1 || len(records) != 1 {
		t.Fatalf("List() = (%d records, total %d, %v)", len(records), total, err)
	}
}

func TestListCountsWhenOffsetPageIsEmpty(t *testing.T) {
	t.Parallel()

	repo, mock := newMockRepo(t)
	mock.ExpectQuery(regexp.QuoteMeta(deepPageQuery)).
		WithArgs(int64(7), 100, 100).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `files` WHERE owner_id = \\?").
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	if records, total, err := repo.List(t.Context(), 7, 100, 100); err != nil || len(records) != 0 || total != 1 {
		t.Fatalf("empty deep page = (%d records, %d total, %v), want (0, 1, nil)", len(records), total, err)
	}
}

func TestListDerivesTotalFromShortDeepPage(t *testing.T) {
	t.Parallel()

	repo, mock := newMockRepo(t)
	mock.ExpectQuery(regexp.QuoteMeta(deepPageQuery)).
		WithArgs(int64(7), 20, 100).WillReturnRows(fileRows())
	records, total, err := repo.List(t.Context(), 7, 100, 20)
	if err != nil || total != 101 || len(records) != 1 || records[0].StorageKey != "user-7/a.txt" {
		t.Fatalf("short deep page = (%+v, %d total, %v)", records, total, err)
	}
}

func TestListPreservesDeepPageQueryError(t *testing.T) {
	t.Parallel()

	repo, mock := newMockRepo(t)
	failure := errors.New("database unavailable")
	mock.ExpectQuery(regexp.QuoteMeta(deepPageQuery)).WithArgs(int64(7), 20, 100).WillReturnError(failure)
	if records, total, err := repo.List(t.Context(), 7, 100, 20); records != nil || total != 0 || !errors.Is(err, failure) {
		t.Fatalf("failed deep page = (%+v, %d total, %v)", records, total, err)
	}
}

func TestListCountsFullPage(t *testing.T) {
	t.Parallel()

	repo, mock := newMockRepo(t)
	rows := sqlmock.NewRows([]string{"id", "owner_id", "storage_key", "driver", "created_at"})
	for i := range 100 {
		rows.AddRow("f-"+strconv.Itoa(i), int64(7), "user-7/file"+strconv.Itoa(i), "local", time.Now())
	}
	mock.ExpectQuery("SELECT .* FROM `files` WHERE owner_id = \\?").
		WithArgs(int64(7), 100).WillReturnRows(rows)
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `files` WHERE owner_id = \\?").
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(100))
	if records, total, err := repo.List(t.Context(), 7, 0, 100); err != nil || len(records) != 100 || total != 100 {
		t.Fatalf("full page = (%d records, %d total, %v), want (100, 100, nil)", len(records), total, err)
	}
}

func TestDeleteScopesOwner(t *testing.T) {
	t.Parallel()

	repo, mock := newMockRepo(t)
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `files` SET `deleted_at`=\\? WHERE \\(owner_id = \\? AND id = \\?\\)").
		WithArgs(sqlmock.AnyArg(), int64(7), "f-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	deleted, err := repo.Delete(t.Context(), 7, "f-1")
	if err != nil || !deleted {
		t.Fatalf("Delete() = (%v, %v)", deleted, err)
	}
}

func TestListAfterScopesAndBoundsEverySeek(t *testing.T) {
	r, m := newMockRepo(t)
	at := time.Now()
	order := " AND `files`.`deleted_at` IS NULL ORDER BY owner_id DESC,created_at DESC,id DESC LIMIT ?"
	m.ExpectQuery(regexp.QuoteMeta("FROM files FORCE INDEX (idx_files_owner) WHERE owner_id = ?"+order)).
		WithArgs(7, 101).WillReturnRows(fileRows())
	if _, err := r.ListAfter(t.Context(), 7, time.Time{}, "", 100); err != nil {
		t.Fatal(err)
	}
	m.ExpectQuery(regexp.QuoteMeta("WHERE owner_id = ? AND ((created_at < ? OR (created_at = ? AND id < ?) OR created_at IS NULL))"+order)).WithArgs(7, at, at, "last", 2).WillReturnRows(fileRows())
	if _, err := r.ListAfter(t.Context(), 7, at, "last", 1); err != nil {
		t.Fatal(err)
	}
	nullOrder := " AND `files`.`deleted_at` IS NULL ORDER BY owner_id DESC,id DESC LIMIT ?"
	m.ExpectQuery(regexp.QuoteMeta("WHERE owner_id = ? AND (created_at IS NULL AND id < ?)"+nullOrder)).
		WithArgs(7, "last", 2).WillReturnRows(fileRows())
	if _, err := r.ListAfter(t.Context(), 7, time.Time{}, "last", 1); err != nil {
		t.Fatal(err)
	}
}
