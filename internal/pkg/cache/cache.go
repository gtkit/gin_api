// Package cache 提供带 singleflight、TTL 抖动与容量上限的进程内缓存。
//
// 它解决的是三个各自独立、但总是一起出现的问题：
//
//	击穿——热点键过期的瞬间，所有并发请求同时回源，把一次缓存 miss 放大成
//	      N 次数据库查询。singleflight 让同一个键在同一时刻只有一次加载。
//	雪崩——一批同时写入的键会在同一瞬间集体过期，回源尖峰周期性重演。
//	      TTL 抖动把它们的到期时刻打散。
//	无界——键空间随请求变化时，缓存会一直涨。那不是命中率问题，是一次
//	      必然发生的 OOM。容量上限 + LRU 淘汰是能安全上线的前提。
//
// 三者缺一都会在生产上被发现，而且都是在流量最高的时候。
package cache

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// defaultJitterRatio 是 TTL 抖动的默认幅度（±10%）。
//
// 取 10% 而不是更大：抖动的目的是打散到期时刻，不是让缓存时长变得不可预期。
// 幅度过大时，同一个键两次写入的有效期能差出一倍，排查"为什么这次没命中"
// 会变得没有着落。
const defaultJitterRatio = 0.1

// defaultLoadTimeout 是单次回源的默认执行超时。
//
// 回源必须有独立超时：load 拿到的 ctx 剥离了调用方的取消（见 GetOrLoad），
// 没有这层超时，一个永久阻塞的 loader 会让 goroutine 与 singleflight 条目
// 随键空间无界积累——maxItems 管的是已完成的条目，管不到未完成的加载。
// 30s 比任何合理的进程内回源都长；需要更长的调用方显式配置 WithLoadTimeout。
const defaultLoadTimeout = 30 * time.Second

// defaultMaxInflight 是同时在途回源数的默认上限。
//
// 它是"阻塞的 loader 最多留下多少个 goroutine"的硬上界：超时只对监听 ctx
// 的 loader 有效，无视 ctx 的 loader 只能靠占着的在途名额把危害封在这个数以内。
const defaultMaxInflight = 64

// Cache 是一个有界的、带加载合并的进程内缓存。
//
// 并发安全。零值不可用，必须经 New 构造。
type Cache[T any] struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	// order 的表头是最近使用的，表尾是淘汰候选。
	order    *list.List
	maxItems int

	ttl         time.Duration
	jitterRatio float64

	// loadTimeout 约束单次回源的执行时长；maxInflight 约束**同时在途的不同键**
	// 数量（两者见 defaultLoadTimeout / defaultMaxInflight 的说明）。
	loadTimeout time.Duration
	maxInflight int

	// loadMu 保护 loading，并让"准入登记"与 DoChan 成为一个原子步骤。
	//
	// 二者必须原子：分开做的话，登记已被摘除而 singleflight 尚未清理该键的
	// 那个瞬间，新调用方会登记一个自己永远不会执行的加载——登记项就此泄漏，
	// 上限最终把所有键都挡在门外。
	loadMu  sync.Mutex
	loading map[string]*loadEntry

	// group 合并同一个键的并发加载。
	group singleflight.Group

	// now 与 jitter 为纯函数注入，让过期与抖动可被确定性地测试。
	now    func() time.Time
	jitter func(time.Duration, float64) time.Duration
}

type entry[T any] struct {
	key       string
	value     T
	expiresAt time.Time
}

// loadEntry 是一次在途回源的登记项。
type loadEntry struct {
	// fenced 记录这次加载期间该键是否被 Invalidate 过。
	//
	// 失效屏障（cache-preset spec）：数据源更新后调用方会 Invalidate，
	// 而此前发起的那次查询可能**晚于**更新才返回——无条件回填等于把刚被
	// 作废的旧值又按完整 TTL 写了回去，且没有任何一处会提示这件事。
	fenced bool
}

// ErrLoadsSaturated 表示同时在途的回源数已达上限，本次未发起加载。
//
// 快速失败而不是排队：排队本身就要占一个 goroutine，而上限存在的理由
// 正是"阻塞的 loader 不得无界积累"。
var ErrLoadsSaturated = errors.New("cache: too many concurrent loads in flight")

// Option 配置缓存。
type Option[T any] func(*Cache[T])

// WithClock 注入时钟（测试用）。
func WithClock[T any](now func() time.Time) Option[T] {
	return func(c *Cache[T]) {
		if now != nil {
			c.now = now
		}
	}
}

// WithJitterRatio 设置 TTL 抖动幅度（0 表示不抖动）。
func WithJitterRatio[T any](ratio float64) Option[T] {
	return func(c *Cache[T]) {
		if ratio >= 0 {
			c.jitterRatio = ratio
		}
	}
}

// WithLoadTimeout 设置单次回源的执行超时；非正值忽略，保持默认。
func WithLoadTimeout[T any](d time.Duration) Option[T] {
	return func(c *Cache[T]) {
		if d > 0 {
			c.loadTimeout = d
		}
	}
}

// WithMaxInflight 设置同时在途的回源数上限；非正值忽略，保持默认。
func WithMaxInflight[T any](n int) Option[T] {
	return func(c *Cache[T]) {
		if n > 0 {
			c.maxInflight = n
		}
	}
}

// New 构造缓存。
//
// maxItems 必须为正：无界缓存在键空间随请求变化时会一直涨。
// ttl 必须为正：不过期的缓存是一份永远不会被纠正的副本。
func New[T any](maxItems int, ttl time.Duration, opts ...Option[T]) (*Cache[T], error) {
	if maxItems <= 0 {
		return nil, errors.New("cache: maxItems must be positive (an unbounded cache eventually takes the process down)")
	}
	if ttl <= 0 {
		return nil, errors.New("cache: ttl must be positive (entries that never expire are a stale copy, not a cache)")
	}

	c := &Cache[T]{
		entries:     make(map[string]*list.Element, maxItems),
		order:       list.New(),
		maxItems:    maxItems,
		ttl:         ttl,
		jitterRatio: defaultJitterRatio,
		loadTimeout: defaultLoadTimeout,
		maxInflight: defaultMaxInflight,
		now:         time.Now,
		jitter:      randomJitter,
	}
	for _, opt := range opts {
		opt(c)
	}
	// 登记表在选项定型后创建：容量即上限。
	c.loading = make(map[string]*loadEntry, c.maxInflight)
	return c, nil
}

// GetOrLoad 返回缓存值；未命中时调用 load 并缓存其结果。
//
// 同一个键的并发调用只会执行**一次** load，其余调用等待并共享结果。
//
// ctx 取消只影响**当前调用方**：load 仍在为其他等待者继续执行。反过来做
// （取消即中断 load）会让一个提前放弃的调用方连累所有等待者一起失败，
// 而它们各自的 ctx 明明还有预算。
//
// load 的执行有两道独立上界：单次执行受 WithLoadTimeout 约束（load 必须
// 监听传入的 ctx），同时在途的加载数受 WithMaxInflight 约束——排队等不到
// 名额同样以超时失败。两道都是"阻塞的 loader 不无界积累"的保证。
//
// load 返回错误时不缓存：把失败缓存下来，一次瞬时故障会被固定住整个 TTL。
func (c *Cache[T]) GetOrLoad(
	ctx context.Context, key string, load func(context.Context) (T, error),
) (T, error) {
	var zero T

	if value, ok := c.get(key); ok {
		return value, nil
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}

	// 准入登记必须在 DoChan **之前**：DoChan 对每个新键都会立刻起一个
	// goroutine，把上限判定放进闭包里，高基数 miss 仍能在判定生效之前
	// 堆起任意多个 goroutine 与 singleflight 条目——上限是假的。
	//
	// 登记与 DoChan 在同一把锁内完成：见 loadMu 的说明。
	c.loadMu.Lock()
	state, joining := c.loading[key]
	if !joining {
		if len(c.loading) >= c.maxInflight {
			c.loadMu.Unlock()
			return zero, fmt.Errorf("%w (limit %d, key %q)", ErrLoadsSaturated, c.maxInflight, key)
		}
		state = &loadEntry{}
		c.loading[key] = state
	}

	// DoChan 而不是 Do：Do 会把调用方阻塞到 load 返回，没有办法响应
	// 自己的 ctx 取消——一个 30s 的慢查询会把所有等待者一起扣住。
	//
	// 传给 load 的 ctx 剥离调用方的取消（WithoutCancel）：这次加载可能在为
	// 其他调用方服务，不能因为**发起者**放弃就中断；值（trace、租户标识）
	// 保留。剥离取消不等于不设限——回源有自己的执行超时。
	//
	// joining 为真时本闭包不会被执行（singleflight 已有该键的在途调用），
	// 因此摘除登记的责任只属于登记者，不会重复也不会遗漏。
	ch := c.group.DoChan(key, func() (any, error) {
		if !joining {
			defer c.finishLoad(key)
		}

		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.loadTimeout)
		defer cancel()

		value, err := load(loadCtx)
		if err != nil {
			return zero, err
		}
		// 失效屏障：加载期间被 Invalidate 过就不回填——那次更新之后，
		// 这个值已经是过期数据，写回去等于把它按完整 TTL 复活。
		c.loadMu.Lock()
		fenced := state.fenced
		c.loadMu.Unlock()
		if !fenced {
			c.set(key, value)
		}
		return value, nil
	})
	c.loadMu.Unlock()

	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case result := <-ch:
		if result.Err != nil {
			return zero, result.Err
		}
		value, ok := result.Val.(T)
		if !ok {
			return zero, errors.New("cache: loaded value has an unexpected type")
		}
		return value, nil
	}
}

// Get 返回未过期的缓存值。
func (c *Cache[T]) get(key string) (T, bool) {
	var zero T

	c.mu.Lock()
	defer c.mu.Unlock()

	element, ok := c.entries[key]
	if !ok {
		return zero, false
	}
	item, ok := element.Value.(*entry[T])
	if !ok {
		return zero, false
	}
	if !c.now().Before(item.expiresAt) {
		// 过期即就地删除：留着它只会白占容量，把还有效的条目挤出去。
		c.removeElementLocked(element)
		return zero, false
	}

	c.order.MoveToFront(element)
	return item.value, true
}

// set 写入并在超出容量时淘汰最久未使用的条目。
func (c *Cache[T]) set(key string, value T) {
	expiresAt := c.now().Add(c.jitter(c.ttl, c.jitterRatio))

	c.mu.Lock()
	defer c.mu.Unlock()

	if element, ok := c.entries[key]; ok {
		if item, typeOK := element.Value.(*entry[T]); typeOK {
			item.value = value
			item.expiresAt = expiresAt
		}
		c.order.MoveToFront(element)
		return
	}

	c.entries[key] = c.order.PushFront(&entry[T]{key: key, value: value, expiresAt: expiresAt})

	for c.order.Len() > c.maxItems {
		if oldest := c.order.Back(); oldest != nil {
			c.removeElementLocked(oldest)
		}
	}
}

func (c *Cache[T]) removeElementLocked(element *list.Element) {
	c.order.Remove(element)
	if item, ok := element.Value.(*entry[T]); ok {
		delete(c.entries, item.key)
	}
}

// Len 返回当前条目数（含尚未被清理的过期条目）。
func (c *Cache[T]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// Invalidate 删除一个键。数据源变更后调用它，比等 TTL 到期快得多。
//
// 同时对该键**在途的加载**竖起屏障：那次查询可能早于本次更新发出、晚于它
// 返回，无条件回填会把刚作废的旧值按完整 TTL 写回去。屏障只影响已在途的
// 加载，之后新发起的加载照常回填。
func (c *Cache[T]) Invalidate(key string) {
	c.loadMu.Lock()
	if state, loading := c.loading[key]; loading {
		state.fenced = true
	}
	c.loadMu.Unlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		c.removeElementLocked(element)
	}
}

// finishLoad 摘除登记并让 singleflight 立刻忘掉该键。
//
// Forget 是必需的：本函数在闭包的 defer 里执行，早于 singleflight 自己删除
// 该键。不 Forget 的话，"登记已摘除、singleflight 仍持有"的瞬间会让下一个
// 调用方登记一个自己永远不会执行的加载，登记项就此泄漏。
func (c *Cache[T]) finishLoad(key string) {
	c.loadMu.Lock()
	defer c.loadMu.Unlock()
	delete(c.loading, key)
	c.group.Forget(key)
}

// randomJitter 在 [ttl*(1-ratio), ttl*(1+ratio)] 内取值。
//
// 结果下限保底 1ns：ratio 被配成 >= 1 时，不保底会算出非正数的 TTL，
// 那等于每次写入都立刻过期——缓存看起来在工作，实际一次也不会命中。
func randomJitter(ttl time.Duration, ratio float64) time.Duration {
	if ratio <= 0 {
		return ttl
	}
	span := float64(ttl) * ratio
	// rand.Float64() ∈ [0,1)，映射到 [-span, span)。
	//
	// 豁免理由：抖动只用来打散到期时刻，不承担任何安全职责——它的取值既不
	// 保护秘密，也不被任何判定信任。换成 crypto/rand 只会让每次缓存写入多
	// 一次系统调用，而防不住任何东西。
	jittered := float64(ttl) + (rand.Float64()*2-1)*span //nolint:gosec // 见上：抖动非安全用途
	if jittered < 1 {
		return 1
	}
	return time.Duration(jittered)
}
