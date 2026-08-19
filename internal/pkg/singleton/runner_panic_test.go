package singleton

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRunOncePanicReleasesLease 反证「租约释放在 panic 路径同样发生」：
// 把 runOnce 里的 Release 移出 defer，本测试必须失败——
// panic 后锁只能等 TTL 自然过期，下面的立即 Acquire 会拿不到。
func TestRunOncePanicReleasesLease(t *testing.T) {
	// 租期取得很长：若释放没有发生，Acquire 在本测试生命周期内必然失败，
	// 不会被"恰好过期"洗成假绿。
	lease, _ := newTestLease(t, 10*time.Minute)

	runner, err := NewRunner(lease, func(context.Context, Held) error {
		panic("task bug")
	}, Config{Interval: time.Hour, Guarantee: GuaranteeIdempotent})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("task panic did not propagate: runOnce must not swallow it")
			}
		}()
		runner.runOnce(t.Context())
	}()

	// 锁必须已被归还：立即抢锁成功，而不是 ErrNotAcquired 等一个 TTL。
	held, err := lease.Acquire(t.Context())
	if errors.Is(err, ErrNotAcquired) {
		t.Fatal("lease still held after task panic: Release must run on the panic path (defer)")
	}
	if err != nil {
		t.Fatalf("Acquire after panic = %v", err)
	}
	_ = lease.Release(t.Context(), held)
}
