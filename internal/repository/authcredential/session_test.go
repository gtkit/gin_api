package authcredential

import (
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRegisterOmitsVersionWhenDisabled(t *testing.T) {
	repo, mock := sessionRepository(t)
	repo.withVersion = false
	mock.ExpectBegin()
	columns := "INSERT INTO `users` (`nickname`,`gender`,`avatar`,`phone`,`email`,`password`,`status`,`vip_status`,`vip_level`,`vip_expire_at`,`follow`,`created_at`,`updated_at`,`deleted_at`)"
	mock.ExpectExec(regexp.QuoteMeta(columns)).WillReturnResult(sqlmock.NewResult(7, 1))
	mock.ExpectCommit()
	uid, err := repo.Create(t.Context(), "account", "hashed", "name")
	if err != nil || uid != 7 {
		t.Fatalf("registration requires a migrated column: uid=%d err=%v", uid, err)
	}
}

func sessionRepository(t *testing.T) (*Repository, sqlmock.Sqlmock) {
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
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: conn, SkipInitializeWithVersion: true}),
		&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return NewWithSessionVersion(db), mock
}

func TestCredentialReadUsesVersionOnlyWhenEnabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			repo, mock := sessionRepository(t)
			repo.withVersion = enabled
			query := "SELECT `id`,`password`,`status` FROM `users`"
			columns := []string{"id", "password", "status"}
			rows := sqlmock.NewRows(columns).AddRow(7, "hashed", 1)
			if enabled {
				query = "SELECT `id`,`password`,`status`,`auth_version` FROM `users`"
				rows = sqlmock.NewRows(append(columns, "auth_version")).AddRow(7, "hashed", 1, 3)
			}
			mock.ExpectQuery(query).WithArgs("account", 1).WillReturnRows(rows)
			cred, found, err := repo.FindByPhone(t.Context(), "account")
			if err != nil || !found || !cred.Active || cred.UserID != 7 || (enabled && cred.AuthVersion != 3) {
				t.Fatalf("credential=%+v found=%v err=%v", cred, found, err)
			}
		})
	}
}

func TestSessionWritesCompareAndIncrement(t *testing.T) {
	for _, password := range []bool{false, true} {
		for _, outcome := range []string{"success", "conflict", "database error"} {
			t.Run(map[bool]string{false: "logout-all/", true: "password/"}[password]+outcome, func(t *testing.T) {
				repo, mock := sessionRepository(t)
				mock.ExpectBegin()
				query := "UPDATE `users` SET `auth_version`=auth_version \\+ 1,`updated_at`=\\? WHERE \\(id = \\? AND status = \\? AND auth_version = \\?\\) AND `users`.`deleted_at` IS NULL"
				var exec *sqlmock.ExpectedExec
				if password {
					query = "UPDATE `users` SET `auth_version`=auth_version \\+ 1,`password`=\\?,`updated_at`=\\? WHERE \\(id = \\? AND status = \\? AND auth_version = \\? AND password = \\?\\) AND `users`.`deleted_at` IS NULL"
					exec = mock.ExpectExec(query).WithArgs("new-hash", sqlmock.AnyArg(), 7, 1, 3, "old-hash")
				} else {
					exec = mock.ExpectExec(query).WithArgs(sqlmock.AnyArg(), 7, 1, 3)
				}
				failure := errors.New("database unavailable")
				switch outcome {
				case "database error":
					exec.WillReturnError(failure)
					mock.ExpectRollback()
				case "conflict":
					exec.WillReturnResult(sqlmock.NewResult(0, 0))
					mock.ExpectCommit()
				default:
					exec.WillReturnResult(sqlmock.NewResult(0, 1))
					mock.ExpectCommit()
				}
				var changed bool
				var err error
				if password {
					changed, err = repo.ChangePassword(t.Context(), 7, 3, "old-hash", "new-hash")
				} else {
					changed, err = repo.RevokeSessions(t.Context(), 7, 3)
				}
				if changed != (outcome == "success") || (outcome == "database error") != errors.Is(err, failure) {
					t.Fatalf("changed=%v err=%v", changed, err)
				}
			})
		}
	}
}

func TestSessionWritesRejectInvalidVersionWithoutSQL(t *testing.T) {
	repo, _ := sessionRepository(t)
	for _, version := range []int64{-1, 1<<63 - 1} {
		if changed, err := repo.ChangePassword(t.Context(), 7, version, "old", "new"); changed || err != nil {
			t.Fatalf("invalid password version changed=%v err=%v", changed, err)
		}
		if changed, err := repo.RevokeSessions(t.Context(), 7, version); changed || err != nil {
			t.Fatalf("invalid revoke version changed=%v err=%v", changed, err)
		}
	}
}
