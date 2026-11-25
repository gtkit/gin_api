package example

import (
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gtkit/gin-api/internal/module/example/application"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestAssemblyScopesSQLAndRollsBack(t *testing.T) {
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
	service, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("INSERT INTO `example_records`").WithArgs(sqlmock.AnyArg(), int64(7), "record", sqlmock.AnyArg(), sqlmock.AnyArg(), nil).WillReturnResult(sqlmock.NewResult(0, 1))
	record, err := service.Create(t.Context(), application.CreateRequest{OwnerID: 7, Title: "record"})
	if err != nil {
		t.Fatal(err)
	}
	persisted := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM `example_records` WHERE .*owner_id = \\? AND id = \\?.*deleted_at.*IS NULL.*FOR UPDATE").
		WithArgs(int64(7), record.ID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "owner_id", "title", "created_at", "updated_at"}).
			AddRow(record.ID, 7, "record", persisted, persisted))
	mock.ExpectCommit()
	repeated, err := service.Update(t.Context(), application.UpdateRequest{OwnerID: 7, ID: record.ID, Title: "record"})
	if err != nil || !repeated.UpdatedAt.Equal(persisted) {
		t.Fatalf("same title did not return the persisted row: %+v, %v", repeated, err)
	}
	mock.ExpectQuery("SELECT .* FROM `example_records` WHERE .*owner_id = \\? AND id = \\?.*deleted_at.*IS NULL").WithArgs(int64(8), record.ID, 1).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	if _, err := service.Get(t.Context(), application.GetRequest{OwnerID: 8, ID: record.ID}); err == nil {
		t.Fatal("cross-owner lookup returned data")
	}
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `example_records` SET `deleted_at`=.*owner_id = \\? AND id IN").WithArgs(sqlmock.AnyArg(), int64(7), record.ID, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()
	if err := service.DeleteMany(t.Context(), application.DeleteManyRequest{OwnerID: 7, IDs: []string{record.ID, uuid.NewV7().String()}}); err == nil {
		t.Fatal("partial batch was committed")
	}
	mock.ExpectExec("UPDATE `example_records` SET `deleted_at`=.*owner_id = \\? AND id = \\?").
		WithArgs(sqlmock.AnyArg(), int64(7), record.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := service.Delete(t.Context(), application.DeleteRequest{OwnerID: 7, ID: record.ID}); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("write rejected")
	mock.ExpectExec("INSERT INTO `example_records`").WillReturnError(failure)
	if _, err := service.Create(t.Context(), application.CreateRequest{OwnerID: 7, Title: "other"}); !errors.Is(err, failure) {
		t.Fatalf("write error lost: %v", err)
	}
}
