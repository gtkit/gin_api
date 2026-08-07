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

// TestConcurrentLoadsAreCollapsed 钉住 singleflight：同一个键的并发加载只执行一次。
//
// 这是缓存击穿的正解：热点键过期的瞬间，所有并发请求同时回源，
// 一次 miss 会被放大成 N 次数据库查询——而那一刻正是流量最高的时候。
func TestConcurrentLoadsAreCollapsed(t *testing.T) {
	t.Parallel()

	c := newTestCache(t, 16, time.Minute)

	var loads atomic.Int32
	release := make(chan struct{})
	const concurrency = 32

	var wg sync.WaitGroup
	results := make([]string, concurrency)
	errs := make([]error, concurrency)
	for i := range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = c.GetOrLoad(context.Background(), "hot", func(context.Context) (string, error) {
				loads.Add(1)
				// 挂住，确保全部调用方都进入等待再放行。
				<-release
				return "value", nil
			})
		}()
	}

	// 等到有加载在执行再放行，否则可能第一个就跑完了。
	for loads.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()

	if got := loads.Load(); got != 1 {
		t.Fatalf("load 被执行 %d 次，want 1：并发加载没有被合并", got)
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
