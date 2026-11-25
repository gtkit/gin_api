package file

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gtkit/gin-api/internal/module/file/application"
	filerepo "github.com/gtkit/gin-api/internal/repository/file"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestListDatabaseBudgetAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout time.Duration
		delay   time.Duration
		dbErr   error
		want    error
	}{
		{"slow statement", 15 * time.Millisecond, time.Minute, nil, context.DeadlineExceeded},
		{"connection refused", time.Second, 0, &net.OpError{Op: "dial", Err: errors.New("unreachable")}, application.ErrDependencyUnavailable},
		{"ordinary query error", time.Second, 0, gorm.ErrInvalidField, gorm.ErrInvalidField},
		{"disabled uses existing error", 0, 0, gorm.ErrInvalidField, gorm.ErrInvalidField},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			query := mock.ExpectQuery("SELECT .* FROM .*files.* WHERE owner_id =").WithArgs(int64(7), 20)
			if tc.dbErr == nil {
				query.WillDelayFor(tc.delay).WillReturnRows(sqlmock.NewRows([]string{"id"}))
			} else {
				query.WillReturnError(tc.dbErr)
			}
			adapter := adapter{repo: filerepo.New(db), listTimeout: tc.timeout}
			rows, total, err := adapter.List(t.Context(), 7, 0, 20)
			if !errors.Is(err, tc.want) || rows != nil || total != 0 {
				t.Fatalf("List = %v, %d, %v, want empty failed result with %v", rows, total, err, tc.want)
			}
		})
	}
}
