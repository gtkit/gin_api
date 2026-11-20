package file

import (
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gtkit/gin-api/internal/models"
	"gorm.io/driver/mysql"
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
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: conn, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	return New(db), mock
}

func fileRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "owner_id", "storage_key", "driver", "created_at"}).
		AddRow("f-1", int64(7), "user-7/a.txt", "local", time.Now())
}

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
	if err := repo.Create(t.Context(), record); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	// 重复键：INSERT 失败后按 (owner, storage_key) 查到已有记录 → 幂等成功。
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `files`").WillReturnError(errors.New("Error 1062: Duplicate entry"))
	mock.ExpectRollback()
	mock.ExpectQuery("SELECT .* FROM `files` WHERE \\(owner_id = \\? AND storage_key = \\?\\)").
		WithArgs(int64(7), "user-7/a.txt", 1).WillReturnRows(fileRows())
	if err := repo.Create(t.Context(), record); err != nil {
		t.Fatalf("duplicate Create() = %v, want nil (idempotent)", err)
	}

	// 真实故障：INSERT 失败且查无记录 → 原错误上抛，不伪装成功。
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `files`").WillReturnError(errors.New("connection refused"))
	mock.ExpectRollback()
	mock.ExpectQuery("SELECT .* FROM `files` WHERE \\(owner_id = \\? AND storage_key = \\?\\)").
		WithArgs(int64(7), "user-7/a.txt", 1).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	if err := repo.Create(t.Context(), record); err == nil {
		t.Fatal("failed Create() = nil, want original error")
	}
}

func TestListClampsLimit(t *testing.T) {
	t.Parallel()

	repo, mock := newMockRepo(t)
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `files` WHERE owner_id = \\?").
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("SELECT .* FROM `files` WHERE owner_id = \\?").
		WithArgs(int64(7), 100).WillReturnRows(fileRows())
	records, total, err := repo.List(t.Context(), 7, 0, 500)
	if err != nil || total != 1 || len(records) != 1 {
		t.Fatalf("List() = (%d records, total %d, %v)", len(records), total, err)
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
