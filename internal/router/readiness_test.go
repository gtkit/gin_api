package router

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestRunDependencyChecksRunConcurrently 钉住并发执行。
//
// 旧实现用 map 字面量按序求值，两项依赖串行、各 2s，最坏 4s，且没有全局预算——
// 依赖数量一增加，探针自己就会先超时，编排器随后误判实例不健康。
// 判据用"两个检查在时间上重叠"而不是总耗时阈值：后者在负载高的机器上会偶发失败。
func TestRunDependencyChecksRunConcurrently(t *testing.T) {
	t.Parallel()

	var (
		mu       sync.Mutex
		inFlight int
		maxSeen  int
	)
	both := make(chan struct{})
	var once sync.Once

	slow := func(name string) func(context.Context) error {
		return func(ctx context.Context) error {
			mu.Lock()
			inFlight++
			if inFlight > maxSeen {
				maxSeen = inFlight
			}
			reached := inFlight >= 2
			mu.Unlock()

			if reached {
				once.Do(func() { close(both) })
			}
			select {
			case <-both:
			case <-ctx.Done():
			}

			mu.Lock()
			inFlight--
			mu.Unlock()
			return nil
		}
	}

	deps := []dependency{
		{name: "database", required: true, ping: slow("database")},
		{name: "redis", required: false, ping: slow("redis")},
	}

	results, requiredFailed, optionalFailed := runDependencyChecks(
		context.Background(), deps, newDepFailureLogLimiter())

	mu.Lock()
	peak := maxSeen
	mu.Unlock()

	if peak < 2 {
		t.Fatalf("peak in-flight checks = %d, want 2: dependency checks must run concurrently", peak)
	}
	if requiredFailed || optionalFailed {
		t.Fatalf("unexpected failures: required=%v optional=%v", requiredFailed, optionalFailed)
	}
	if len(results) != 2 {
		t.Fatalf("results = %v, want both dependencies reported", results)
	}
}

// TestRunDependencyChecksRespectGlobalDeadline 验证全局预算封顶。
//
// 单项预算再宽，整体也不能超过全局预算——否则依赖增多时最坏耗时线性增长。
func TestRunDependencyChecksRespectGlobalDeadline(t *testing.T) {
	t.Parallel()

	block := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	deps := []dependency{
		{name: "database", required: true, ping: block},
		{name: "redis", required: true, ping: block},
	}

	start := time.Now()
	_, requiredFailed, _ := runDependencyChecks(context.Background(), deps, newDepFailureLogLimiter())
	elapsed := time.Since(start)

	if !requiredFailed {
		t.Fatal("blocked required dependencies did not report failure")
	}
	// 留出充足余量，只证明"有上界"，不对精确耗时做断言。
	if elapsed > readyGlobalDeadline*2 {
		t.Fatalf("elapsed = %s, want bounded by the global readiness deadline %s", elapsed, readyGlobalDeadline)
	}
}

// TestRunDependencyChecksOneFailureDoesNotCancelOthers 验证一个依赖失败
// 不取消其余依赖的检查。
//
// 若用 errgroup 的错误传播语义取消同组任务，readiness 的输出就会取决于
// 哪个依赖先失败——同一故障场景每次给出不同的 body。
func TestRunDependencyChecksOneFailureDoesNotCancelOthers(t *testing.T) {
	t.Parallel()

	var redisRan bool
	deps := []dependency{
		{name: "database", required: true, ping: func(context.Context) error { return errors.New("down") }},
		{name: "redis", required: false, ping: func(ctx context.Context) error {
			// 给 database 的失败留出传播窗口；若组被取消，这里会拿到已取消的 ctx。
			select {
			case <-time.After(20 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
			redisRan = true
			return nil
		}},
	}

	results, requiredFailed, optionalFailed := runDependencyChecks(
		context.Background(), deps, newDepFailureLogLimiter())

	if !redisRan {
		t.Fatal("the second dependency check was canceled by the first failure")
	}
	if !requiredFailed {
		t.Fatal("required dependency failure was not reported")
	}
	if optionalFailed {
		t.Fatal("optional dependency was reported as failed although it succeeded")
	}
	if results["redis"] != statusOK {
		t.Fatalf("redis status = %q, want %q", results["redis"], statusOK)
	}
}

// TestDepFailureLogLimiter 覆盖限频的四条规则。
//
// 依赖持续故障时探针每秒都在跑，不限频就会用同一条消息淹没日志，
// 把真正的新信息埋掉；但类别变化与恢复必须立即可见，否则限频反而藏住了信息。
func TestDepFailureLogLimiter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	limiter := newDepFailureLogLimiter()
	limiter.now = func() time.Time { return now }

	boom := errors.New("connection refused")
	other := errors.New("i/o timeout")

	// 首次失败：记录时刻。
	limiter.observe("database", boom)
	first := limiter.state["database"].lastLoggedAt
	if first != now {
		t.Fatalf("first failure was not logged at %s", now)
	}

	// 间隔内的同类失败：不更新时刻（= 未打印）。
	now = now.Add(depFailureLogInterval / 2)
	limiter.observe("database", boom)
	if limiter.state["database"].lastLoggedAt != first {
		t.Fatal("a repeated failure within the interval was logged again")
	}

	// 错误类别变化：立即打印。
	limiter.observe("database", other)
	if limiter.state["database"].lastLoggedAt != now {
		t.Fatal("a different error category was not logged immediately")
	}
	if limiter.state["database"].lastCategory != other.Error() {
		t.Fatal("the recorded category was not updated")
	}

	// 恢复：重置状态。
	limiter.observe("database", nil)
	if limiter.state["database"].lastCategory != "" {
		t.Fatal("recovery did not reset the failure state")
	}

	// 状态表只按依赖名建 key，不随错误种类增长。
	for i := range 50 {
		limiter.observe("database", errors.New("distinct "+string(rune('a'+i%26))))
	}
	if len(limiter.state) != 1 {
		t.Fatalf("limiter state keys = %d, want 1: keys must be dependency names, not error texts", len(limiter.state))
	}
}
