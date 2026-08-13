package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestCache(t *testing.T, maxItems int, ttl time.Duration, opts ...Option[string]) *Cache[string] {
	t.Helper()
	c, err := New(maxItems, ttl, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// TestNewRejectsUnboundedOrEternal 钉住构造期的两条硬要求。
func TestNewRejectsUnboundedOrEternal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		maxItems int
		ttl      time.Duration
	}{
		{name: "容量为零", maxItems: 0, ttl: time.Minute},
		{name: "容量为负", maxItems: -1, ttl: time.Minute},
		{name: "TTL 为零", maxItems: 10, ttl: 0},
		{name: "TTL 为负", maxItems: 10, ttl: -time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := New[string](tt.maxItems, tt.ttl); err == nil {
				t.Fatal("构造接受了无界或不过期的配置")
			}
		})
	}
}

// TestConcurrentLoadsAreCollapsed 钉住 singleflight：并发加载被合并。
//
// 这是缓存击穿的正解：热点键过期的瞬间，所有并发请求同时回源，
// 一次 miss 会被放大成 N 次数据库查询——而那一刻正是流量最高的时候。
//
// **断言是"远少于并发数"而不是"恰好 1 次"**：singleflight 只合并**同时在飞**
// 的调用，而"某个 goroutine 已经进入 singleflight"这件事从外部无法确定性地
// 观测到。原先只等一个 load 开始就放行，于是在负载高的机器上（发布门禁并行
// 跑几十个包时）第一个 load 可能在其余 goroutine 到达之前就完成、key 已释放，
// 后到的调用于是又跑一次——测试随机变红，而被测代码完全正确。
// 一个会随机失败的测试比没有测试更坏：它会训练人忽略红色。
func TestConcurrentLoadsAreCollapsed(t *testing.T) {
	t.Parallel()

	c := newTestCache(t, 16, time.Minute)

	var loads atomic.Int32
	var entered atomic.Int32
	release := make(chan struct{})
	const concurrency = 32

	var wg sync.WaitGroup
	results := make([]string, concurrency)
	errs := make([]error, concurrency)
	for i := range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entered.Add(1)
			results[i], errs[i] = c.GetOrLoad(context.Background(), "hot", func(context.Context) (string, error) {
				loads.Add(1)
				// 挂住，让后到的调用都落进同一次飞行。
				<-release
				return "value", nil
			})
		}()
	}

	// 等全部调用方都已进入 GetOrLoad，再放行第一个 load。
	// 这不能保证它们都已抵达 singleflight 内部（那一点无法从外部观测），
	// 但把窗口从"一个 load 开始"扩到"32 个都已进入函数"，合并必然发生在
	// 绝大多数调用上——因此下面断言的是"合并确实发生"，不是精确次数。
	for entered.Load() < concurrency {
		time.Sleep(time.Millisecond)
	}
	for loads.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()

	got := loads.Load()
	if got == 0 {
		t.Fatal("load 一次都没执行")
	}
	if got >= concurrency {
		t.Fatalf("load 被执行 %d 次（并发 %d）：并发加载完全没有被合并", got, concurrency)
	}
	for i := range concurrency {
		if errs[i] != nil {
			t.Fatalf("调用 %d 返回错误 %v", i, errs[i])
		}
		if results[i] != "value" {
			t.Fatalf("调用 %d 得到 %q，want %q", i, results[i], "value")
		}
	}
}

// TestInFlightLoadAbsorbsLaterCaller 钉住"在飞期间到达的调用一定被合并"。
//
// 这一条是确定性的，补上上面那个测试放弃的精度：只要第一个 load 还没返回，
// singleflight 的 key 就仍被占用，此后到达的同键调用**必然**共享它，
// 不可能启动第二次加载。
func TestInFlightLoadAbsorbsLaterCaller(t *testing.T) {
	t.Parallel()

	c := newTestCache(t, 16, time.Minute)

	var loads atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})

	load := func(context.Context) (string, error) {
		if loads.Add(1) == 1 {
			close(started)
		}
		<-release
		return "value", nil
	}

	first := make(chan string, 1)
	go func() {
		value, err := c.GetOrLoad(context.Background(), "hot", load)
		if err != nil {
			t.Errorf("first GetOrLoad: %v", err)
		}
		first <- value
	}()

	// 第一次加载确定已在飞：load 已被调用且尚未返回（release 未关闭）。
	<-started

	second := make(chan string, 1)
	go func() {
		value, err := c.GetOrLoad(context.Background(), "hot", load)
		if err != nil {
			t.Errorf("second GetOrLoad: %v", err)
		}
		second <- value
	}()

	// 让第二个调用有机会抵达 singleflight，再放行。key 在此期间一直被占用，
	// 因此它要么已被合并、要么尚未到达——两种情况都不会产生第二次加载。
	//
	// 取 200ms 而不是几十毫秒：发布门禁并行跑几十个测试包，机器负载高时一个
	// 已启动的 goroutine 被调度到需要的时间会拉长。窗口给足，这条断言才不会
	// 变成又一个随机失败——而随机失败的测试会训练人忽略红色。
	time.Sleep(200 * time.Millisecond)
	close(release)

	if got := <-first; got != "value" {
		t.Fatalf("first = %q, want %q", got, "value")
	}
	if got := <-second; got != "value" {
		t.Fatalf("second = %q, want %q", got, "value")
	}
	if got := loads.Load(); got != 1 {
		t.Fatalf("load 被执行 %d 次，want 1：在飞期间到达的调用没有被合并", got)
	}
}

// TestCachedValueSkipsLoad 钉住命中不回源。
func TestCachedValueSkipsLoad(t *testing.T) {
	t.Parallel()

	c := newTestCache(t, 16, time.Minute)

	var loads atomic.Int32
	load := func(context.Context) (string, error) {
		loads.Add(1)
		return "v", nil
	}

	for range 3 {
		if _, err := c.GetOrLoad(context.Background(), "k", load); err != nil {
			t.Fatalf("GetOrLoad: %v", err)
		}
	}
	if got := loads.Load(); got != 1 {
		t.Fatalf("load 被执行 %d 次，want 1", got)
	}
}

// TestErrorsAreNotCached 钉住失败不进缓存。
//
// 把失败缓存下来，一次瞬时故障会被固定住整个 TTL——故障早已恢复，
// 而调用方还在拿到那个错误。
func TestErrorsAreNotCached(t *testing.T) {
	t.Parallel()

	c := newTestCache(t, 16, time.Minute)
	wantErr := errors.New("upstream down")

	var loads atomic.Int32
	load := func(context.Context) (string, error) {
		if loads.Add(1) == 1 {
			return "", wantErr
		}
		return "recovered", nil
	}

	if _, err := c.GetOrLoad(context.Background(), "k", load); !errors.Is(err, wantErr) {
		t.Fatalf("first GetOrLoad error = %v, want %v", err, wantErr)
	}
	value, err := c.GetOrLoad(context.Background(), "k", load)
	if err != nil {
		t.Fatalf("second GetOrLoad: %v", err)
	}
	if value != "recovered" {
		t.Fatalf("second GetOrLoad = %q, want %q（失败被缓存了）", value, "recovered")
	}
}

// TestCapacityIsEnforcedWithLRUEviction 钉住容量上限与淘汰顺序。
func TestCapacityIsEnforcedWithLRUEviction(t *testing.T) {
	t.Parallel()

	const capacity = 3
	c := newTestCache(t, capacity, time.Minute)

	load := func(v string) func(context.Context) (string, error) {
		return func(context.Context) (string, error) { return v, nil }
	}

	for i := range capacity {
		key := fmt.Sprintf("k%d", i)
		if _, err := c.GetOrLoad(context.Background(), key, load(key)); err != nil {
			t.Fatalf("GetOrLoad: %v", err)
		}
	}
	if got := c.Len(); got != capacity {
		t.Fatalf("Len() = %d, want %d", got, capacity)
	}

	// 访问 k0 让它成为最近使用的，淘汰候选变成 k1。
	if _, err := c.GetOrLoad(context.Background(), "k0", load("unused")); err != nil {
		t.Fatalf("GetOrLoad: %v", err)
	}
	if _, err := c.GetOrLoad(context.Background(), "k3", load("k3")); err != nil {
		t.Fatalf("GetOrLoad: %v", err)
	}

	if got := c.Len(); got != capacity {
		t.Fatalf("超出容量后 Len() = %d, want %d", got, capacity)
	}

	// k1 应已被淘汰：再取会触发一次新的 load。
	var reloaded atomic.Bool
	if _, err := c.GetOrLoad(context.Background(), "k1", func(context.Context) (string, error) {
		reloaded.Store(true)
		return "k1", nil
	}); err != nil {
		t.Fatalf("GetOrLoad: %v", err)
	}
	if !reloaded.Load() {
		t.Fatal("最久未使用的条目没有被淘汰")
	}
}

// TestExpiredEntriesReload 钉住过期后回源，且过期条目不白占容量。
func TestExpiredEntriesReload(t *testing.T) {
	t.Parallel()

	var now atomic.Int64
	now.Store(time.Now().UnixNano())
	clock := func() time.Time { return time.Unix(0, now.Load()) }

	c := newTestCache(t, 8, time.Minute,
		WithClock[string](clock),
		// 关掉抖动，让过期时刻可确定地推进。
		WithJitterRatio[string](0),
	)

	var loads atomic.Int32
	load := func(context.Context) (string, error) {
		loads.Add(1)
		return "v", nil
	}

	if _, err := c.GetOrLoad(context.Background(), "k", load); err != nil {
		t.Fatalf("GetOrLoad: %v", err)
	}
	now.Add(int64(2 * time.Minute))
	if _, err := c.GetOrLoad(context.Background(), "k", load); err != nil {
		t.Fatalf("GetOrLoad: %v", err)
	}

	if got := loads.Load(); got != 2 {
		t.Fatalf("load 被执行 %d 次，want 2（过期后应回源）", got)
	}
	if got := c.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1（过期条目应就地删除，不白占容量）", got)
	}
}

// TestJitterSpreadsExpiry 钉住 TTL 抖动真的打散了到期时刻。
//
// 没有抖动时，一批同时写入的键会在同一瞬间集体过期，回源尖峰周期性重演。
func TestJitterSpreadsExpiry(t *testing.T) {
	t.Parallel()

	ttl := time.Minute
	ratio := 0.1

	low := time.Duration(float64(ttl) * (1 - ratio))
	high := time.Duration(float64(ttl) * (1 + ratio))

	seen := make(map[time.Duration]struct{})
	for range 200 {
		got := randomJitter(ttl, ratio)
		if got < low || got > high {
			t.Fatalf("抖动后的 TTL %s 超出 [%s, %s] 区间", got, low, high)
		}
		seen[got] = struct{}{}
	}
	if len(seen) < 2 {
		t.Fatal("抖动没有产生任何差异：到期时刻仍然集中在同一瞬间")
	}

	// ratio 为 0 时不抖动，行为必须可预期。
	if got := randomJitter(ttl, 0); got != ttl {
		t.Fatalf("ratio=0 时 TTL = %s, want %s", got, ttl)
	}
	// ratio 过大时结果仍须为正，否则每次写入都立刻过期，
	// 缓存看起来在工作而一次也不会命中。
	if got := randomJitter(time.Millisecond, 5); got < 1 {
		t.Fatalf("大幅抖动算出了非正 TTL %s", got)
	}
}

// TestCallerCancellationDoesNotBreakOthers 钉住取消的影响范围。
//
// 一个提前放弃的调用方不得连累其他等待者：它们各自的 ctx 明明还有预算。
func TestCallerCancellationDoesNotBreakOthers(t *testing.T) {
	t.Parallel()

	c := newTestCache(t, 8, time.Minute)

	release := make(chan struct{})
	started := make(chan struct{})
	var loads atomic.Int32

	load := func(ctx context.Context) (string, error) {
		if loads.Add(1) == 1 {
			close(started)
		}
		<-release
		// 加载用的 ctx 不该因为发起者取消而结束。
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "value", nil
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := c.GetOrLoad(cancelCtx, "k", load)
		firstDone <- err
	}()

	<-started

	secondDone := make(chan string, 1)
	go func() {
		value, err := c.GetOrLoad(context.Background(), "k", load)
		if err != nil {
			t.Errorf("second GetOrLoad: %v", err)
		}
		secondDone <- value
	}()

	// 让第二个调用方也进入等待，再取消第一个。
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("被取消的调用方得到 %v, want context.Canceled", err)
	}

	close(release)
	if got := <-secondDone; got != "value" {
		t.Fatalf("另一个等待者得到 %q, want %q：它被别人的取消连累了", got, "value")
	}
	if got := loads.Load(); got != 1 {
		t.Fatalf("load 被执行 %d 次，want 1", got)
	}
}

// TestInvalidateDropsKey 钉住主动失效。
func TestInvalidateDropsKey(t *testing.T) {
	t.Parallel()

	c := newTestCache(t, 8, time.Minute)

	var loads atomic.Int32
	load := func(context.Context) (string, error) {
		loads.Add(1)
		return "v", nil
	}

	if _, err := c.GetOrLoad(context.Background(), "k", load); err != nil {
		t.Fatalf("GetOrLoad: %v", err)
	}
	c.Invalidate("k")
	c.Invalidate("missing") // 不存在的键不得出事
	if _, err := c.GetOrLoad(context.Background(), "k", load); err != nil {
		t.Fatalf("GetOrLoad: %v", err)
	}
	if got := loads.Load(); got != 2 {
		t.Fatalf("load 被执行 %d 次，want 2（失效后应回源）", got)
	}
}

// TestPreCancelledContextSkipsLoad 钉住已取消的 ctx 不触发加载。
func TestPreCancelledContextSkipsLoad(t *testing.T) {
	t.Parallel()

	c := newTestCache(t, 8, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var loaded atomic.Bool
	_, err := c.GetOrLoad(ctx, "k", func(context.Context) (string, error) {
		loaded.Store(true)
		return "v", nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if loaded.Load() {
		t.Fatal("已取消的调用仍然触发了加载")
	}
}

// TestBlockedLoaderTimesOutIndependently 钉住回源的独立执行超时。
//
// load 拿到的 ctx 剥离了调用方的取消，没有独立超时的话，一个永久阻塞的
// loader 会带着 goroutine 与 singleflight 条目一起留下来——每个不同的键
// 一个，maxItems 管不到它们。
func TestBlockedLoaderTimesOutIndependently(t *testing.T) {
	t.Parallel()

	c := newTestCache(t, 8, time.Minute, WithLoadTimeout[string](50*time.Millisecond))

	start := time.Now()
	_, err := c.GetOrLoad(context.Background(), "k", func(ctx context.Context) (string, error) {
		<-ctx.Done() // 合规的 loader：阻塞但监听 ctx
		return "", ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded from the load timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("load 超时耗时 %s：独立超时没有生效", elapsed)
	}

	// 超时后同一个键必须能重新加载（singleflight 条目已清理）。
	v, err := c.GetOrLoad(context.Background(), "k", func(context.Context) (string, error) {
		return "recovered", nil
	})
	if err != nil || v != "recovered" {
		t.Fatalf("重载 = (%q, %v)，want (recovered, nil)", v, err)
	}
}

// TestInflightLoadsAreBounded 钉住在途上限是**真实**的：超限的新键
// 立即失败，且不为它创建 goroutine 与 singleflight 条目。
//
// 回归点：上限判定曾放在 DoChan 的闭包里，而 DoChan 对每个新键都会先起一个
// goroutine——判定生效之前，高基数 miss 已经把 goroutine 堆起来了。
func TestInflightLoadsAreBounded(t *testing.T) {
	t.Parallel()

	c := newTestCache(t, 8, time.Minute, WithMaxInflight[string](1))

	release := make(chan struct{})
	started := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = c.GetOrLoad(context.Background(), "occupier", func(context.Context) (string, error) {
			close(started)
			<-release
			return "a", nil
		})
	}()
	<-started

	var ran atomic.Bool
	_, err := c.GetOrLoad(context.Background(), "queued", func(context.Context) (string, error) {
		ran.Store(true)
		return "b", nil
	})
	if !errors.Is(err, ErrLoadsSaturated) {
		t.Fatalf("超限加载 = %v，want ErrLoadsSaturated", err)
	}
	if ran.Load() {
		t.Fatal("名额已满仍执行了新的加载：在途上限没有生效")
	}

	// 同一个键的并发调用是**合并**而不是新增在途项，不该被上限挡下。
	joined := make(chan error, 1)
	go func() {
		_, joinErr := c.GetOrLoad(context.Background(), "occupier", func(context.Context) (string, error) {
			return "should not run", nil
		})
		joined <- joinErr
	}()

	close(release)
	wg.Wait()
	if err := <-joined; err != nil {
		t.Fatalf("同键并发调用 = %v，want nil（合并而非占用新名额）", err)
	}

	// 释放后新键恢复正常。这同时证明被拒的调用没有留下 singleflight 条目：
	// 留下的话，这次同键调用会挂到一个永远不会执行的 leader 上。
	if v, err := c.GetOrLoad(context.Background(), "queued", func(context.Context) (string, error) {
		return "b", nil
	}); err != nil || v != "b" {
		t.Fatalf("释放名额后的加载 = (%q, %v)，want (b, nil)", v, err)
	}
}

// TestInvalidateFencesInFlightLoad 钉住失效屏障：数据源更新并 Invalidate 之后，
// 此前发起的旧查询不得把作废的值按完整 TTL 写回去。
func TestInvalidateFencesInFlightLoad(t *testing.T) {
	t.Parallel()

	c := newTestCache(t, 8, time.Minute)

	loading := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.GetOrLoad(context.Background(), "k", func(context.Context) (string, error) {
			close(loading)
			<-release
			return "stale", nil // 早于更新发出、晚于更新返回的旧值
		})
	}()

	<-loading
	c.Invalidate("k") // 数据源已更新
	close(release)
	<-done

	// 屏障生效：旧值没有被写回，下一次读必须重新回源。
	var reloaded atomic.Bool
	v, err := c.GetOrLoad(context.Background(), "k", func(context.Context) (string, error) {
		reloaded.Store(true)
		return "fresh", nil
	})
	if err != nil {
		t.Fatalf("GetOrLoad: %v", err)
	}
	if !reloaded.Load() || v != "fresh" {
		t.Fatalf("命中了被 Invalidate 的旧值 %q：失效屏障没有生效", v)
	}

	// 屏障只作用于当时在途的那次加载，之后的加载照常回填。
	if _, err := c.GetOrLoad(context.Background(), "k", func(context.Context) (string, error) {
		t.Fatal("屏障没有解除：新加载的结果也被拒绝回填")
		return "", nil
	}); err != nil {
		t.Fatalf("GetOrLoad after fence: %v", err)
	}
}
