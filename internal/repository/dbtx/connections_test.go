package dbtx

import (
	"context"
	"errors"
	"testing"
)

func TestManagedTransactionRejectsOtherDatabaseRepository(t *testing.T) {
	m, mock := mockManager(t)
	other, otherMock := mockManager(t)
	mock.ExpectBegin()
	mock.ExpectRollback()
	err := m.Do(t.Context(), func(ctx context.Context) error {
		if err := DB(ctx, other.db).Exec("SELECT 1").Error; !errors.Is(err, ErrForeignTransaction) {
			t.Errorf("foreign repository did not reject transaction: %v", err)
		}
		// Even a swallowed query error must not commit the unsafe transaction.
		return nil
	})
	if !errors.Is(err, ErrForeignTransaction) {
		t.Fatalf("foreign transaction committed: %v", err)
	}
	if err := otherMock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
