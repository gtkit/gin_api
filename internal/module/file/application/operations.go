package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/pkg/errx"
	"github.com/gtkit/gouploader"
	"github.com/gtkit/gouploader/chunkstore"
)

// OperationRepository is the transaction boundary for a file and its journal.
type OperationRepository interface {
	FindOperation(context.Context, int64, string, string) (models.FileOperation, error)
	BeginOperation(context.Context, models.FileOperation) (models.FileOperation, bool, error)
	HideForDeletion(context.Context, models.FileOperation) (models.FileOperation, bool, error)
	RegisterWrite(context.Context, models.FileOperation, string) error
	PublishOperation(context.Context, models.FileOperation, models.File) error
	SetOperationState(context.Context, models.FileOperation, string, string, time.Time) error
	RecoveryCandidates(context.Context, string, int) ([]models.FileOperation, error)
	ClaimOperation(context.Context, models.FileOperation, time.Time) (models.FileOperation, bool, error)
	LiveObjectReference(context.Context, string, string, string) (bool, error)
	PublicationNamespace(context.Context, int64, string) (string, error)
	FindByObjectIncludingDeleted(context.Context, int64, string) (models.File, error)
	PruneCompletedAttempts(context.Context, string, time.Time, int) error
}

const operationReplayWindow = 7 * 24 * time.Hour

type OperationOptions struct {
	Repository   OperationRepository
	Namespace    string
	MaxFileBytes int64
	AllowedTypes []string
	Session      func(context.Context, string) (*chunkstore.Session, bool, error)
	Merge        func(context.Context, string, func(context.Context, string) error) (*gouploader.UploadResult, error)
}

func (s *Service) newOperation(ctx context.Context, owner int64, kind, key string) models.FileOperation {
	now := time.Now().UTC()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = now.Add(5 * time.Minute)
	}
	lease := deadline.UTC().Add(15 * time.Second)
	id := uuid.NewV7().String()
	if key == "" {
		key = id
	}
	return models.FileOperation{
		ID: id, OwnerID: owner, Kind: kind, BusinessKey: key, FileID: id,
		Driver: s.driver, Bucket: s.bucket, Namespace: s.operations.Namespace,
		State: models.FileOperationWorking, Generation: 1, LeaseUntil: &lease, NextAt: lease,
		CreatedAt: now, UpdatedAt: now,
	}
}

func (s *Service) uploadRecoverable(ctx context.Context, owner int64, name string, source io.ReadSeeker) (Record, error) {
	size, checksum, err := uploadDigest(ctx, source, s.operations.MaxFileBytes)
	if err != nil {
		return Record{}, err
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return Record{}, fmt.Errorf("rewind upload: %w", err)
	}
	return s.UploadPrepared(ctx, owner, name, source, size, checksum)
}

// UploadPrepared receives content that the transport has staged and hashed in one pass.
func (s *Service) UploadPrepared(ctx context.Context, owner int64, name string, source io.ReadSeeker, size int64, checksum string) (Record, error) {
	if s.operations == nil {
		return s.Upload(ctx, owner, name, source, size)
	}
	if err := validOwner(ctx, owner); err != nil {
		return Record{}, err
	}
	name = filepath.Base(name)
	if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
		name = name[i+1:]
	}
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 255 || strings.ContainsAny(name, "\x00\r\n") {
		return Record{}, ErrInvalid
	}
	ext := filepath.Ext(name)
	if !slices.ContainsFunc(s.operations.AllowedTypes, func(allowed string) bool { return strings.EqualFold(allowed, ext) }) {
		return Record{}, gouploader.ErrFileTypeNotAllow
	}
	if size < 0 || !validSHA256(checksum) {
		return Record{}, ErrInvalid
	}
	if size > s.operations.MaxFileBytes {
		return Record{}, gouploader.ErrFileTooLarge
	}

	repo := s.operations.Repository
	op := s.newOperation(ctx, owner, models.FileUploadOperation, "")
	op.FileName, op.Size, op.Checksum, op.Algorithm = name, size, checksum, "sha256"
	op, started, err := repo.BeginOperation(ctx, op)
	if err != nil || !started {
		return Record{}, operationError("begin upload", err)
	}
	key := "files/" + op.ID[len(op.ID)-2:] + "/" + op.ID
	if err := repo.RegisterWrite(ctx, op, key); err != nil {
		return Record{}, operationError("register upload object", err)
	}
	op.StorageKey = key
	if err := s.up.Storage().Save(ctx, key, contextReader{ctx, source}, size); err != nil {
		return Record{}, operationError("save upload object", err)
	}
	return s.publish(ctx, op)
}

func (s *Service) mergeRecoverable(ctx context.Context, owner int64, uploadID string) (Record, error) {
	if !validID(uploadID) {
		return Record{}, gouploader.ErrSessionNotFound
	}
	repo := s.operations.Repository
	existing, err := repo.FindOperation(ctx, owner, models.FileMergeOperation, uploadID)
	if err != nil {
		return Record{}, operationError("find merge", err)
	}
	if existing.ID != "" {
		return s.replayMerge(ctx, existing)
	}

	session, bound, err := s.operations.Session(ctx, uploadID)
	if err != nil {
		return Record{}, err
	}
	if session.UserID != strconv.FormatInt(owner, 10) {
		return Record{}, gouploader.ErrUnauthorized
	}
	// Legacy completed sessions can only replay an already committed file.
	if session.Merged {
		f, err := repo.FindByObjectIncludingDeleted(ctx, owner, session.MergeKey)
		if err != nil {
			return Record{}, operationError("find legacy merge", err)
		}
		if f.ID == "" || f.DeletedAt.Valid || !s.matchesStorageLocation(Record{Driver: f.Driver, Bucket: f.Bucket}) {
			return Record{}, ErrNotFound
		}
		return s.Get(ctx, owner, f.ID)
	}
	if !bound || s.driver == "local" && (!session.Assemble || session.StorageKey == "") {
		return Record{}, ErrStorageLocationMismatch
	}
	if len(session.Uploaded) != session.TotalChunks {
		return Record{}, gouploader.ErrMergeIncomplete
	}

	op := s.newOperation(ctx, owner, models.FileMergeOperation, uploadID)
	op.FileName, op.Size, op.Algorithm = session.FileName, session.FileSize, strings.ToLower(session.Algorithm)
	op.Checksum = session.Checksum
	op, started, err := repo.BeginOperation(ctx, op)
	if err != nil {
		return Record{}, operationError("begin merge", err)
	}
	if !started {
		return s.replayMerge(ctx, op)
	}
	register := func(ctx context.Context, key string) error {
		if err := repo.RegisterWrite(ctx, op, key); err != nil {
			return operationError("register merge object", err)
		}
		op.StorageKey = key
		return nil
	}
	if session.Assemble {
		if err := register(ctx, session.StorageKey); err != nil {
			return Record{}, err
		}
	}
	merged, err := s.operations.Merge(ctx, uploadID, register)
	if err != nil {
		return Record{}, err
	}
	if op.StorageKey == "" || op.StorageKey != merged.StorageKey || op.Size != merged.FileSize {
		return Record{}, operationError("verify registered merge target", nil)
	}
	op.Checksum = merged.Checksum
	return s.publish(ctx, op)
}

func (s *Service) requireChunkLocation(ctx context.Context, owner int64, uploadID string) error {
	if !validID(uploadID) {
		return gouploader.ErrSessionNotFound
	}
	session, bound, err := s.operations.Session(ctx, uploadID)
	if err != nil {
		return err
	}
	if session.UserID != strconv.FormatInt(owner, 10) {
		return gouploader.ErrUnauthorized
	}
	if !bound {
		return ErrStorageLocationMismatch
	}
	return nil
}

func (s *Service) replayMerge(ctx context.Context, op models.FileOperation) (Record, error) {
	if op.Namespace != s.operations.Namespace {
		return Record{}, ErrStorageLocationMismatch
	}
	switch op.State {
	case models.FileOperationDone:
		return s.Get(ctx, op.OwnerID, op.FileID)
	case models.FileOperationReview:
		return Record{}, ErrDependencyUnavailable
	default:
		return Record{}, gouploader.ErrMergeInProgress
	}
}

func (s *Service) publish(ctx context.Context, op models.FileOperation) (Record, error) {
	f := models.File{
		ID: op.FileID, OwnerID: op.OwnerID, StorageKey: op.StorageKey, FileName: op.FileName,
		Driver: op.Driver, Bucket: op.Bucket, Checksum: op.Checksum, Size: op.Size,
		MIMEType: mimeFromName(op.FileName), CreatedAt: op.CreatedAt, UpdatedAt: op.CreatedAt,
	}
	if err := s.operations.Repository.PublishOperation(ctx, op, f); err != nil {
		return Record{}, operationError("publish file and operation", err)
	}
	return Record{
		ID: f.ID, OwnerID: f.OwnerID, StorageKey: f.StorageKey, FileName: f.FileName,
		Driver: f.Driver, Bucket: f.Bucket, Checksum: f.Checksum, Size: f.Size,
		MIMEType: f.MIMEType, CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt,
	}, nil
}

func (s *Service) deleteRecoverable(ctx context.Context, owner int64, id string) error {
	if err := validOwner(ctx, owner); err != nil {
		return err
	}
	if !validID(id) {
		return ErrNotFound
	}
	repo := s.operations.Repository
	f, found, err := s.repo.Find(ctx, owner, id)
	if err != nil {
		return operationError("find file for deletion", err)
	}
	var op models.FileOperation
	var started bool
	if found {
		if !s.matchesStorageLocation(f) {
			return ErrStorageLocationMismatch
		}
		if err := s.checkPublishedLocation(ctx, owner, id); err != nil {
			return err
		}
		op = s.newOperation(ctx, owner, models.FileDeleteOperation, id)
		op.FileID, op.StorageKey, op.FileName = id, f.StorageKey, f.FileName
		op, started, err = repo.HideForDeletion(ctx, op)
	} else {
		op, err = repo.FindOperation(ctx, owner, models.FileDeleteOperation, id)
	}
	if err != nil {
		return operationError("hide file for deletion", err)
	}
	if op.ID == "" {
		return ErrNotFound
	}
	if op.Namespace != s.operations.Namespace {
		return ErrStorageLocationMismatch
	}
	if op.State == models.FileOperationDone {
		if time.Since(op.UpdatedAt) > operationReplayWindow {
			return ErrNotFound
		}
		return nil
	}
	if !started && op.State == models.FileOperationPending {
		op, started, err = repo.ClaimOperation(ctx, op, time.Now().UTC().Add(45*time.Second))
		if err != nil {
			return operationError("claim deletion", err)
		}
	}
	if !started {
		return ErrDependencyUnavailable
	}
	return s.finishDeletion(ctx, op)
}

func (s *Service) checkPublishedLocation(ctx context.Context, owner int64, id string) error {
	namespace, err := s.operations.Repository.PublicationNamespace(ctx, owner, id)
	if err != nil {
		return operationError("read file publication location", err)
	}
	if namespace != "" && namespace != s.operations.Namespace {
		return ErrStorageLocationMismatch
	}
	return nil
}

func (s *Service) finishDeletion(ctx context.Context, op models.FileOperation) error {
	repo := s.operations.Repository
	if op.Namespace != s.operations.Namespace || op.StorageKey == "" ||
		!s.matchesStorageLocation(Record{Driver: op.Driver, Bucket: op.Bucket}) {
		return s.deferOperation(ctx, op, "invalid_delete_target", nil)
	}
	referenced, err := repo.LiveObjectReference(ctx, op.Driver, op.Bucket, op.StorageKey)
	if err != nil || referenced {
		return s.deferOperation(ctx, op, "reference_check", err)
	}
	if err := s.up.Storage().Delete(ctx, op.StorageKey); err != nil {
		return s.deferOperation(ctx, op, "delete_object", err)
	}
	if s.driver == "s3" {
		exists, err := s.up.Storage().Exists(ctx, op.StorageKey)
		if err != nil || exists {
			return s.deferOperation(ctx, op, "verify_object_absence", err)
		}
	}
	if err := repo.SetOperationState(ctx, op, models.FileOperationDone, "", time.Now().UTC()); err != nil {
		return operationError("complete deletion", err)
	}
	return nil
}

// CheckRecovery verifies the operation table before starting HTTP service.
func (s *Service) CheckRecovery(ctx context.Context) error {
	_, err := s.operations.Repository.RecoveryCandidates(ctx, s.operations.Namespace, 1)
	return err
}

// PruneCompletedAttempts only removes terminal attempt details. Primary records
// retain file provenance and prevent a merge from resurrecting a deleted file.
func (s *Service) PruneCompletedAttempts(ctx context.Context) error {
	return s.operations.Repository.PruneCompletedAttempts(ctx, s.operations.Namespace,
		time.Now().UTC().Add(-operationReplayWindow), 100)
}

// RecoverOperations processes a bounded batch and verifies writes without
// retrying an ambiguous write to its old key.
func (s *Service) RecoverOperations(ctx context.Context) error {
	repo := s.operations.Repository
	ops, err := repo.RecoveryCandidates(ctx, s.operations.Namespace, 8)
	if err != nil {
		return err
	}
	var failures []error
	for _, op := range ops {
		if ctx.Err() != nil {
			break
		}
		itemCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := s.recoverOperation(itemCtx, op)
		cancel()
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *Service) recoverOperation(ctx context.Context, op models.FileOperation) error {
	repo := s.operations.Repository
	op, claimed, err := repo.ClaimOperation(ctx, op, time.Now().UTC().Add(45*time.Second))
	if err != nil || !claimed {
		return err
	}
	if op.Kind == models.FileDeleteOperation {
		return s.finishDeletion(ctx, op)
	}
	if op.StorageKey == "" || op.Algorithm != "sha256" || !validSHA256(op.Checksum) ||
		!s.matchesStorageLocation(Record{Driver: op.Driver, Bucket: op.Bucket}) {
		err := repo.SetOperationState(ctx, op, models.FileOperationReview, "unverified_write", time.Now().UTC())
		return operationError("file operation "+op.ID+" requires review", err)
	}
	exists, err := s.up.Storage().Exists(ctx, op.StorageKey)
	if err != nil || !exists {
		return s.deferOperation(ctx, op, "missing_or_unknown_object", err)
	}
	stream, err := s.up.Storage().Get(ctx, op.StorageKey)
	if err != nil {
		return s.deferOperation(ctx, op, "read_object", err)
	}
	size, checksum, readErr := uploadDigest(ctx, stream, op.Size)
	if err := errors.Join(readErr, stream.Close()); err != nil || size != op.Size || !strings.EqualFold(checksum, op.Checksum) {
		return s.deferOperation(ctx, op, "unverified_content", err)
	}
	_, err = s.publish(ctx, op)
	return err
}

func (s *Service) deferOperation(ctx context.Context, op models.FileOperation, reason string, cause error) error {
	now := time.Now().UTC()
	state, next := models.FileOperationPending, now.Add((time.Second<<min(op.Generation, 7))+time.Duration(op.CreatedAt.Nanosecond()%1_000_000_000))
	if now.Sub(op.CreatedAt) >= 24*time.Hour {
		state, next = models.FileOperationReview, now
	}
	err := s.operations.Repository.SetOperationState(ctx, op, state, reason, next)
	return operationError(reason, errors.Join(cause, err))
}

func operationError(step string, err error) error {
	if err == nil {
		return fmt.Errorf("%s: %w", step, ErrDependencyUnavailable)
	}
	if errx.IsPureAbort(err) {
		return fmt.Errorf("%s: %w", step, err)
	}
	return fmt.Errorf("%s: %w: %w", step, ErrDependencyUnavailable, err)
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func uploadDigest(ctx context.Context, reader io.Reader, limit int64) (int64, string, error) {
	h := sha256.New()
	size, err := io.Copy(h, io.LimitReader(contextReader{ctx, reader}, limit+1))
	if err != nil {
		return 0, "", err
	}
	if size > limit {
		return 0, "", gouploader.ErrFileTooLarge
	}
	return size, hex.EncodeToString(h.Sum(nil)), nil
}

func validSHA256(checksum string) bool {
	if len(checksum) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(checksum)
	return err == nil
}
