package example

import (
	"context"
	"errors"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/repository/dbtx"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repository struct{ db *gorm.DB }

func New(db *gorm.DB) *Repository                         { return &Repository{db: db} }
func (r *Repository) mdbCtx(ctx context.Context) *gorm.DB { return dbtx.DB(ctx, r.db) }

func (r *Repository) Find(ctx context.Context, owner int64, id string) (models.ExampleRecord, bool, error) {
	return r.find(ctx, owner, id, false)
}

// FindForUpdate performs a current read inside the caller's transaction.
func (r *Repository) FindForUpdate(ctx context.Context, owner int64, id string) (models.ExampleRecord, bool, error) {
	return r.find(ctx, owner, id, true)
}

func (r *Repository) find(ctx context.Context, owner int64, id string, lock bool) (models.ExampleRecord, bool, error) {
	var record models.ExampleRecord
	query := r.mdbCtx(ctx).Where("owner_id = ? AND id = ?", owner, id)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return record, false, nil
	}
	return record, err == nil, err
}

func (r *Repository) List(ctx context.Context, owner int64, offset, limit int) ([]models.ExampleRecord, int64, error) {
	var total int64
	if err := r.mdbCtx(ctx).Model(&models.ExampleRecord{}).Where("owner_id = ?", owner).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var records []models.ExampleRecord
	err := r.mdbCtx(ctx).Where("owner_id = ?", owner).Order("created_at DESC").Order("id DESC").Offset(offset).Limit(limit).Find(&records).Error
	return records, total, err
}

func (r *Repository) Create(ctx context.Context, record models.ExampleRecord) error {
	return r.mdbCtx(ctx).Session(&gorm.Session{SkipDefaultTransaction: true}).Create(&record).Error
}
func (r *Repository) Update(ctx context.Context, record models.ExampleRecord) (bool, error) {
	result := r.mdbCtx(ctx).Model(&models.ExampleRecord{}).Where("owner_id = ? AND id = ?", record.OwnerID, record.ID).
		Updates(map[string]any{"title": record.Title, "updated_at": record.UpdatedAt})
	return result.RowsAffected > 0, result.Error
}
func (r *Repository) Delete(ctx context.Context, owner int64, id string) (bool, error) {
	result := r.mdbCtx(ctx).Session(&gorm.Session{SkipDefaultTransaction: true}).
		Where("owner_id = ? AND id = ?", owner, id).Delete(&models.ExampleRecord{})
	return result.RowsAffected > 0, result.Error
}

func (r *Repository) DeleteMany(ctx context.Context, owner int64, ids []string) (int64, error) {
	result := r.mdbCtx(ctx).Where("owner_id = ? AND id IN ?", owner, ids).Delete(&models.ExampleRecord{})
	return result.RowsAffected, result.Error
}
