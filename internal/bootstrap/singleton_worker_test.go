package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/gtkit/gin-api/internal/pkg/singleton"
)

// stubLeaseClient 是最小的租约客户端替身。
//
// 本文件测的是**编排契约**（Kind / Required / Lifecycle）与启动期的锁实测，
// 这两类都不需要真实 Redis。租约本身的语义在 internal/pkg/singleton 有覆盖。
type stubLeaseClient struct{}

func (stubLeaseClient) ScriptLoad(context.Context, string) *redis.StringCmd {
	return redis.NewStringResult("sha", nil)
}

func (stubLeaseClient) EvalSha(context.Context, string, []string, ...any) *redis.Cmd {
	return redis.NewCmdResult([]any{int64(1), int64(1)}, nil)
}

// TestSingletonWorkerContract 钉住编排契约。
//
// Kind 决定关停顺序，Required 决定失败是否拖垮进程——两者写错都不会有
// 编译错误，只会在关停或故障时才表现出来。
func TestSingletonWorkerContract(t *testing.T) {
	t.Parallel()

	w := newSingletonWorker("task", newTestRunner(t), stubLeaseClient{}, "app")

	// Producer：定时任务在关停时最先停。它与在飞请求无关，提前停不会让
	// 任何请求失败；继续跑则要么被硬取消，要么拖住关停。
	if got := w.Kind(); got != KindProducer {
		t.Errorf("Kind() = %v, want KindProducer", got)
	}
	// 非 required：一轮任务失败的正确处置是下一轮重试，不是让服务不可用。
	if w.Required() {
		t.Error("Required() = true: one dependency blip would escalate into an outage, " +
			"while the correct response is to retry next round")
	}
	if got := w.Lifecycle(); got != LifecycleLongRunning {
		t.Errorf("Lifecycle() = %v, want LifecycleLongRunning", got)
	}
	if w.Name() != "task" {
		t.Errorf("Name() = %q, want %q", w.Name(), "task")
	}

	// Cleanup 必须幂等：管理器保证恰好调用一次，但多调一次不得出错。
	for range 3 {
		if err := w.Cleanup(context.Background()); err != nil {
			t.Fatalf("Cleanup: %v", err)
		}
	}
}

// TestSingletonWorkerPrepareRejectsABrokenLock 钉住启动期实测会拦下坏锁。
//
// 只 PING 的话，一个不允许 EVAL 的 Redis 会让 worker "启动成功"，
// 然后每一轮都静默失败。
func TestSingletonWorkerPrepareRejectsABrokenLock(t *testing.T) {
	t.Parallel()

	runner := newTestRunner(t)
	w := newSingletonWorker("task", runner, brokenLeaseClient{}, "app")

	err := w.Prepare(context.Background())
	if err == nil {
		t.Fatal("Prepare passed against a Redis that cannot run the lock scripts: " +
			"the worker would start and then fail silently every round")
	}
	if !strings.Contains(err.Error(), "task") {
		t.Errorf("error %q does not name the worker", err)
	}
}

// brokenLeaseClient 模拟不允许执行脚本的 Redis。
type brokenLeaseClient struct{}

func (brokenLeaseClient) ScriptLoad(context.Context, string) *redis.StringCmd {
	return redis.NewStringResult("", errors.New("NOPERM this user has no permissions"))
}

func (brokenLeaseClient) EvalSha(context.Context, string, []string, ...any) *redis.Cmd {
	return redis.NewCmdResult(nil, errors.New("NOPERM"))
}

// newTestRunner 构造一个不会真的执行任何工作的 Runner。
func newTestRunner(t *testing.T) *singleton.Runner {
	t.Helper()

	lease, err := singleton.NewLease(stubLeaseClient{}, "app", "task", time.Minute)
	if err != nil {
		t.Fatalf("NewLease: %v", err)
	}
	runner, err := singleton.NewRunner(lease,
		func(context.Context, singleton.Held) error { return nil },
		singleton.Config{Interval: time.Hour, Guarantee: singleton.GuaranteeIdempotent})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner
}
