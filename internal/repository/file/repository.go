// Package file 封装 files 表的数据访问：上传对象的归属记录。
package file

import (
	"context"
	"errors"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/repository/dbtx"
	"gorm.io/gorm"
)

// maxListLimit 是列表查询的硬上限（db-patterns guide：LIMIT 最大 100）。
const maxListLimit = 100

type Repository struct{ db *gorm.DB }

func New(db *gorm.DB) *Repository                         { return &Repository{db: db} }
func (r *Repository) mdbCtx(ctx context.Context) *gorm.DB { return dbtx.DB(ctx, r.db) }

func (r *Repository) Find(ctx context.Context, owner int64, id string) (models.File, bool, error) {
	var record models.File
	err := r.mdbCtx(ctx).Where("owner_id = ? AND id = ?", owner, id).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return record, false, nil
	}
	return record, err == nil, err
}

func (r *Repository) FindByStorageKey(ctx context.Context, owner int64, key string) (models.File, bool, error) {
	var record models.File
	err := r.mdbCtx(ctx).Where("owner_id = ? AND storage_key = ?", owner, key).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return record, false, nil
	}
	return record, err == nil, err
}

func (r *Repository) List(ctx context.Context, owner int64, offset, limit int) ([]models.File, int64, error) {
	limit = min(max(limit, 1), maxListLimit)
	var total int64
	if err := r.mdbCtx(ctx).Model(&models.File{}).Where("owner_id = ?", owner).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var records []models.File
	err := r.mdbCtx(ctx).Where("owner_id = ?", owner).
		Order("created_at DESC").Order("id DESC").Offset(offset).Limit(limit).Find(&records).Error
	return records, total, err
}

// Create 写入记录；同一 (owner_id, storage_key) 的重复写入按幂等成功处理，
// 兜底分片合并的并发/重试场景（唯一键 uk_files_owner_storage）。
func (r *Repository) Create(ctx context.Context, record models.File) error {
	err := r.mdbCtx(ctx).Create(&record).Error
	if err == nil {
		return nil
	}
	if _, found, findErr := r.FindByStorageKey(ctx, record.OwnerID, record.StorageKey); findErr == nil && found {
		return nil
	}
	return err
}

func (r *Repository) Delete(ctx context.Context, owner int64, id string) (bool, error) {
	result := r.mdbCtx(ctx).Where("owner_id = ? AND id = ?", owner, id).Delete(&models.File{})
	return result.RowsAffected > 0, result.Error
}
