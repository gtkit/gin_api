package application

import (
	"context"
	"errors"
	"testing"

	"github.com/gtkit/gin-api/internal/models"
)

// mustService 构造 Service，装配失败直接失败测试。
func mustService(t *testing.T, deps Deps, opts ...ServiceOption) *Service {
	t.Helper()

	svc, err := NewService(deps, opts...)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

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

func TestDeactivateRejectsZeroUserID(t *testing.T) {
	t.Parallel()

	tx := &fakeTxManager{}
	service := mustService(t, Deps{Repo: &fakeUserRepo{}, Recorder: &fakeDeactRecorder{}, Tx: tx})

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
	service := mustService(t, Deps{Repo: repo, Recorder: &fakeDeactRecorder{}, Tx: &fakeTxManager{}})

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
	service := mustService(t, Deps{Repo: repo, Recorder: &fakeDeactRecorder{}, Tx: &fakeTxManager{}})

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
	service := mustService(t, Deps{Repo: repo, Recorder: rec, Tx: tx}, WithAuditLogger(audit))

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

// TestDeactivateAuditLayering 钉住审计的两层边界：
//
//  1. 业务事实记录（user_deactivations）在事务内写，事务失败即随之回滚——
//     不能留下一条"已注销"记录却没真正注销；
//  2. 操作日志在事务**之外**打印，因此事务失败时一条都不写——
//     不能把"尝试过注销"记成"已注销"。
//
// 没有这条断言，把 auditf 挪回事务内不会被任何测试发现，
// 而那正是"日志后端卡住 → 数据库事务与行锁一起被拖住"的形态。
func TestDeactivateAuditLayering(t *testing.T) {
	t.Parallel()

	recErr := errors.New("recorder down")
	repo := &fakeUserRepo{
		lockFound: true,
		lockUser:  &models.User{Phone: "13800138000", Status: models.UserStatusNormal},
	}
	rec := &fakeDeactRecorder{err: recErr}
	audit := &fakeAuditLogger{}
	service := mustService(t, Deps{Repo: repo, Recorder: rec, Tx: &fakeTxManager{}}, WithAuditLogger(audit))

	err := service.Deactivate(context.Background(), DeactivateInput{UserID: 42, Channel: "app"})
	if !errors.Is(err, recErr) {
		t.Fatalf("err = %v, want wrapped %v", err, recErr)
	}
	if audit.calls != 0 {
		t.Fatalf("audit calls = %d, want 0（事务失败时不得写「已注销」操作日志）", audit.calls)
	}
	if rec.created != nil {
		t.Fatal("recorder 失败时不应留下业务事实记录")
	}
}

// TestDeactivateSurvivesMissingAuditLogger 证明未注入 AuditLogger 时注销照常成功：
// 审计日志是 best-effort，缺失不构成业务失败，更不该 panic。
func TestDeactivateSurvivesMissingAuditLogger(t *testing.T) {
	t.Parallel()

	repo := &fakeUserRepo{
		lockFound: true,
		lockUser:  &models.User{Phone: "13800138000", Status: models.UserStatusNormal},
	}
	service := mustService(t, Deps{Repo: repo, Recorder: &fakeDeactRecorder{}, Tx: &fakeTxManager{}})

	if err := service.Deactivate(context.Background(), DeactivateInput{UserID: 42, Channel: "app"}); err != nil {
		t.Fatalf("Deactivate without audit logger: %v", err)
	}
	if repo.anonCalls != 1 || repo.softDelCalls != 1 {
		t.Fatalf("anonCalls=%d softDelCalls=%d, want 1/1", repo.anonCalls, repo.softDelCalls)
	}
}

type fakeAuditLogger struct{ calls int }

func (f *fakeAuditLogger) Auditf(string, ...any) { f.calls++ }
