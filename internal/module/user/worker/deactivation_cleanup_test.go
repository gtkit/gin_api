package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakePurger 记录清理调用并按脚本返回。
type fakePurger struct {
	mu      sync.Mutex
	calls   []time.Time
	batches []int
	// remaining 是"还剩多少行可删"，用于驱动分批循环。
	remaining int64
	err       error
}

func (f *fakePurger) DeleteOlderThan(_ context.Context, cutoff time.Time, limit int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, cutoff)
	f.batches = append(f.batches, limit)
	if f.err != nil {
		return 0, f.err
	}

	deleted := int64(limit)
	if f.remaining < deleted {
		deleted = f.remaining
	}
	f.remaining -= deleted
	return deleted, nil
}

func (f *fakePurger) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type recordingAuditor struct {
	mu      sync.Mutex
	entries []string
}

func (r *recordingAuditor) Auditf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, format)
}

func (r *recordingAuditor) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// TestNewDeactivationCleanupRejectsDisabledShape 钉住"禁用"不能表现为一个空转的 worker。
//
// 注册一个什么都不做的 worker 会在关停顺序与就绪信息里留下一个名字，
// 让人以为清理正在进行——而它一行都没删过。禁用必须表现为**根本不注册**。
func TestNewDeactivationCleanupRejectsDisabledShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		retention time.Duration
		interval  time.Duration
		batch     int
	}{
		{name: "zero retention", retention: 0, interval: time.Hour, batch: 10},
		{name: "negative retention", retention: -time.Hour, interval: time.Hour, batch: 10},
		{name: "zero interval", retention: time.Hour, interval: 0, batch: 10},
		{name: "zero batch", retention: time.Hour, interval: time.Hour, batch: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewDeactivationCleanup(&fakePurger{}, nil, tt.retention, tt.interval, tt.batch)
			if err == nil {
				t.Fatal("a degenerate configuration was accepted; it must fail at assembly")
			}
		})
	}

	if _, err := NewDeactivationCleanup(nil, nil, time.Hour, time.Hour, 10); err == nil {
		t.Fatal("a nil repository was accepted")
	}
}

// TestCleanupPurgesInBatchesUntilDrained 钉住单轮内分批删完。
//
// 每个间隔只删一批的话，积压很大时永远追不上——一天删 500 行，
// 而每天新增可能更多。
func TestCleanupPurgesInBatchesUntilDrained(t *testing.T) {
	t.Parallel()

	repo := &fakePurger{remaining: 1200}
	cleanup, err := NewDeactivationCleanup(repo, nil, 30*24*time.Hour, time.Hour, 500)
	if err != nil {
		t.Fatalf("NewDeactivationCleanup: %v", err)
	}
	cleanup.now = func() time.Time { return time.Unix(1754000000, 0) }

	cleanup.purge(context.Background())

	// 1200 行、每批 500：500 + 500 + 200，第三批不足一批即停。
	if got := repo.callCount(); got != 3 {
		t.Fatalf("delete calls = %d, want 3 (500+500+200 then stop on a short batch)", got)
	}
	if repo.remaining != 0 {
		t.Fatalf("remaining = %d, want the backlog fully drained", repo.remaining)
	}
}

// TestCleanupUsesRetentionCutoff 验证截止时刻按保留期回推。
func TestCleanupUsesRetentionCutoff(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	retention := 90 * 24 * time.Hour

	repo := &fakePurger{remaining: 1}
	cleanup, err := NewDeactivationCleanup(repo, nil, retention, time.Hour, 500)
	if err != nil {
		t.Fatalf("NewDeactivationCleanup: %v", err)
	}
	cleanup.now = func() time.Time { return now }

	cleanup.purge(context.Background())

	want := now.Add(-retention)
	if !repo.calls[0].Equal(want) {
		t.Fatalf("cutoff = %s, want %s", repo.calls[0], want)
	}
}

// TestCleanupFailureIsAuditedNotFatal 钉住删除失败留痕但不升级为致命。
//
// 这是 optional worker：一次失败的正确处置是下一轮重试，数据还在，没有丢失风险。
// 但**必须留痕**——静默失败会让一份该被清除的个人信息无限期留存，而没人知道。
func TestCleanupFailureIsAuditedNotFatal(t *testing.T) {
	t.Parallel()

	auditor := &recordingAuditor{}
	repo := &fakePurger{err: errors.New("database is down")}
	cleanup, err := NewDeactivationCleanup(repo, auditor, time.Hour, time.Hour, 10)
	if err != nil {
		t.Fatalf("NewDeactivationCleanup: %v", err)
	}
	cleanup.now = func() time.Time { return time.Unix(1754000000, 0) }

	cleanup.purge(context.Background())

	if auditor.count() == 0 {
		t.Fatal("a failed purge left no trace: the records stay forever and nobody knows")
	}
	// 失败后立即停止本轮，不无限重试。
	if got := repo.callCount(); got != 1 {
		t.Fatalf("delete calls = %d, want 1 (stop this round and retry next tick)", got)
	}
}

// TestCleanupStopsOnContextCancel 钉住关停时能立刻退出。
//
// 积压很大时单轮会跑很久；不检查 ctx 的话，关停要等这一轮跑完，
// 而那可能远超关停预算。
func TestCleanupStopsOnContextCancel(t *testing.T) {
	t.Parallel()

	repo := &fakePurger{remaining: 1_000_000}
	cleanup, err := NewDeactivationCleanup(repo, nil, time.Hour, time.Hour, 1)
	if err != nil {
		t.Fatalf("NewDeactivationCleanup: %v", err)
	}
	cleanup.now = func() time.Time { return time.Unix(1754000000, 0) }

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cleanup.purge(ctx)

	if repo.callCount() != 0 {
		t.Fatalf("delete calls = %d, want 0 on an already-canceled context", repo.callCount())
	}
}

// TestRunReturnsNilOnCancel 验证关停不被当作错误。
//
// ctx 取消是这个 worker 唯一正常的结束方式；返回错误会让编排层把一次正常
// 关停记成故障。
func TestRunReturnsNilOnCancel(t *testing.T) {
	t.Parallel()

	cleanup, err := NewDeactivationCleanup(&fakePurger{}, nil, time.Hour, time.Hour, 10)
	if err != nil {
		t.Fatalf("NewDeactivationCleanup: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := cleanup.Run(ctx); err != nil {
		t.Fatalf("Run returned %v on cancellation, want nil", err)
	}
}
