// Package dbtx 提供基于 context 的事务传递与 gorm 事务管理器。
//
// 约定（务必遵守，否则事务会静默失效）：repository 的所有数据访问必须经各自的
// mdbCtx(ctx) 收口，由 mdbCtx 通过 From(ctx) 感知事务句柄——这是 tx 注入的唯一入口。
package dbtx

import (
	"context"

	"gorm.io/gorm"
)

type ctxKey struct{}

// With 返回携带事务句柄的 ctx。
func With(ctx context.Context, tx *gorm.DB) context.Context {
	return context.WithValue(ctx, ctxKey{}, tx)
}

// From 取出 ctx 中的事务句柄；不在事务内时 ok=false。
func From(ctx context.Context) (*gorm.DB, bool) {
	tx, ok := ctx.Value(ctxKey{}).(*gorm.DB)
	return tx, ok
}

// DB 是各仓储 mdbCtx 的统一实现：ctx 内携带事务时返回事务句柄，否则返回底座 db。
// 约定：所有 repo 的 mdbCtx 都应 `return dbtx.DB(ctx, r.db)`，从而任意 repo 默认事务安全。
func DB(ctx context.Context, fallback *gorm.DB) *gorm.DB {
	if tx, ok := From(ctx); ok {
		return tx.WithContext(ctx)
	}
	return fallback.WithContext(ctx)
}

// Manager 用 gorm 事务实现 application 侧的 TxManager 接口（结构化满足，不反向依赖 application）。
type Manager struct {
	db *gorm.DB
}

// NewManager 创建事务管理器。
func NewManager(db *gorm.DB) Manager {
	return Manager{db: db}
}

// Do 在单个 gorm 事务内执行 fn，并把 tx 注入 ctx，供 repo 经 mdbCtx 自动参与同一事务。
func (m Manager) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	return m.db.Transaction(func(tx *gorm.DB) error {
		return fn(With(ctx, tx))
	})
}
