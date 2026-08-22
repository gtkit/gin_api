package dao

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// TestResourcesCloseIsSafeUnderConcurrentCallers 是 "Close 幂等且**并发安全**"
// 这条声明的反证测试。
//
// 为什么串行的双次 Close 不够：`resources.go` 上写着 closeMu + closed 保证并发安全，
// 理由是"关停触发源不止一个（信号、worker 异常、HTTP 异常），它们可能并发到达"。
// 而既有的 TestResourcesCloseReverseOrderAndIdempotent 是**串行**调用两次——
// 把 closeMu 整个删掉，那个测试照样全绿。一条声明了并发安全却只有串行判据的
// 契约，等于凭空承诺。
//
// 本测试让 N 个 goroutine 同时进入 Close：
//   - closer 必须恰好执行一次（否则重复关闭底层连接池会把一次正常关停染成失败）；
//   - 每个调用方拿到的必须是同一个结果（缓存的首次错误）。
//
// 删掉 closeMu 后，`-race` 会在 closed / closeErr 上报告数据竞争，
// 且 closer 的执行次数会大于 1。
func TestResourcesCloseIsSafeUnderConcurrentCallers(t *testing.T) {
	t.Parallel()

	const callers = 32

	firstErr := errors.New("close failed")
	var closerRuns atomic.Int64

	res := &Resources{
		closers: []namedCloser{
			{name: "only", close: func() error {
				closerRuns.Add(1)
				return firstErr
			}},
		},
	}

	var (
		wg      sync.WaitGroup
		start   = make(chan struct{})
		results = make([]error, callers)
	)
	for i := range callers {
		wg.Go(func() {
			<-start // 尽量让全部 goroutine 在同一时刻进入 Close
			results[i] = res.Close()
		})
	}
	close(start)
	wg.Wait()

	if got := closerRuns.Load(); got != 1 {
		t.Fatalf("closer 执行了 %d 次，want 恰好 1 次："+
			"重复关闭底层连接池会返回错误，把一次正常关停染成失败", got)
	}
	for i, err := range results {
		if !errors.Is(err, firstErr) {
			t.Fatalf("第 %d 个调用方拿到 %v，want 与首次一致的 %v："+
				"并发调用方看到不同结果时，关停成功与否取决于谁先到", i, err, firstErr)
		}
	}
}

// TestResourcesCloseNilSafeUnderConcurrency 覆盖 nil 接收者的并发路径。
//
// 关停链上多处写 `defer res.Close()`，而装配失败时 res 可能是 nil。
// 与上一条同源：nil 判定若放在锁内侧就会先解引用。
func TestResourcesCloseNilSafeUnderConcurrency(t *testing.T) {
	t.Parallel()

	var res *Resources
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := res.Close(); err != nil {
				t.Errorf("nil Resources 的 Close() = %v, want nil", err)
			}
		})
	}
	wg.Wait()
}
