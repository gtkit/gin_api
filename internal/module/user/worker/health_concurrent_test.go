package worker

import (
	"sync"
	"testing"
	"time"
)

// TestHealthStateIsSafeUnderConcurrentReadWrite 是 healthState "并发安全"声明的反证测试。
//
// 这条声明有明确的历史：`deactivation_cleanup.go` 的字段注释写着"**必须加锁**：
// 写发生在 Run 的 goroutine 上，而读发生在指标采集线程上（异步回调在采集周期
// 被调用）。此前只有日志读它、且与写同一 goroutine，不加锁是安全的；接上指标
// 之后那个前提就不成立了。"
//
// 也就是说：不加锁曾经是对的，是**接上指标这件事**让它变错的。而本包此前的
// 测试全部是串行的——把 healthState 的 mu 删掉，它们照样全绿，`-race` 也抓不到
// 任何东西（没有并发交错就没有竞态可报）。挂着 -race 跑串行测试，只是给
// 一个没有判据的声明加了个名头。
//
// 本测试让写（recordFailure / recordSuccess）与读（snapshot，即指标采集线程
// 走的那条路）真正并发。删掉 mu 后 `-race` 会在 consecutiveFailures 与
// lastSuccess 上报告数据竞争。
func TestHealthStateIsSafeUnderConcurrentReadWrite(t *testing.T) {
	t.Parallel()

	const (
		writers = 8
		readers = 8
		rounds  = 200
	)

	var h healthState

	var wg sync.WaitGroup
	start := make(chan struct{})

	// 写者：模拟 Run goroutine 的成功/失败交替。
	for i := range writers {
		wg.Go(func() {
			<-start
			for r := range rounds {
				if (i+r)%2 == 0 {
					h.recordFailure()
				} else {
					h.recordSuccess(time.Unix(int64(r), 0))
				}
			}
		})
	}

	// 读者：模拟指标采集线程的异步回调。
	for range readers {
		wg.Go(func() {
			<-start
			for range rounds {
				// 只要读到就行——断言的是"读写并发不产生竞态"，
				// 而具体读到哪个中间值本身没有确定性可言。
				_, _ = h.snapshot()
			}
		})
	}

	close(start)
	wg.Wait()

	// 收尾断言状态仍自洽：一次成功之后连续失败数必须能归零，
	// 否则说明有写丢失（锁没能保护住读改写序列）。
	h.recordSuccess(time.Unix(0, 0))
	if failures, _ := h.snapshot(); failures != 0 {
		t.Fatalf("recordSuccess 之后连续失败数 = %d, want 0", failures)
	}
}

// TestHealthStateFailureCountIsExactUnderConcurrency 钉住计数不丢。
//
// snapshot 的并发安全只保证"读不撕裂"，不保证"计数准确"——一个用原子读写
// 替掉互斥锁的实现能通过上一条测试，却会在 `consecutiveFailures++` 上丢更新。
// 连续失败数是告警阈值的输入，丢更新意味着告警比实际晚触发甚至不触发。
func TestHealthStateFailureCountIsExactUnderConcurrency(t *testing.T) {
	t.Parallel()

	const (
		writers = 16
		rounds  = 100
	)

	var h healthState
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			for range rounds {
				h.recordFailure()
			}
		})
	}
	wg.Wait()

	failures, _ := h.snapshot()
	if want := writers * rounds; failures != want {
		t.Fatalf("连续失败数 = %d, want %d：丢更新会让告警阈值比实际晚触发", failures, want)
	}
}
