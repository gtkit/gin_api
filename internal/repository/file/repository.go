// Package file 封装 files 表的数据访问：上传对象的归属记录。
package file

import (
	"context"
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
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

func (r *Repository) findByStorageKey(ctx context.Context, owner int64, key string) (models.File, bool, error) {
	var record models.File
	err := r.mdbCtx(ctx).Where("owner_id = ? AND storage_key = ?", owner, key).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return record, false, nil
	}
	return record, err == nil, err
}

func (r *Repository) List(ctx context.Context, owner int64, offset, limit int) ([]models.File, int64, error) {
	limit = min(max(limit, 1), maxListLimit)
	query := r.mdbCtx(ctx).Select("id", "owner_id", "storage_key", "file_name", "size", "driver", "bucket", "checksum", "mime_type", "created_at", "updated_at")
	if offset >= maxListLimit {
		// Use the covering index for discarded rows. The join fetches the payload
		// for the selected IDs in the same statement snapshot.
		page := r.mdbCtx(ctx).Model(&models.File{}).
			Select("id AS page_id", "created_at AS page_created_at").Where("owner_id = ?", owner).
			Order("created_at DESC").Order("id DESC").Offset(offset).Limit(limit)
		query = query.Joins("JOIN (?) AS file_page ON file_page.page_id = files.id", page).
			Order("file_page.page_created_at DESC").Order("file_page.page_id DESC")
	} else {
		query = query.Where("owner_id = ?", owner).
			Order("created_at DESC").Order("id DESC").Offset(offset).Limit(limit)
	}
	var records []models.File
	if err := query.Find(&records).Error; err != nil {
		return nil, 0, err
	}
	if len(records) < limit && (len(records) > 0 || offset == 0) {
		return records, int64(offset + len(records)), nil
	}
	var total int64
	if err := r.mdbCtx(ctx).Model(&models.File{}).Where("owner_id = ?", owner).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	return records, total, nil
}

// ListAfter reads one extra row to determine if another owner-scoped page exists.
func (r *Repository) ListAfter(ctx context.Context, owner int64, at time.Time, id string, size int) ([]models.File, error) {
	query := r.mdbCtx(ctx).Where("owner_id = ?", owner)
	if id != "" {
		if at.IsZero() {
			query = query.Where("created_at IS NULL AND id < ?", id)
		} else {
			query = query.Where("(created_at < ? OR (created_at = ? AND id < ?) OR created_at IS NULL)", at, at, id)
		}
	}
	var records []models.File
	err := query.Order("created_at DESC").Order("id DESC").Limit(min(max(size, 1), maxListLimit) + 1).Find(&records).Error
	return records, err
}

// Create 写入记录；同一 (owner_id, storage_key) 的重复写入按幂等成功处理，
// 兜底分片合并的并发/重试场景（唯一键 uk_files_owner_storage）。
func (r *Repository) Create(ctx context.Context, record models.File) (models.File, error) {
	err := r.mdbCtx(ctx).Create(&record).Error
	if err == nil {
		return record, nil
	}
	if !isDuplicateKey(err) {
		return models.File{}, err
	}
	stored, found, findErr := r.findByStorageKey(ctx, record.OwnerID, record.StorageKey)
	if findErr != nil {
		return models.File{}, findErr
	}
	if found {
		return stored, nil
	}
	return models.File{}, err
}

const mysqlDuplicateEntry = 1062

func isDuplicateKey(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	mysqlErr, ok := errors.AsType[*mysql.MySQLError](err)
	return ok && mysqlErr.Number == mysqlDuplicateEntry
}

func (r *Repository) Delete(ctx context.Context, owner int64, id string) (bool, error) {
	result := r.mdbCtx(ctx).Where("owner_id = ? AND id = ?", owner, id).Delete(&models.File{})
	return result.RowsAffected > 0, result.Error
}
