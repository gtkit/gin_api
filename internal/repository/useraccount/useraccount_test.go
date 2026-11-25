package useraccount

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gtkit/gin-api/internal/models"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newAccountRepository(t *testing.T) (*Repository, sqlmock.Sqlmock) {
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

func TestValidateUserUsesExistenceProbe(t *testing.T) {
	t.Parallel()

	repo, mock := newAccountRepository(t)
	mock.ExpectQuery("SELECT `id` FROM `users`").
		WithArgs(int64(7), models.UserStatusNormal, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
	if ok, err := repo.ValidateUser(t.Context(), 7); err != nil || !ok {
		t.Fatalf("active account = (%v, %v), want (true, nil)", ok, err)
	}

	mock.ExpectQuery("SELECT `id` FROM `users`").
		WithArgs(int64(8), models.UserStatusNormal, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	if ok, err := repo.ValidateUser(t.Context(), 8); err != nil || ok {
		t.Fatalf("missing account = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestValidateSessionPreservesDatabaseErrors(t *testing.T) {
	t.Parallel()

	repo, mock := newAccountRepository(t)
	failure := errors.New("database unavailable")
	mock.ExpectQuery("SELECT `id` FROM `users`").
		WithArgs(int64(7), models.UserStatusNormal, int64(3), 1).
		WillReturnError(failure)
	ok, err := repo.ValidateSession(t.Context(), 7, 3)
	if ok || !errors.Is(err, failure) {
		t.Fatalf("database failure = (%v, %v), want (false, wrapped failure)", ok, err)
	}
}
