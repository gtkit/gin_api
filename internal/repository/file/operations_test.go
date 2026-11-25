package file

import (
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gtkit/gin-api/internal/models"
)

func TestRegisterWriteRollsBackOnAttemptInsertFailure(t *testing.T) {
	repo, mock := newMockRepo(t)
	op := models.FileOperation{ID: "operation", OwnerID: 7, Namespace: "local", Generation: 2}
	injected := errors.New("cannot persist attempt")
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE .*file_operations.*storage_key").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO .*file_operations").WillReturnError(injected)
	mock.ExpectRollback()
	if err := repo.RegisterWrite(t.Context(), op, "object"); !errors.Is(err, injected) {
		t.Fatalf("RegisterWrite = %v", err)
	}
}

func TestPublishOperationFencesStaleGeneration(t *testing.T) {
	repo, mock := newMockRepo(t)
	op := models.FileOperation{ID: "operation", OwnerID: 7, Namespace: "local", Generation: 3, StorageKey: "object"}
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE .*file_operations.*generation.*storage_key").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	if err := repo.PublishOperation(t.Context(), op, models.File{}); !errors.Is(err, ErrOperationChanged) {
		t.Fatalf("PublishOperation = %v", err)
	}
}

func TestClaimOperationAllowsOnlyOneGeneration(t *testing.T) {
	repo, mock := newMockRepo(t)
	op := models.FileOperation{ID: "operation", OwnerID: 7, Namespace: "local", State: models.FileOperationPending, Generation: 3}
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE .*file_operations.*generation.*next_at").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	got, claimed, err := repo.ClaimOperation(t.Context(), op, time.Now().Add(time.Minute))
	if err != nil || !claimed || got.Generation != op.Generation+1 {
		t.Fatalf("first claim: generation=%d claimed=%t err=%v", got.Generation, claimed, err)
	}
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE .*file_operations.*generation.*next_at").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	if _, claimed, err := repo.ClaimOperation(t.Context(), op, time.Now().Add(time.Minute)); err != nil || claimed {
		t.Fatalf("stale claim: claimed=%t err=%v", claimed, err)
	}
}
