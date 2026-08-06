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
//
// ctx 必须绑定到 db 上：GORM 用 Statement.Context 执行 BeginTx，不绑定即为
// background，于是**事务的建立、提交与回滚都不受调用方超时约束**——而事务内的
// 查询经 DB(ctx, …) 绑定了 ctx，会按时超时。这种半绑定的故障形态最难查：
// 连接池耗尽时请求侧超时已经返回，goroutine 仍挂在池上等连接并持续堆积，
// 而观测上看到的是"超时正常生效"，与真实原因相反。
//
// 已知遗留：本方法不感知 ctx 中已有事务，嵌套调用会开出第二个独立事务。
// 当前无嵌套调用点；要支持嵌套需引入"是否已在事务中"的语义判定，属独立决策。
func (m Manager) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(With(ctx, tx))
	})
}
