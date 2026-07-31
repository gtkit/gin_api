package application

import (
	"context"
	"errors"
	"testing"

	"github.com/gtkit/gin-api/internal/models"
)

// fakeTxManager 直接执行 fn（不开真实事务），用于单测注销编排逻辑。
type fakeTxManager struct{ called bool }

func (f *fakeTxManager) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	f.called = true
	return fn(ctx)
}

type fakeDeactRecorder struct {
	created *models.UserDeactivation
	err     error
}

func (f *fakeDeactRecorder) Create(_ context.Context, r *models.UserDeactivation) error {
	if f.err != nil {
		return f.err
	}
	f.created = r
	return nil
}

func TestDeactivateRequiresTx(t *testing.T) {
	t.Parallel()

	// 仅 repo 的 NewWithDeps 不携带 TxManager/recorder，必须立刻短路。
	service := NewWithDeps(&fakeUserRepo{})

	err := service.Deactivate(context.Background(), DeactivateInput{UserID: 42, Channel: "app"})
	if !errors.Is(err, ErrDeactivateUnavailable) {
		t.Fatalf("err = %v, want ErrDeactivateUnavailable", err)
	}
}

func TestDeactivateRejectsZeroUserID(t *testing.T) {
	t.Parallel()

	tx := &fakeTxManager{}
	service := NewServiceWithTx(&fakeUserRepo{}, &fakeDeactRecorder{}, tx)

	err := service.Deactivate(context.Background(), DeactivateInput{UserID: 0, Channel: "app"})
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("err = %v, want ErrUserNotFound", err)
	}
	if tx.called {
		t.Fatal("UserID<=0 应在开启事务前短路，不应进入 tx.Do")
	}
}

func TestDeactivateUserNotFound(t *testing.T) {
	t.Parallel()

	repo := &fakeUserRepo{lockFound: false}
	service := NewServiceWithTx(repo, &fakeDeactRecorder{}, &fakeTxManager{})

	err := service.Deactivate(context.Background(), DeactivateInput{UserID: 42, Channel: "app"})
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("err = %v, want ErrUserNotFound", err)
	}
}

func TestDeactivateAlreadyDeactivated(t *testing.T) {
	t.Parallel()

	repo := &fakeUserRepo{
		lockFound: true,
		lockUser:  &models.User{Status: models.UserStatusDeleted},
	}
	service := NewServiceWithTx(repo, &fakeDeactRecorder{}, &fakeTxManager{})

	err := service.Deactivate(context.Background(), DeactivateInput{UserID: 42, Channel: "app"})
	if !errors.Is(err, ErrAlreadyDeactivated) {
		t.Fatalf("err = %v, want ErrAlreadyDeactivated", err)
	}
	if repo.anonCalls != 0 || repo.softDelCalls != 0 {
		t.Fatal("幂等短路后不应执行脱敏/软删")
	}
}

func TestDeactivateHappyPath(t *testing.T) {
	t.Parallel()

	repo := &fakeUserRepo{
		lockFound: true,
		lockUser: &models.User{
			Phone:    "13800138000",
			Nickname: "老用户",
			Status:   models.UserStatusNormal,
		},
	}
	rec := &fakeDeactRecorder{}
	tx := &fakeTxManager{}
	audit := &fakeAuditLogger{}
	service := NewServiceWithTx(repo, rec, tx, WithAuditLogger(audit))

	err := service.Deactivate(context.Background(), DeactivateInput{UserID: 42, Channel: "app", Reason: "不再使用"})
	if err != nil {
		t.Fatalf("Deactivate returned error: %v", err)
	}
	if !tx.called {
		t.Fatal("应通过 TxManager.Do 编排事务")
	}
	if rec.created == nil || rec.created.OriginalPhone != "13800138000" || rec.created.Channel != "app" {
		t.Fatalf("audit record = %+v, want 保留原手机号与渠道", rec.created)
	}
	if audit.calls != 1 {
		t.Fatalf("audit calls = %d, want 1（注销必须写审计日志）", audit.calls)
	}
	if repo.anonCalls != 1 || repo.softDelCalls != 1 {
		t.Fatalf("anonCalls=%d softDelCalls=%d, want 1/1", repo.anonCalls, repo.softDelCalls)
	}
	if repo.anonPhone == "13800138000" {
		t.Fatal("脱敏后 phone 不应仍为原值")
	}
}

type fakeAuditLogger struct{ calls int }

func (f *fakeAuditLogger) Auditf(string, ...any) { f.calls++ }
