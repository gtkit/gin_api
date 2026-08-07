package breaker

import (
	"errors"
	"sync"
	"testing"

	"github.com/sony/gobreaker/v2"
)

// TestBreakerTripsAfterConsecutiveFailures 钉住熔断契约
// （outbound-http-resilience spec）：连续失败达到阈值后，后续调用不再执行、
// 立即返回熔断错误，且状态变更事件被发出。
func TestBreakerTripsAfterConsecutiveFailures(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Config{ConsecutiveFailures: 2})
	var transitions []string
	var mu sync.Mutex
	r.onStateChange = func(name string, from, to gobreaker.State) {
		mu.Lock()
		transitions = append(transitions, from.String()+"->"+to.String())
		mu.Unlock()
	}

	downstreamErr := errors.New("downstream down")
	for range 2 {
		if err := r.Do("billing", func() error { return downstreamErr }); !errors.Is(err, downstreamErr) {
			t.Fatalf("failing call returned %v, want the downstream error", err)
		}
	}

	executed := false
	err := r.Do("billing", func() error { executed = true; return nil })
	if executed {
		t.Fatal("the call was executed although the breaker is open: no fast-fail, no protection")
	}
	if !IsOpen(err) {
		t.Fatalf("err = %v, want an open-breaker fast failure", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(transitions) == 0 {
		t.Fatal("no state-change event was emitted; operators would never learn the breaker tripped")
	}
}

// TestBreakerIsolatesDownstreams 钉住实例按下游名隔离：一个下游熔断
// 不影响另一个下游；同名取到同一实例（状态跨调用累计）。
func TestBreakerIsolatesDownstreams(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Config{ConsecutiveFailures: 1})
	_ = r.Do("failing", func() error { return errors.New("boom") })

	if err := r.Do("healthy", func() error { return nil }); err != nil {
		t.Fatalf("a healthy downstream was affected by another downstream's breaker: %v", err)
	}
	if err := r.Do("failing", func() error { return nil }); !IsOpen(err) {
		t.Fatalf("err = %v, want open (state must accumulate across calls on the same name)", err)
	}
}
