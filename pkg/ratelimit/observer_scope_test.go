package ratelimit

import (
	"context"
	"sync"
	"testing"
)

// countingObserver 数事件次数。
type countingObserver struct {
	mu            sync.Mutex
	phaseChanges  int
	localFallback int
}

func (o *countingObserver) BackendPhaseChanged(HealthPhase, HealthPhase) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.phaseChanges++
}

func (o *countingObserver) FellBackToLocal(string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.localFallback++
}

func (o *countingObserver) counts() (phase, fallback int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.phaseChanges, o.localFallback
}

// TestObserverStaysLowCardinality 钉住 Observer 的契约：**不是每请求回调**。
//
// 本包的 Observer 是给日志用的：它的文档承诺事件低基数，因此实现可以放心地
// 每次都写一条日志。哪天有人为了做指标在判定路径上加一个 Observer 事件，
// 一次流量高峰就会把调用方的日志刷满，而信息量只有第一条——那种改动在
// 功能测试里毫无迹象，只有上线后才发现。
//
// 每请求粒度的观测由**调用方**在中间件里做（那里判定结果已经拿到，
// 记一次指标就是一次计数器递增）。两者分处不同的包，是这条边界的实际形态。
func TestObserverStaysLowCardinality(t *testing.T) {
	t.Parallel()

	observer := &countingObserver{}
	fake := &fakeRedis{
		// 稳定放行：健康路径上不该有任何相位迁移。
		evalResults: []any{[]any{int64(1), int64(20), int64(0)}},
	}

	backend, err := NewRedisBackend(context.Background(), fake,
		WithKeyPrefix("svc"), WithObserver(observer))
	if err != nil {
		t.Fatalf("NewRedisBackend: %v", err)
	}

	policy := Policy{
		ID: "p", Rate: 100, Burst: 20,
		Scope: ScopeIP, Store: StoreRedis, FailureMode: FailureClosed, MaxKeys: 1000,
	}

	const requests = 200
	for range requests {
		if _, err := backend.check(context.Background(), policy, "subject"); err != nil {
			t.Fatalf("check: %v", err)
		}
	}

	phase, fallback := observer.counts()
	if phase != 0 || fallback != 0 {
		t.Fatalf("%d healthy requests produced %d phase events and %d fallback events, "+
			"want 0 and 0: the Observer contract is low-cardinality lifecycle events, "+
			"and per-request events there would flood the caller's logs",
			requests, phase, fallback)
	}
}
