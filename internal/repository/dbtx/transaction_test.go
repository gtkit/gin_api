package dbtx

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func mockManager(t *testing.T) (Manager, sqlmock.Sqlmock) {
	t.Helper()
	conn, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		mock.ExpectClose()
		if err := conn.Close(); err != nil {
			t.Errorf("close mock database: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: conn, SkipInitializeWithVersion: true}),
		&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return NewManager(db), mock
}

func TestManagerRetainsRollbackFailure(t *testing.T) {
	t.Parallel()
	m, mock := mockManager(t)
	work, cleanup := errors.New("work failed"), errors.New("rollback failed")
	mock.ExpectBegin()
	mock.ExpectRollback().WillReturnError(cleanup)
	err := m.Do(t.Context(), func(context.Context) error { return work })
	if !errors.Is(err, work) || !errors.Is(err, cleanup) {
		t.Fatalf("primary and rollback errors must both survive: %v", err)
	}
}

func TestManagerNestedUsesOuterConnection(t *testing.T) {
	t.Parallel()
	m, mock := mockManager(t)
	work := errors.New("outer failed")
	mock.ExpectBegin()
	mock.ExpectExec("SAVEPOINT gin_api_sp_1").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	err := m.Do(t.Context(), func(ctx context.Context) error {
		if err := m.Do(ctx, func(inner context.Context) error {
			if DB(inner, m.db).Statement.ConnPool != DB(ctx, m.db).Statement.ConnPool {
				t.Error("nested work escaped the outer physical transaction")
			}
			return nil
		}); err != nil {
			return err
		}
		return work
	})
	if !errors.Is(err, work) {
		t.Fatalf("outer failure lost: %v", err)
	}
}

func TestManagerNestedRollbackFailurePreventsOuterCommit(t *testing.T) {
	t.Parallel()
	m, mock := mockManager(t)
	work, cleanup := errors.New("inner failed"), errors.New("savepoint rollback failed")
	mock.ExpectBegin()
	mock.ExpectExec("SAVEPOINT gin_api_sp_1").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("ROLLBACK TO SAVEPOINT gin_api_sp_1").WillReturnError(cleanup)
	mock.ExpectRollback()
	err := m.Do(t.Context(), func(ctx context.Context) error {
		innerErr := m.Do(ctx, func(context.Context) error { return work })
		if !errors.Is(innerErr, work) || !errors.Is(innerErr, cleanup) {
			t.Errorf("inner causes lost: %v", innerErr)
		}
		// Deliberately handle the inner error. An uncertain rollback still forbids commit.
		return nil
	})
	if !errors.Is(err, work) || !errors.Is(err, cleanup) {
		t.Fatalf("outer transaction committed despite uncertain rollback: %v", err)
	}
}

func TestManagerNestedSuccessAndHandledFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "handled failure"}[failed], func(t *testing.T) {
			m, mock := mockManager(t)
			work := errors.New("inner failed")
			mock.ExpectBegin()
			mock.ExpectExec("SAVEPOINT gin_api_sp_1").WillReturnResult(sqlmock.NewResult(0, 0))
			if failed {
				mock.ExpectExec("ROLLBACK TO SAVEPOINT gin_api_sp_1").WillReturnResult(sqlmock.NewResult(0, 0))
			}
			mock.ExpectCommit()
			err := m.Do(t.Context(), func(ctx context.Context) error {
				inner := m.Do(ctx, func(context.Context) error {
					if failed {
						return work
					}
					return nil
				})
				if failed && !errors.Is(inner, work) || !failed && inner != nil {
					t.Errorf("unexpected inner result: %v", inner)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManagerPanicPreservesCleanupFailures(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback succeeds", true: "rollback fails"}[failed], func(t *testing.T) {
			m, mock := mockManager(t)
			work, cleanup := errors.New("callback panic"), errors.New("rollback failed")
			mock.ExpectBegin()
			if failed {
				mock.ExpectRollback().WillReturnError(cleanup)
			} else {
				mock.ExpectRollback()
			}
			defer func() {
				recovered := recover()
				err, ok := recovered.(error)
				if !ok || !errors.Is(err, work) || failed && !errors.Is(err, cleanup) {
					t.Errorf("panic or cleanup cause lost: %v", recovered)
				}
			}()
			if err := m.Do(t.Context(), func(context.Context) error { panic(work) }); err != nil {
				t.Errorf("callback panic returned instead of propagating: %v", err)
			}
		})
	}
}

func TestManagerRejectsUnknownAndForeignTransactions(t *testing.T) {
	m, mock := mockManager(t)
	called := false
	err := m.Do(With(t.Context(), m.db), func(context.Context) error { called = true; return nil })
	if called || !errors.Is(err, ErrForeignTransaction) {
		t.Fatalf("external transaction was silently accepted: %v", err)
	}
	other, _ := mockManager(t)
	mock.ExpectBegin()
	mock.ExpectCommit()
	err = m.Do(t.Context(), func(ctx context.Context) error {
		err := other.Do(ctx, func(context.Context) error { called = true; return nil })
		if !errors.Is(err, ErrForeignTransaction) {
			t.Errorf("foreign manager accepted: %v", err)
		}
		return nil
	})
	if called || err != nil {
		t.Fatalf("foreign callback ran or outer failed: %v", err)
	}
}

func TestManagerSavepointFailureForbidsCommit(t *testing.T) {
	m, mock := mockManager(t)
	failure := errors.New("savepoint unavailable")
	mock.ExpectBegin()
	mock.ExpectExec("SAVEPOINT gin_api_sp_1").WillReturnError(failure)
	mock.ExpectRollback()
	called := false
	err := m.Do(t.Context(), func(ctx context.Context) error {
		inner := m.Do(ctx, func(context.Context) error { called = true; return nil })
		if !errors.Is(inner, failure) {
			t.Errorf("savepoint failure lost: %v", inner)
		}
		return nil
	})
	if called || !errors.Is(err, failure) {
		t.Fatalf("uncertain transaction committed: %v", err)
	}
}

func TestManagerReturnsBeginAndCommitFailures(t *testing.T) {
	for _, begin := range []bool{true, false} {
		t.Run(map[bool]string{true: "begin", false: "commit"}[begin], func(t *testing.T) {
			m, mock := mockManager(t)
			failure := errors.New("database failure")
			if begin {
				mock.ExpectBegin().WillReturnError(failure)
			} else {
				mock.ExpectBegin()
				mock.ExpectCommit().WillReturnError(failure)
			}
			called := false
			err := m.Do(t.Context(), func(context.Context) error { called = true; return nil })
			if !errors.Is(err, failure) || called == begin {
				t.Fatalf("called=%v, err=%v", called, err)
			}
		})
	}
}

func TestManagerRejectsParallelAndExpiredNestedScopes(t *testing.T) {
	m, mock := mockManager(t)
	mock.ExpectBegin()
	mock.ExpectExec("SAVEPOINT gin_api_sp_1").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	err := m.Do(t.Context(), func(ctx context.Context) error {
		entered, release := make(chan struct{}), make(chan struct{})
		result := make(chan error, 1)
		expired := ctx
		go func() {
			result <- m.Do(ctx, func(inner context.Context) error {
				expired = inner
				close(entered)
				<-release
				return nil
			})
		}()
		<-entered
		parallel := m.Do(ctx, func(context.Context) error { t.Error("parallel callback ran"); return nil })
		if !errors.Is(parallel, ErrConcurrentTransaction) {
			t.Errorf("parallel call accepted: %v", parallel)
		}
		close(release)
		if err := <-result; err != nil {
			return err
		}
		err := m.Do(expired, func(context.Context) error { t.Error("expired callback ran"); return nil })
		if !errors.Is(err, ErrConcurrentTransaction) {
			t.Errorf("expired scope accepted: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
