// Package userdeactivation 用户注销记录仓储。
package userdeactivation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/repository/dbtx"

	"gorm.io/gorm"
)

type Repository struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Create 写入一条注销记录。
//
// 事务由上层通过 dbtx.Manager.Do 开启并经 ctx 传递，本方法走 mdbCtx 自动参与同一事务，
// 与"改写 users 字段"原子提交。
func (r *Repository) Create(ctx context.Context, record *models.UserDeactivation) error {
	if err := r.mdbCtx(ctx).Create(record).Error; err != nil {
		// 带上操作与主键：注销是多步事务，裸错误无法指出是哪一步失败的。
		return fmt.Errorf("create deactivation record for user %d: %w", record.UserID, err)
	}
	return nil
}

// GetLatestByUserID 返回指定用户最新一条注销记录。
// 未找到返回 (nil, nil)，便于上层做幂等判断。
func (r *Repository) GetLatestByUserID(ctx context.Context, userID int64) (*models.UserDeactivation, error) {
	var record models.UserDeactivation
	err := r.mdbCtx(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC").
		First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// mdbCtx 数据访问收口：经 dbtx.DB 在事务内自动用事务句柄，否则用底座 db。
func (r *Repository) mdbCtx(ctx context.Context) *gorm.DB {
	return dbtx.DB(ctx, r.db)
}

// DeleteOlderThan 删除创建时间早于 cutoff 的注销档案，单次最多 limit 行。
//
// **分批而不是一次删完**：注销档案在长期运行的系统里可能积累到百万级，
// 一条无界 DELETE 会长时间持有锁并撑大 binlog，而这个任务本身并不紧急。
// 返回实际删除行数，调用方据此判断是否还有剩余。
//
// 用 Unscoped：这张表存的是**待清除的个人信息**，软删只会让它继续留在磁盘上，
// 而清理的全部意义就是让它不再存在。
func (r *Repository) DeleteOlderThan(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("delete deactivation records: batch limit must be positive, got %d", limit)
	}

	result := r.mdbCtx(ctx).
		Unscoped().
		Where("created_at < ?", cutoff).
		Limit(limit).
		Delete(&models.UserDeactivation{})
	if result.Error != nil {
		return 0, fmt.Errorf("delete deactivation records older than %s: %w",
			cutoff.Format(time.RFC3339), result.Error)
	}
	return result.RowsAffected, nil
}
