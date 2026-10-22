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
	failure := errors.New("boom")
	if err := r.Do("failing", func() error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("initial downstream failure: %v", err)
	}

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
		if err := r.Do("billing", func() error { return context.Canceled }); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled call: %v", err)
		}
	}
	for range 10 {
		if err := r.Do("billing", func() error { return context.DeadlineExceeded }); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expired call: %v", err)
		}
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
		if err := r.Do("billing", func() error { return businessErr }); !errors.Is(err, businessErr) {
			t.Fatalf("business rejection: %v", err)
		}
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

// TestExcludedErrorsDoNotResetFailureCount 钉住排除项不清零连续失败计数。
//
// 这是 IsExcluded 与 IsSuccessful 的实质差别，也是熔断能否真正生效的关键：
// 用 IsSuccessful 表达"不计入"会把该次调用**计为成功**从而清零计数，于是
// 下游连续失败 4 次（阈值 5）后夹进一次客户端取消，计数归零、永远到不了阈值
// ——保护看起来在，实际不在。
func TestExcludedErrorsDoNotResetFailureCount(t *testing.T) {
	t.Parallel()

	downstream := errors.New("downstream is down")
	r := NewRegistry(Config{ConsecutiveFailures: 3}, "svc")

	fail := func() error { return downstream }
	canceledFn := func() error { return context.Canceled }

	// 两次真实失败 + 一次取消（应被排除，不清零）+ 一次真实失败 = 触发熔断。
	if err := r.Do("svc", fail); !errors.Is(err, downstream) {
		t.Fatalf("first failure: %v", err)
	}
	if err := r.Do("svc", fail); !errors.Is(err, downstream) {
		t.Fatalf("second failure: %v", err)
	}
	if err := r.Do("svc", canceledFn); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation should pass through: %v", err)
	}
	if err := r.Do("svc", fail); !errors.Is(err, downstream) {
		t.Fatalf("third failure: %v", err)
	}

	// 第三次真实失败已达阈值：熔断必须已跳闸。
	err := r.Do("svc", fail)
	if !IsOpen(err) {
		t.Fatalf("熔断未跳闸（err=%v）：取消被当成成功清零了连续失败计数", err)
	}
}

// TestSuccessStillResetsFailureCount 钉住排除判据没有把成功也排除掉。
//
// 库在 isSuccessful 之前调用 isExcluded，且 nil error 也会走它。排除判据漏掉
// err != nil 的话，成功调用会被一并排除——连续失败计数从此永不清零，
// 一个早已恢复的下游会在下次失败时立刻跳闸。
func TestSuccessStillResetsFailureCount(t *testing.T) {
	t.Parallel()

	downstream := errors.New("downstream is down")
	r := NewRegistry(Config{ConsecutiveFailures: 3}, "svc")

	fail := func() error { return downstream }
	ok := func() error { return nil }

	for range 2 {
		if err := r.Do("svc", fail); !errors.Is(err, downstream) {
			t.Fatalf("warmup failure: %v", err)
		}
	}
	// 一次成功必须清零：否则下一次失败就是第三次，会误跳闸。
	if err := r.Do("svc", ok); err != nil {
		t.Fatalf("success returned an error: %v", err)
	}
	if err := r.Do("svc", fail); !errors.Is(err, downstream) {
		t.Fatalf("failure after success: %v", err)
	}
	// 计数已被成功清零，此时只累积了 1 次失败，不该跳闸。
	if err := r.Do("svc", fail); IsOpen(err) {
		t.Fatal("成功调用没有清零计数：熔断在远低于阈值的失败数上跳闸了")
	}
}

// TestJoinedErrorWithRealFailureIsNotExcluded 钉住聚合错误不被整体排除。
//
// errors.Is 对聚合错误的任一成员命中即真，于是
// errors.Join(context.Canceled, realFailure) 会被判成"只是取消"，
// 真实的下游故障从熔断统计里彻底消失——下游一直在坏，熔断器永远不跳。
func TestJoinedErrorWithRealFailureIsNotExcluded(t *testing.T) {
	t.Parallel()

	downstream := errors.New("downstream is down")
	mixed := errors.Join(context.Canceled, downstream)

	if !DefaultIsFailure(mixed) {
		t.Fatal("取消与真实故障的聚合错误被整体排除了：下游故障会从熔断统计里消失")
	}
	// 纯取消仍须排除。
	if DefaultIsFailure(errors.Join(context.Canceled, context.DeadlineExceeded)) {
		t.Fatal("纯取消/超时的聚合错误被计入了熔断")
	}
}
