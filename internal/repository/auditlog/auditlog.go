// Package auditlog 是写操作审计记录的数据访问层。
package auditlog

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/repository/dbtx"
)

const (
	// writeBatchSize 是单条 INSERT 语句的行数上限（db-patterns：批量默认不超过 500）。
	writeBatchSize = 100
	// deleteBatchCeiling 是保留期清理的单批上限。
	deleteBatchCeiling = 500
)

// Repository 读写 operation_audit_logs 表。
type Repository struct {
	db *gorm.DB
}

// New 创建审计仓储。
func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// mdbCtx 统一经 dbtx.DB 收口，使本 repo 的数据访问自动参与 TxManager 开启的事务。
func (r *Repository) mdbCtx(ctx context.Context) *gorm.DB {
	return dbtx.DB(ctx, r.db)
}

// WriteBatch 批量写入审计记录，实现 internal/pkg/auditlog 的 Sink。
// 调用方（记录器）已在构造点截断字符串字段，本方法不再逐条检查。
func (r *Repository) WriteBatch(ctx context.Context, records []models.OperationAudit) error {
	if len(records) == 0 {
		return nil
	}
	if err := r.mdbCtx(ctx).CreateInBatches(records, writeBatchSize).Error; err != nil {
		return fmt.Errorf("write operation audit batch: %w", err)
	}
	return nil
}

// DeleteOlderThan 按保留期分批删除过期记录，返回删除总数。
// 保留期长短是合规决定，由调用方给出 cutoff；单批受上限约束，
// 避免一条 DELETE 长时间持锁。批间检查 ctx，清理任务可被取消。
func (r *Repository) DeleteOlderThan(ctx context.Context, cutoff time.Time, batchLimit int) (int64, error) {
	if batchLimit <= 0 || batchLimit > deleteBatchCeiling {
		batchLimit = deleteBatchCeiling
	}
	var total int64
	for {
		res := r.mdbCtx(ctx).
			Where("occurred_at < ?", cutoff).
			Limit(batchLimit).
			Delete(&models.OperationAudit{})
		if res.Error != nil {
			return total, fmt.Errorf("delete expired operation audits: %w", res.Error)
		}
		total += res.RowsAffected
		if res.RowsAffected < int64(batchLimit) {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, fmt.Errorf("delete expired operation audits interrupted: %w", err)
		}
	}
}
