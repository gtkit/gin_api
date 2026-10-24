// Package dbtx 提供基于 context 的事务传递与 gorm 事务管理器。
//
// 约定（务必遵守，否则事务会静默失效）：repository 的所有数据访问必须经各自的
// mdbCtx(ctx) 收口，由 mdbCtx 通过 From(ctx) 感知事务句柄——这是 tx 注入的唯一入口。
package dbtx

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/gtkit/goerr"
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
// 同归属嵌套调用使用 savepoint。未知归属或并行嵌套被拒绝；失败的 savepoint
// 回滚会把外层标为不可提交。物理事务的提交与回滚错误均保留，不自动重试。
func (m Manager) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	if m.db == nil || fn == nil {
		return errors.New("dbtx: database and callback are required")
	}
	if err := ctx.Err(); err != nil {
		return goerr.Wrap(err, "begin transaction")
	}
	if handle, exists := From(ctx); exists {
		scope, ok := ctx.Value(scopeKey{}).(*transactionScope)
		if !ok || scope == nil || scope.handle != handle || scope.state.owner != m.db {
			return ErrForeignTransaction
		}
		return m.nested(ctx, scope, fn)
	}
	return m.outer(ctx, fn)
}

// ErrForeignTransaction rejects implicit cross-manager or external transactions.
var ErrForeignTransaction = errors.New("dbtx: transaction belongs to an unknown or different manager")

// ErrConcurrentTransaction rejects parallel use of a sequential savepoint scope.
var ErrConcurrentTransaction = errors.New("dbtx: transaction scope is already in use or closed")

type scopeKey struct{}

type transactionState struct {
	mu       sync.Mutex
	owner    *gorm.DB
	sequence uint64
	closed   bool
	fatal    error
}

type transactionScope struct {
	state  *transactionState
	handle *gorm.DB
	busy   bool // protected by state.mu; never a lock held across callback/I/O
	closed bool
}

// panicCleanupError preserves the original panic identity and failed cleanup together.
// A non-error panic value stays private instead of becoming a client message.
type panicCleanupError struct {
	value any
	err   error
}

func (p *panicCleanupError) Error() string { return "dbtx: panic cleanup failed: " + p.err.Error() }
func (p *panicCleanupError) Unwrap() []error {
	if cause, ok := p.value.(error); ok {
		return []error{cause, p.err}
	}
	return []error{p.err}
}

func (m Manager) outer(ctx context.Context, fn func(context.Context) error) (err error) {
	tx := m.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return goerr.Wrap(tx.Error, "begin transaction")
	}
	state := &transactionState{owner: m.db}
	scope := &transactionScope{state: state, handle: tx}
	returned := false
	defer func() {
		panicked := recover()
		state.mu.Lock()
		state.closed = true
		fatal := state.fatal
		if scope.busy {
			fatal = errors.Join(fatal, ErrConcurrentTransaction)
		}
		state.mu.Unlock()
		err = errors.Join(err, fatal)
		if returned && panicked == nil && err == nil {
			err = goerr.Wrap(tx.Commit().Error, "commit transaction")
			if err == nil {
				return
			}
		}
		rollbackErr := tx.Rollback().Error
		// database/sql has already closed a transaction after Commit or cancellation.
		// That terminal state needs no second rollback; it does not erase Commit's error.
		if errors.Is(rollbackErr, sql.ErrTxDone) {
			rollbackErr = nil
		}
		err = errors.Join(err, goerr.Wrap(rollbackErr, "rollback transaction"))
		if panicked != nil {
			if cleanupErr := errors.Join(fatal, rollbackErr); cleanupErr != nil {
				panic(&panicCleanupError{value: panicked, err: cleanupErr})
			}
			panic(panicked)
		}
		if !returned && rollbackErr != nil {
			// A callback using runtime.Goexit cannot receive a return error.
			panic(&panicCleanupError{err: rollbackErr})
		}
	}()
	owned := context.WithValue(With(ctx, tx), scopeKey{}, scope)
	err = fn(owned)
	if err == nil {
		err = ctx.Err()
	}
	returned = true
	return err
}

func (m Manager) nested(ctx context.Context, parent *transactionScope, fn func(context.Context) error) (err error) {
	state := parent.state
	state.mu.Lock()
	if state.closed || parent.closed || parent.busy || state.fatal != nil {
		state.mu.Unlock()
		return ErrConcurrentTransaction
	}
	if parent.handle.DisableNestedTransaction {
		state.mu.Unlock()
		return errors.New("dbtx: nested transactions are disabled")
	}
	parent.busy = true
	state.sequence++
	name := "gin_api_sp_" + strconv.FormatUint(state.sequence, 10)
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		parent.busy = false
		state.mu.Unlock()
	}()

	tx := parent.handle.WithContext(ctx)
	if saveErr := tx.SavePoint(name).Error; saveErr != nil {
		err = goerr.Wrap(saveErr, "create transaction savepoint")
		state.fail(err)
		return err
	}
	scope := &transactionScope{state: state, handle: tx}
	returned := false
	defer func() {
		panicked := recover()
		state.mu.Lock()
		active := scope.busy
		closed := state.closed
		scope.closed = true
		state.mu.Unlock()
		if closed {
			err = errors.Join(err, ErrConcurrentTransaction)
			if panicked != nil {
				panic(panicked)
			}
			return
		}
		if active {
			err = errors.Join(err, ErrConcurrentTransaction)
			state.fail(ErrConcurrentTransaction)
		}
		if returned && panicked == nil && err == nil {
			return
		}
		rollbackErr := goerr.Wrap(tx.RollbackTo(name).Error, "rollback transaction savepoint")
		if rollbackErr != nil {
			state.fail(errors.Join(err, rollbackErr))
		}
		err = errors.Join(err, rollbackErr)
		if panicked != nil {
			if rollbackErr != nil {
				panic(&panicCleanupError{value: panicked, err: rollbackErr})
			}
			panic(panicked)
		}
		if !returned && rollbackErr != nil {
			panic(&panicCleanupError{err: rollbackErr})
		}
	}()
	owned := context.WithValue(With(ctx, tx), scopeKey{}, scope)
	err = fn(owned)
	if err == nil {
		err = ctx.Err()
	}
	returned = true
	return err
}

func (s *transactionState) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fatal = errors.Join(s.fatal, fmt.Errorf("transaction cannot commit: %w", err))
}
