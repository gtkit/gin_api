package breaker

import (
	"context"
	"errors"
	"testing"
)

// TestBreakerTripsAfterConsecutiveFailures 钉住熔断基本契约
// （outbound-http-resilience spec）：连续失败达到阈值后，后续调用不再执行、
// 立即返回熔断错误。
func TestBreakerTripsAfterConsecutiveFailures(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Config{ConsecutiveFailures: 2}, "billing")

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
}

// TestBreakerIsolatesDownstreams 钉住实例按下游名隔离且状态跨调用累计。
func TestBreakerIsolatesDownstreams(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Config{ConsecutiveFailures: 1}, "failing", "healthy")
	_ = r.Do("failing", func() error { return errors.New("boom") })

	if err := r.Do("healthy", func() error { return nil }); err != nil {
		t.Fatalf("a healthy downstream was affected by another downstream's breaker: %v", err)
	}
	if err := r.Do("failing", func() error { return nil }); !IsOpen(err) {
		t.Fatalf("err = %v, want open (state must accumulate across calls on the same name)", err)
	}
}

// TestCancellationDoesNotTripTheBreaker 钉住失败分类器的默认行为
// （outbound-http-resilience spec）：调用方取消与超时不计入熔断。
//
// 计进去的后果很具体：一次客户端批量取消或一次上游超时风暴会熔断掉一个
// 完全健康的下游——把下游的健康判断建立在与下游无关的信号上。
func TestCancellationDoesNotTripTheBreaker(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Config{ConsecutiveFailures: 2}, "billing")

	// 远超阈值的取消次数。
	for range 10 {
		_ = r.Do("billing", func() error { return context.Canceled })
	}
	for range 10 {
		_ = r.Do("billing", func() error { return context.DeadlineExceeded })
	}

	executed := false
	if err := r.Do("billing", func() error { executed = true; return nil }); err != nil {
		t.Fatalf("the breaker tripped on cancellations: %v", err)
	}
	if !executed {
		t.Fatal("the call was not executed: cancellations must not open the breaker")
	}
}

// TestCustomFailureClassifierExcludesBusinessErrors 钉住分类器可注入：
// 调用方声明的业务错误（如 HTTP 4xx、"余额不足"）不计入熔断。
func TestCustomFailureClassifierExcludesBusinessErrors(t *testing.T) {
	t.Parallel()

	businessErr := errors.New("insufficient balance")
	r := NewRegistry(Config{
		ConsecutiveFailures: 2,
		IsFailure: func(err error) bool {
			return err != nil && !errors.Is(err, businessErr) && DefaultIsFailure(err)
		},
	}, "billing")

	for range 10 {
		_ = r.Do("billing", func() error { return businessErr })
	}

	executed := false
	if err := r.Do("billing", func() error { executed = true; return nil }); err != nil || !executed {
		t.Fatalf("the breaker tripped on declared business errors (err=%v executed=%v)", err, executed)
	}
}

// TestUnknownDownstreamIsRejected 钉住下游集合是启动期事实
// （outbound-http-resilience spec）：未登记的名字报错且**不执行** fn，
// 注册表不新增实例。
//
// 隐式建实例的后果有两个：注册表随请求内容无界增长；一个拼错的名字
// 悄悄得到一个永远不会跳闸的独立熔断器，那次调用完全没有保护。
func TestUnknownDownstreamIsRejected(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Config{}, "billing")
	before := r.Downstreams()

	executed := false
	err := r.Do("typo-billing", func() error { executed = true; return nil })
	if !errors.Is(err, ErrUnknownDownstream) {
		t.Fatalf("err = %v, want ErrUnknownDownstream", err)
	}
	if executed {
		t.Fatal("the call ran without any breaker protection")
	}
	if got := r.Downstreams(); got != before {
		t.Fatalf("registry grew from %d to %d: the downstream set must be fixed at assembly", before, got)
	}
}
