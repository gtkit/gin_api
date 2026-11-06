package ratelimit

import (
	"context"
	"sync"
	"testing"
	"time"
)

// blockingLimiter 的 Close 阻塞到 release 关闭。
type blockingLimiter struct {
	policy  Policy
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (l *blockingLimiter) Policy() Policy { return l.policy }

func (l *blockingLimiter) Check(context.Context, string) (Decision, error) {
	return Decision{Outcome: OutcomeAllowed}, nil
}

func (l *blockingLimiter) Close() {
	l.once.Do(func() { close(l.entered) })
	<-l.release
}

// TestRegistryCloseDoesNotHoldTheLock 钉住关闭执行体时不持注册表锁
// （reusable-packages spec）：Limiter 的 Close 是外部实现，持锁调用它会把
// "Close 必须又快又不重入"变成没有编译器背书的隐式契约，违反者得到死锁。
func TestRegistryCloseDoesNotHoldTheLock(t *testing.T) {
	t.Parallel()

	blocking := &blockingLimiter{
		policy: Policy{
			ID: "blocking", Rate: 1, Burst: 1, Scope: ScopeGlobal,
			Store: StoreLocal, FailureMode: FailureClosed, MaxKeys: 1,
		},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(blocking.release)

	registry := NewRegistry()
	if err := registry.Register(blocking); err != nil {
		t.Fatalf("Register: %v", err)
	}
	registry.Freeze()

	closed := make(chan struct{})
	go func() {
		if err := registry.Close(); err != nil {
			t.Errorf("registry.Close(): %v", err)
		}
		close(closed)
	}()

	select {
	case <-blocking.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("Close never reached the limiter")
	}

	// Close 仍卡在外部实现里：注册表的读取路径必须照常可用。
	done := make(chan struct{})
	go func() { _ = registry.IDs(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the registry lock is held while an external Close is running: " +
			"any Close that touches the registry would deadlock")
	}
}

// TestRegistryRejectsDuplicatesAndFreezes 验证注册表的两条不变式。
func TestRegistryRejectsDuplicatesAndFreezes(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()

	first, err := NewLocalBackend(testPolicy(nil))
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	if err := reg.Register(first); err != nil {
		t.Fatalf("Register: %v", err)
	}

	second, err := NewLocalBackend(testPolicy(nil))
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	if err := reg.Register(second); err == nil {
		t.Fatal("registry accepted a duplicate policy id; the effective one would depend on registration order")
	}

	reg.Freeze()
	third, err := NewLocalBackend(testPolicy(func(p *Policy) { p.ID = "third.policy" }))
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	if err := reg.Register(third); err == nil {
		t.Fatal("registry accepted a registration after being frozen")
	}

	if err := reg.Close(); err != nil {
		t.Errorf("reg.Close(): %v", err)
	}
}
