package application_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/gtkit/gin-api/internal/models"
	app "github.com/gtkit/gin-api/internal/module/file/application"
	runtimefile "github.com/gtkit/gin-api/internal/runtime/module/file"
)

// A fenced journal fixture lets two application instances share one committed
// state while storage uses the actual local adapter.
type journalFixture struct {
	mu             sync.Mutex
	ops            map[string]models.FileOperation
	files          *fakeRepo
	failPublish    bool
	failCompletion bool
	forceDue       bool
	publications   int
}

func (j *journalFixture) FindOperation(_ context.Context, owner int64, kind, key string) (models.FileOperation, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, op := range j.ops {
		if op.OwnerID == owner && op.Kind == kind && op.BusinessKey == key {
			return op, nil
		}
	}
	return models.FileOperation{}, nil
}

func (j *journalFixture) BeginOperation(_ context.Context, op models.FileOperation) (models.FileOperation, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, existing := range j.ops {
		if existing.OwnerID == op.OwnerID && existing.Kind == op.Kind && existing.BusinessKey == op.BusinessKey {
			return existing, false, nil
		}
	}
	j.ops[op.ID] = op
	return op, true, nil
}

func (j *journalFixture) HideForDeletion(ctx context.Context, op models.FileOperation) (models.FileOperation, bool, error) {
	op, newIntent, err := j.BeginOperation(ctx, op)
	if newIntent && err == nil {
		_, err = j.files.Delete(ctx, op.OwnerID, op.FileID)
	}
	return op, newIntent, err
}

func (j *journalFixture) RegisterWrite(_ context.Context, op models.FileOperation, key string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	current := j.ops[op.ID]
	if current.Generation != op.Generation || current.StorageKey != "" {
		return errors.New("stale write")
	}
	current.StorageKey = key
	j.ops[op.ID] = current
	return nil
}

func (j *journalFixture) PublishOperation(ctx context.Context, op models.FileOperation, f models.File) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	current := j.ops[op.ID]
	if j.failPublish || current.Generation != op.Generation || current.State != models.FileOperationWorking {
		return errors.New("publication rejected")
	}
	_, err := j.files.Create(ctx, app.Record{
		ID: f.ID, OwnerID: f.OwnerID, StorageKey: f.StorageKey, Driver: f.Driver, Bucket: f.Bucket,
		Size: f.Size, Checksum: f.Checksum, FileName: f.FileName,
	})
	if err == nil {
		current.State = models.FileOperationDone
		j.ops[op.ID] = current
		j.publications++
	}
	return err
}

func (j *journalFixture) SetOperationState(_ context.Context, op models.FileOperation, state, reason string, next time.Time) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	current := j.ops[op.ID]
	if j.failCompletion || current.Generation != op.Generation || current.State != models.FileOperationWorking {
		return errors.New("state update rejected")
	}
	current.State, current.Failure, current.NextAt = state, reason, next
	j.ops[op.ID] = current
	return nil
}

func (j *journalFixture) RecoveryCandidates(_ context.Context, namespace string, limit int) ([]models.FileOperation, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	var results []models.FileOperation
	for _, op := range j.ops {
		if op.Namespace == namespace && (op.State == models.FileOperationPending || op.State == models.FileOperationWorking) {
			results = append(results, op)
		}
		if len(results) == limit {
			break
		}
	}
	return results, nil
}

func (j *journalFixture) ClaimOperation(_ context.Context, op models.FileOperation, until time.Time) (models.FileOperation, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	current := j.ops[op.ID]
	if current.Generation != op.Generation || current.State != op.State ||
		!j.forceDue && time.Now().Before(current.NextAt) {
		return op, false, nil
	}
	j.forceDue = false
	op.Generation++
	op.State, op.NextAt = models.FileOperationWorking, until
	j.ops[op.ID] = op
	return op, true, nil
}

func (j *journalFixture) LiveObjectReference(_ context.Context, driver, bucket, key string) (bool, error) {
	j.files.mu.Lock()
	defer j.files.mu.Unlock()
	for _, f := range j.files.byID {
		if f.Driver == driver && f.Bucket == bucket && f.StorageKey == key {
			return true, nil
		}
	}
	return false, nil
}

func (j *journalFixture) FindByObjectIncludingDeleted(context.Context, int64, string) (models.File, error) {
	return models.File{}, nil
}

func (j *journalFixture) PublicationNamespace(context.Context, int64, string) (string, error) {
	return "", nil
}

func (j *journalFixture) PruneCompletedAttempts(context.Context, string, time.Time, int) error {
	return nil
}

func TestUploadRecoveryPublishesOnlyOnceAfterCommitFailure(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo := newFakeRepo()
	journal := &journalFixture{files: repo, ops: map[string]models.FileOperation{}, failPublish: true}
	_, up := newTestService(t, repo, "local")
	opt := app.OperationOptions{Repository: journal, Namespace: "local-volume", MaxFileBytes: 1024, AllowedTypes: []string{".txt"},
		Session: unexpectedSession,
	}
	instance := func() *app.Service {
		t.Helper()
		service, err := app.New(runtimefile.NewTransfer(up), repo, "local", "", opt)
		if err != nil {
			t.Fatal(err)
		}
		return service
	}
	const content = "durable content"
	if _, err := instance().Upload(ctx, 7, "a.txt", strings.NewReader(content), int64(len(content))); !errors.Is(err, app.ErrDependencyUnavailable) {
		t.Fatalf("upload returned %v after publication failure", err)
	}
	var op models.FileOperation
	for _, row := range journal.ops {
		op = row
	}
	if op.StorageKey == "" || string(readBack(t, up, op.StorageKey)) != content {
		t.Fatal("object was not durable before publication")
	}
	if _, found, _ := repo.Find(ctx, 7, op.FileID); found {
		t.Fatal("pending file became visible")
	}
	journal.failPublish, journal.forceDue = false, true
	first, second := instance(), instance()
	var wg sync.WaitGroup
	for _, s := range []*app.Service{first, second} {
		wg.Go(func() {
			if err := s.RecoverOperations(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if journal.publications != 1 {
		t.Fatalf("file published %d times", journal.publications)
	}
	if _, _, err := second.Download(ctx, 8, op.FileID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("cross-owner download = %v", err)
	}
	if err := second.Delete(ctx, 8, op.FileID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("cross-owner delete = %v", err)
	}
	journal.failCompletion = true
	if err := first.Delete(ctx, 7, op.FileID); !errors.Is(err, app.ErrDependencyUnavailable) {
		t.Fatalf("delete after failed completion = %v", err)
	}
	if _, err := first.Get(ctx, 7, op.FileID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("file is still visible after logical delete: %v", err)
	}
	if exists, err := up.Storage().Exists(ctx, op.StorageKey); err != nil || exists {
		t.Fatalf("physical delete: exists=%t err=%v", exists, err)
	}
	journal.failCompletion, journal.forceDue = false, true
	if err := second.RecoverOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if err := second.Delete(ctx, 7, op.FileID); err != nil {
		t.Fatalf("delete replay = %v", err)
	}
}

func TestRecoveryDoesNotPublishUnverifiedObject(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo := newFakeRepo()
	_, up := newTestService(t, repo, "local")
	journal := &journalFixture{files: repo, ops: map[string]models.FileOperation{}, forceDue: true}
	service, err := app.New(runtimefile.NewTransfer(up), repo, "local", "", app.OperationOptions{
		Repository: journal, Namespace: "local", MaxFileBytes: 1024, AllowedTypes: []string{".txt"}, Session: unexpectedSession,
	})
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewV7().String()
	op := models.FileOperation{ID: id, FileID: id, OwnerID: 7, Kind: models.FileMergeOperation,
		BusinessKey: "known-session", Driver: "local", Namespace: "local", State: models.FileOperationWorking,
		Generation: 1, StorageKey: "unfinished", Size: 3}
	journal.ops[id] = op
	if err := up.Storage().Save(ctx, op.StorageKey, strings.NewReader("abc"), 3); err != nil {
		t.Fatal(err)
	}
	if err := service.RecoverOperations(ctx); !errors.Is(err, app.ErrDependencyUnavailable) {
		t.Fatalf("unverified object did not produce a review diagnostic: %v", err)
	}
	if journal.ops[op.ID].State != models.FileOperationReview || repo.len() != 0 {
		t.Fatal("object without completion evidence was published")
	}
	stream, err := up.Storage().Get(ctx, op.StorageKey)
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(stream)
	if err := errors.Join(err, stream.Close()); err != nil || string(content) != "abc" {
		t.Fatalf("quarantined object: size=%d err=%v", len(content), err)
	}
}

func unexpectedSession(context.Context, string) (app.ChunkSession, bool, error) {
	return app.ChunkSession{}, false, errors.New("unexpected upload session lookup")
}
