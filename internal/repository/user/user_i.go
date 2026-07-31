// Package user 模型
package user

import (
	"context"

	"github.com/gtkit/gin-api/internal/repository/dbtx"

	"gorm.io/gorm"
)

type Repository struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Repository {
	return &Repository{
		db: db,
	}
}

// mdbCtx 是本仓储所有数据访问的唯一收口：经 dbtx.DB 在事务内自动用事务句柄，否则用底座 db。
func (r *Repository) mdbCtx(ctx context.Context) *gorm.DB {
	return dbtx.DB(ctx, r.db)
}
