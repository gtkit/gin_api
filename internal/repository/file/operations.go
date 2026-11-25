package file

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
	"uuid"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/repository/dbtx"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrOperationChanged = errors.New("file operation state changed")

func (r *Repository) FindOperation(ctx context.Context, owner int64, kind, key string) (models.FileOperation, error) {
	var op models.FileOperation
	err := r.mdbCtx(ctx).Where("owner_id = ? AND kind = ? AND business_key = ?", owner, kind, key).Take(&op).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = nil
	}
	return op, err
}

func (r *Repository) BeginOperation(ctx context.Context, op models.FileOperation) (models.FileOperation, bool, error) {
	if err := r.mdbCtx(ctx).Create(&op).Error; err != nil {
		if !isDuplicateKey(err) {
			return models.FileOperation{}, false, err
		}
		existing, err := r.FindOperation(ctx, op.OwnerID, op.Kind, op.BusinessKey)
		return existing, false, err
	}
	return op, true, nil
}

// HideForDeletion locks the file row, records the exact target, and hides it in
// one transaction. Repeated requests recover the owner-scoped intent.
func (r *Repository) HideForDeletion(ctx context.Context, op models.FileOperation) (models.FileOperation, bool, error) {
	var result models.FileOperation
	var started bool
	err := dbtx.NewManager(r.db).Do(ctx, func(ctx context.Context) error {
		var f models.File
		err := r.mdbCtx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("owner_id = ? AND id = ?", op.OwnerID, op.FileID).Take(&f).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			result, err = r.FindOperation(ctx, op.OwnerID, op.Kind, op.BusinessKey)
			return err
		}
		if err != nil {
			return err
		}
		if f.Driver != op.Driver || f.Bucket != op.Bucket || f.StorageKey != op.StorageKey {
			return ErrOperationChanged
		}
		source, err := r.PublicationNamespace(ctx, op.OwnerID, op.FileID)
		if err != nil {
			return err
		}
		if source != "" && source != op.Namespace {
			return ErrOperationChanged
		}
		result, started, err = r.BeginOperation(ctx, op)
		if err != nil || !started {
			return err
		}
		deleted, err := r.Delete(ctx, op.OwnerID, op.FileID)
		if err == nil && !deleted {
			return ErrOperationChanged
		}
		return err
	})
	return result, started, err
}

func (r *Repository) PublicationNamespace(ctx context.Context, owner int64, fileID string) (string, error) {
	var published struct{ Namespace string }
	err := r.mdbCtx(ctx).Model(&models.FileOperation{}).Select("namespace").
		Where("owner_id = ? AND file_id = ? AND kind IN ? AND state = ?", owner, fileID,
			[]string{models.FileUploadOperation, models.FileMergeOperation}, models.FileOperationDone).
		Take(&published).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = nil
	}
	return published.Namespace, err
}

func activeOperation(op models.FileOperation) clause.Expr {
	return clause.Expr{
		SQL:  "id = ? AND owner_id = ? AND namespace = ? AND state = ? AND generation = ?",
		Vars: []any{op.ID, op.OwnerID, op.Namespace, models.FileOperationWorking, op.Generation},
	}
}

// RegisterWrite persists the actual object key and its attempt before allowing
// external I/O. The parent update and child insert either both happen or neither does.
func (r *Repository) RegisterWrite(ctx context.Context, op models.FileOperation, key string) error {
	return dbtx.NewManager(r.db).Do(ctx, func(ctx context.Context) error {
		update := r.mdbCtx(ctx).Model(&models.FileOperation{}).Where(activeOperation(op)).
			Where("storage_key = '' AND lease_until > UTC_TIMESTAMP(6)").
			Update("storage_key", key)
		if err := requireOperationUpdate(update); err != nil {
			return err
		}
		parent := op.ID
		op.ID, op.ParentID = uuid.NewV7().String(), &parent
		op.Kind = models.FileAttemptOperation
		op.BusinessKey = parent + ":" + strconv.FormatUint(op.Generation, 10)
		op.StorageKey = key
		// Only the parent is scheduled. The child is an immutable write record.
		op.State = models.FileOperationDone
		return r.mdbCtx(ctx).Create(&op).Error
	})
}

func (r *Repository) PublishOperation(ctx context.Context, op models.FileOperation, file models.File) error {
	return dbtx.NewManager(r.db).Do(ctx, func(ctx context.Context) error {
		result := r.mdbCtx(ctx).Model(&models.FileOperation{}).Where(activeOperation(op)).Where("storage_key = ?", op.StorageKey).
			Updates(map[string]any{"state": models.FileOperationDone, "lease_until": nil, "checksum": file.Checksum, "failure": ""})
		if err := requireOperationUpdate(result); err != nil {
			return err
		}
		// A published row cannot be overwritten or resurrected.
		return r.mdbCtx(ctx).Create(&file).Error
	})
}

func (r *Repository) SetOperationState(ctx context.Context, op models.FileOperation, state, failure string, next time.Time) error {
	result := r.mdbCtx(ctx).Model(&models.FileOperation{}).Where(activeOperation(op)).Updates(map[string]any{
		"state": state, "failure": failure, "next_at": next, "lease_until": nil,
	})
	return requireOperationUpdate(result)
}

func (r *Repository) RecoveryCandidates(ctx context.Context, namespace string, limit int) ([]models.FileOperation, error) {
	var ops []models.FileOperation
	err := r.mdbCtx(ctx).Where("namespace = ? AND state IN ? AND next_at <= UTC_TIMESTAMP(6)",
		namespace, []string{models.FileOperationPending, models.FileOperationWorking}).
		Order("next_at, id").Limit(limit).Find(&ops).Error
	return ops, err
}

func (r *Repository) ClaimOperation(ctx context.Context, op models.FileOperation, leaseUntil time.Time) (models.FileOperation, bool, error) {
	result := r.mdbCtx(ctx).Model(&models.FileOperation{}).
		Where("id = ? AND owner_id = ? AND namespace = ? AND generation = ? AND state = ? AND next_at <= UTC_TIMESTAMP(6)",
			op.ID, op.OwnerID, op.Namespace, op.Generation, op.State).
		Updates(map[string]any{"state": models.FileOperationWorking, "generation": gorm.Expr("generation + 1"),
			"lease_until": leaseUntil, "next_at": leaseUntil})
	op.Generation++
	op.State, op.LeaseUntil, op.NextAt = models.FileOperationWorking, &leaseUntil, leaseUntil
	return op, result.RowsAffected == 1, result.Error
}

func (r *Repository) LiveObjectReference(ctx context.Context, driver, bucket, key string) (bool, error) {
	var row struct{ ID string }
	err := r.mdbCtx(ctx).Model(&models.File{}).Select("id").
		Where("driver = ? AND bucket = ? AND storage_key = ?", driver, bucket, key).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (r *Repository) FindByObjectIncludingDeleted(ctx context.Context, owner int64, key string) (models.File, error) {
	var f models.File
	err := r.mdbCtx(ctx).Unscoped().Where("owner_id = ? AND storage_key = ?", owner, key).Take(&f).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = nil
	}
	return f, err
}

func (r *Repository) PruneCompletedAttempts(ctx context.Context, namespace string, cutoff time.Time, limit int) error {
	var ids []string
	err := r.mdbCtx(ctx).Model(&models.FileOperation{}).
		Joins("JOIN file_operations AS parent ON parent.id = file_operations.parent_id").
		Where("file_operations.namespace = ? AND file_operations.state = ? AND file_operations.next_at <= ? AND parent.state = ? AND parent.updated_at <= ?",
			namespace, models.FileOperationDone, cutoff, models.FileOperationDone, cutoff).
		Order("file_operations.next_at, file_operations.id").Limit(limit).Pluck("file_operations.id", &ids).Error
	if err != nil || len(ids) == 0 {
		return err
	}
	return r.mdbCtx(ctx).Where("id IN ? AND namespace = ? AND kind = ?", ids, namespace, models.FileAttemptOperation).
		Delete(&models.FileOperation{}).Error
}

func requireOperationUpdate(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("expected to update one file operation: %w", ErrOperationChanged)
	}
	return nil
}
