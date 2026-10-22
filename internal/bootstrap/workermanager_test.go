package bootstrap

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func testWorkerBudget() WorkerBudget {
	return WorkerBudget{Prepare: time.Second, Startup: 3 * time.Second, Cleanup: time.Second}
}

// TestWorkerContractValidation 覆盖注册期的全部声明约束。
//
// 零值类别/生命周期必须拒绝：若零值被解释成某个具体取值，忘记声明的 worker
// 会静默落进那一类，而"本该最先停的 producer 被当成 consumer"正是
// 分相关停要消灭的形态。
func TestWorkerContractValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		worker   Worker
		wantErr  bool
		wantHint string
	}{
		{
			name:   "valid producer",
			worker: &funcWorker{name: "cron", kind: KindProducer, lifecycle: LifecycleLongRunning},
		},
		{
			name:   "valid consumer",
			worker: &funcWorker{name: "outbox", kind: KindConsumer, lifecycle: LifecycleLongRunning},
		},
		{
			name:   "valid one-shot bootstrap",
			worker: &funcWorker{name: "repair", kind: KindBootstrap, lifecycle: LifecycleOneShot},
		},
		{
			name:     "zero kind rejected",
			worker:   &funcWorker{name: "x", lifecycle: LifecycleLongRunning},
			wantErr:  true,
			wantHint: "must declare a kind",
		},
		{
			name:     "zero lifecycle rejected",
			worker:   &funcWorker{name: "x", kind: KindConsumer},
			wantErr:  true,
			wantHint: "must declare a lifecycle",
		},
		{
			name:     "empty name rejected",
			worker:   &funcWorker{kind: KindConsumer, lifecycle: LifecycleLongRunning},
			wantErr:  true,
			wantHint: "name must not be empty",
		},
		{
			name:     "one-shot must be bootstrap",
			worker:   &funcWorker{name: "x", kind: KindConsumer, lifecycle: LifecycleOneShot},
			wantErr:  true,
			wantHint: "must be of kind bootstrap",
		},
		{
			name:     "long-running must not be bootstrap",
			worker:   &funcWorker{name: "x", kind: KindBootstrap, lifecycle: LifecycleLongRunning},
			wantErr:  true,
			wantHint: "must be producer or consumer",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateWorkerContract(tc.worker)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("validateWorkerContract() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("validateWorkerContract() error = nil, want error")
			}
			if !errors.Is(err, ErrWorkerContractViolation) {
				t.Fatalf("error %v is not ErrWorkerContractViolation", err)
			}
			if !strings.Contains(err.Error(), tc.wantHint) {
				t.Fatalf("error %q does not mention %q", err, tc.wantHint)
			}
		})
	}
}

// TestNewWorkerManagerRejectsDuplicateNames 验证重名在注册期拒绝。
//
// 重名让日志与错误无法指认到具体 worker，也让降级表互相覆盖——
// 两个同名 worker 里挂掉一个，readiness 会报告哪一个是未定义的。
func TestNewWorkerManagerRejectsDuplicateNames(t *testing.T) {
	t.Parallel()

	_, err := NewWorkerManager(testWorkerBudget(),
		&funcWorker{name: "dup", kind: KindConsumer, lifecycle: LifecycleLongRunning},
		&funcWorker{name: "dup", kind: KindProducer, lifecycle: LifecycleLongRunning},
	)
	if err == nil {
		t.Fatal("NewWorkerManager accepted duplicate worker names")
	}
	if !strings.Contains(err.Error(), "duplicate worker name") {
		t.Fatalf("error %q does not report the duplicate name", err)
	}
}

// TestLongRunningEarlyReturnIsUnexpectedExit 钉住本批次最核心的一条语义：
// 长期 worker 在 ctx 取消之前返回——**包括返回 nil**——一律是异常退出。
//
// 旧实现只在 err != nil 时上报，于是一个静默返回 nil 的长期 worker 会消失得
// 无声无息：进程继续对外服务，而它负责的功能已经不工作了。
func TestLongRunningEarlyReturnIsUnexpectedExit(t *testing.T) {
	t.Parallel()

	mgr, err := NewWorkerManager(testWorkerBudget(), &funcWorker{
		name: "silent", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
		run: func(context.Context) error { return nil }, // 正常返回 nil，但 ctx 未取消
	})
	if err != nil {
		t.Fatalf("NewWorkerManager: %v", err)
	}

	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case got := <-mgr.Errors():
		if !errors.Is(got, ErrUnexpectedExit) {
			t.Fatalf("error = %v, want ErrUnexpectedExit", got)
		}
		if !strings.Contains(got.Error(), "silent") {
			t.Fatalf("error %q does not name the worker", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a long-running worker returned nil early and no error was reported")
	}

	if err := mgr.StopAll(context.Background()); err != nil && !errors.Is(err, ErrUnexpectedExit) {
		t.Fatalf("StopAll after unexpected worker exit: %v", err)
	}
}

// TestOneShotNormalReturnIsNotUnexpectedExit 反向证明：一次性任务正常返回
// 不得被判为异常退出。没有这条，上一条断言可能因为"所有返回都算异常"而恒真。
func TestOneShotNormalReturnIsNotUnexpectedExit(t *testing.T) {
	t.Parallel()

	done := make(chan struct{})
	mgr, err := NewWorkerManager(testWorkerBudget(), &funcWorker{
		name: "repair", kind: KindBootstrap, lifecycle: LifecycleOneShot, required: true,
		run: func(context.Context) error { close(done); return nil },
	})
	if err != nil {
		t.Fatalf("NewWorkerManager: %v", err)
	}

	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-done

	select {
	case got := <-mgr.Errors():
		t.Fatalf("one-shot normal completion reported an error: %v", got)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestWorkerPanicBecomesRequiredFailure 验证 panic 转 WorkerPanicError 后
// 走与普通运行期异常完全相同的 Required 语义。
//
// 不能让所有 panic 一律升级为全进程致命（与 optional 契约冲突），
// 也不能只记录后继续把该 worker 当作健康（它已经不在跑了）。
func TestWorkerPanicBecomesRequiredFailure(t *testing.T) {
	t.Parallel()

	mgr, err := NewWorkerManager(testWorkerBudget(), &funcWorker{
		name: "boomer", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
		run: func(context.Context) error { panic("kaboom") },
	})
	if err != nil {
		t.Fatalf("NewWorkerManager: %v", err)
	}
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case got := <-mgr.Errors():
		panicErr, ok := errors.AsType[*WorkerPanicError](got)
		if !ok {
			t.Fatalf("error = %v, want a WorkerPanicError", got)
		}
		if panicErr.Worker != "boomer" {
			t.Fatalf("panic error worker = %q, want boomer", panicErr.Worker)
		}
		if len(panicErr.Stack) == 0 {
			t.Fatal("panic error carries no stack")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a panicking required worker produced no error")
	}
}

// TestOptionalWorkerPanicDoesNotReachErrorChannel 验证 optional worker 的 panic
// 只标降级、不触发全进程关停。
func TestOptionalWorkerPanicDoesNotReachErrorChannel(t *testing.T) {
	t.Parallel()

	panicked := make(chan struct{})
	mgr, err := NewWorkerManager(testWorkerBudget(), &funcWorker{
		name: "optional-boomer", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: false,
		run: func(context.Context) error { close(panicked); panic("kaboom") },
	})
	if err != nil {
		t.Fatalf("NewWorkerManager: %v", err)
	}
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-panicked

	select {
	case got := <-mgr.Errors():
		t.Fatalf("an optional worker panic reached the shutdown path: %v", got)
	case <-time.After(200 * time.Millisecond):
	}

	if _, ok := mgr.Degraded()["optional-boomer"]; !ok {
		t.Fatal("an optional worker panic was not recorded as degraded")
	}
}

// TestCleanupCalledExactlyOnce 钉住"Prepare 成功后 Cleanup 最终恰好一次"。
//
// 少于一次是资源泄漏（正常 Run 结束后没人释放 Prepare 建立的连接与锁），
// 多于一次会让不幂等的实现产生副作用，也让"恰好一次"这条契约无人保证。
func TestCleanupCalledExactlyOnce(t *testing.T) {
	t.Parallel()

	w := &funcWorker{
		name: "consumer", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
	}
	mgr, err := NewWorkerManager(testWorkerBudget(), w)
	if err != nil {
		t.Fatalf("NewWorkerManager: %v", err)
	}
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := mgr.StopKind(stopCtx, KindConsumer); err != nil {
		t.Fatalf("StopKind: %v", err)
	}
	// 兜底清理必须是空操作：supervise 已经清理过了。
	if err := mgr.CleanupRemaining(context.Background()); err != nil {
		t.Fatalf("CleanupRemaining: %v", err)
	}
	// 再来一次，证明幂等由管理器保证而不是靠调用方克制。
	if err := mgr.CleanupRemaining(context.Background()); err != nil {
		t.Fatalf("second CleanupRemaining: %v", err)
	}

	if got := w.cleanupCalls.Load(); got != 1 {
		t.Fatalf("cleanup calls = %d, want exactly 1", got)
	}
}

// TestStartupRollbackCleansInReverseOrder 验证启动失败时按 Prepare 的逆序清理。
//
// 逆序：后 Prepare 的可能依赖先 Prepare 的（如后者持有前者建立的连接），
// 正序清理会让后者在依赖已消失的情况下收尾。
func TestStartupRollbackCleansInReverseOrder(t *testing.T) {
	t.Parallel()

	var (
		mu    sync.Mutex
		order []string
	)
	record := func(name string) func(context.Context) error {
		return func(context.Context) error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}

	first := &funcWorker{
		name: "first", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
		cleanup: record("first"),
	}
	second := &funcWorker{
		name: "second", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
		cleanup: record("second"),
	}
	failing := &funcWorker{
		name: "failing", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: true,
		prepare: func(context.Context) error { return errors.New("nope") },
	}

	mgr, err := NewWorkerManager(testWorkerBudget(), first, second, failing)
	if err != nil {
		t.Fatalf("NewWorkerManager: %v", err)
	}

	if err := mgr.Start(context.Background()); err == nil {
		t.Fatal("Start returned nil although a required worker failed to prepare")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "second" || order[1] != "first" {
		t.Fatalf("cleanup order = %v, want [second first] (reverse of prepare order)", order)
	}
}

// TestRequiredOneShotCleanupFailureFailsStartup 验证 required OneShot 的
// Cleanup 失败视为启动失败。
//
// 它的资源（锁、连接）若没能释放，后续的服务运行就建立在一个不确定的前提上。
func TestRequiredOneShotCleanupFailureFailsStartup(t *testing.T) {
	t.Parallel()

	mgr, err := NewWorkerManager(testWorkerBudget(), &funcWorker{
		name: "repair", kind: KindBootstrap, lifecycle: LifecycleOneShot, required: true,
		cleanup: func(context.Context) error { return errors.New("release failed") },
	})
	if err != nil {
		t.Fatalf("NewWorkerManager: %v", err)
	}

	err = mgr.Start(context.Background())
	if err == nil {
		t.Fatal("Start returned nil although the required one-shot cleanup failed")
	}
	if !strings.Contains(err.Error(), "release failed") {
		t.Fatalf("error %q does not carry the cleanup cause", err)
	}
}

// TestStartupBudgetCoversRequiredOneShotRun 验证启动总预算覆盖
// required OneShot 的 **Run**，而不只是 Prepare。
//
// 只给 Prepare 设预算的话，一个卡住的启动期数据修复任务照样能让启动无限期挂起，
// 而编排器的启动探针早已超时。
func TestStartupBudgetCoversRequiredOneShotRun(t *testing.T) {
	t.Parallel()

	mgr, err := NewWorkerManager(
		WorkerBudget{Prepare: time.Second, Startup: 200 * time.Millisecond, Cleanup: time.Second},
		&funcWorker{
			name: "slow-repair", kind: KindBootstrap, lifecycle: LifecycleOneShot, required: true,
			run: func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			},
		})
	if err != nil {
		t.Fatalf("NewWorkerManager: %v", err)
	}

	start := time.Now()
	err = mgr.Start(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Start returned nil although the required one-shot exceeded the startup budget")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Start took %s; the startup budget did not bound the one-shot Run", elapsed)
	}
}

// TestOptionalPrepareFailureDoesNotBlockStartup 验证 optional Prepare 失败
// 不阻断启动，只标降级；且它没进入 prepared 集合，因此不会被 Cleanup。
func TestOptionalPrepareFailureDoesNotBlockStartup(t *testing.T) {
	t.Parallel()

	optional := &funcWorker{
		name: "optional", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: false,
		prepare: func(context.Context) error { return errors.New("no backend") },
	}
	mgr, err := NewWorkerManager(testWorkerBudget(), optional)
	if err != nil {
		t.Fatalf("NewWorkerManager: %v", err)
	}

	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start failed because of an optional worker: %v", err)
	}
	if _, ok := mgr.Degraded()["optional"]; !ok {
		t.Fatal("an optional prepare failure was not recorded as degraded")
	}
	if got := optional.cleanupCalls.Load(); got != 0 {
		t.Fatalf("cleanup calls = %d, want 0: a failed Prepare never enters the prepared set", got)
	}
}

func TestWorkerKindAndLifecycleStrings(t *testing.T) {
	t.Parallel()

	kinds := map[WorkerKind]string{
		KindBootstrap: "bootstrap", KindProducer: "producer", KindConsumer: "consumer",
		KindUnspecified: "unspecified", WorkerKind(200): "unspecified",
	}
	for k, want := range kinds {
		if got := k.String(); got != want {
			t.Errorf("WorkerKind(%d).String() = %q, want %q", k, got, want)
		}
	}

	lifecycles := map[Lifecycle]string{
		LifecycleLongRunning: "long-running", LifecycleOneShot: "one-shot",
		LifecycleUnspecified: "unspecified", Lifecycle(200): "unspecified",
	}
	for l, want := range lifecycles {
		if got := l.String(); got != want {
			t.Errorf("Lifecycle(%d).String() = %q, want %q", l, got, want)
		}
	}
}
