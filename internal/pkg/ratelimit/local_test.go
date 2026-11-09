package ratelimit

import (
	"context"
	"testing"
)

// TestLocalBackendEnforcesBurstAndRate 验证本地桶的基本行为。
func TestLocalBackendEnforcesBurstAndRate(t *testing.T) {
	t.Parallel()

	// Rate 极低，保证测试期间几乎不补充：断言的是 Burst 边界本身。
	backend, err := NewLocalBackend(testPolicy(func(p *Policy) {
		p.Rate, p.Burst = 0.01, 3
	}))
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	defer backend.Close()

	ctx := context.Background()
	for i := range 3 {
		d, err := backend.Check(ctx, "k")
		if err != nil {
			t.Fatalf("Check %d: %v", i, err)
		}
		if !d.Allowed() {
			t.Fatalf("request %d was rejected within the burst of 3", i)
		}
	}

	d, err := backend.Check(ctx, "k")
	if err != nil {
		t.Fatalf("Check beyond burst: %v", err)
	}
	if d.Allowed() {
		t.Fatal("a request beyond the burst was allowed")
	}
	if d.Outcome != OutcomeRateLimited {
		t.Fatalf("outcome = %s, want rate_limited", d.Outcome)
	}
}

// TestLocalBackendRejectsNewKeysAtBound 钉住"达上限拒绝新建，不淘汰活跃桶"。
//
// LRU 淘汰是限流绕过：攻击者轮换超过 MaxKeys 个主体就能反复把旧桶挤掉，
// 旧主体回来时拿到一个满桶。因此达上限时必须拒绝新键，并且这个拒绝
// 要能与普通限速区分开——它说明主体数量异常，而不是某个主体太快。
func TestLocalBackendRejectsNewKeysAtBound(t *testing.T) {
	t.Parallel()

	backend, err := NewLocalBackend(testPolicy(func(p *Policy) {
		p.MaxKeys = 2
	}))
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	defer backend.Close()

	ctx := context.Background()
	for _, key := range []string{"a", "b"} {
		if d, err := backend.Check(ctx, key); err != nil || !d.Allowed() {
			t.Fatalf("key %q was rejected while under the key bound", key)
		}
	}

	d, err := backend.Check(ctx, "c")
	if err != nil {
		t.Fatalf("key capacity check: %v", err)
	}
	if d.Allowed() {
		t.Fatal("a new key was accepted beyond the key bound")
	}
	if d.Outcome != OutcomeKeyBudgetExhausted {
		t.Fatalf("outcome = %s, want key_budget_exhausted (it must be distinguishable from plain rate limiting)",
			d.Outcome)
	}

	// 已存在的桶必须仍然可用——被挤掉的话就等于 LRU 淘汰。
	if _, err := backend.Check(ctx, "a"); err != nil {
		t.Fatalf("existing key failed after the bound was reached: %v", err)
	}
}
