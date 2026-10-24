// Package cache 提供带加载合并、TTL 抖动与 LRU 容量上限的进程内缓存。
package cache

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime/debug"
	"sync"
	"time"

	"github.com/gtkit/goerr"
	"github.com/gtkit/logger/v2"
	"go.uber.org/zap"
)

// defaultJitterRatio 是 TTL 抖动的默认幅度（±10%）。
//
// 取 10% 而不是更大：抖动的目的是打散到期时刻，不是让缓存时长变得不可预期。
// 幅度过大时，同一个键两次写入的有效期能差出一倍，排查"为什么这次没命中"
// 会变得没有着落。
const defaultJitterRatio = 0.1

// defaultLoadTimeout 是单次回源的默认执行超时。
//
// load 的 ctx 剥离调用方取消后仍需独立预算；无视 ctx 的 loader 由
// maxInflight 限制数量，不能在超时后提前释放它的名额。
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

	// loadMu 保护准入、失效屏障与加载结果提交。嵌套加锁顺序固定为 loadMu → mu。
	loadMu  sync.Mutex
	loading map[string]*loadEntry[T]

	// now 与 jitter 为纯函数注入，让过期与抖动可被确定性地测试。
	now    func() time.Time
	jitter func(time.Duration, float64) time.Duration
}

type entry[T any] struct {
	key       string
	value     T
	expiresAt time.Time
}

// loadEntry 为一个在途键保留一份结果和完成通知，不保存等待者列表。
type loadEntry[T any] struct {
	done  chan struct{}
	value T
	err   error
	// fenced 由 loadMu 保护；value/err 在关闭 done 前发布，之后不再修改。
	fenced bool
}

// ErrLoadsSaturated 表示同时在途的回源数已达上限，本次未发起加载。
//
// 快速失败而不是排队：排队本身就要占一个 goroutine，而上限存在的理由
// 正是"阻塞的 loader 不得无界积累"。
var ErrLoadsSaturated = errors.New("cache: too many concurrent loads in flight")

// ErrLoaderPanic identifies a recovered asynchronous loader failure.
var ErrLoaderPanic = errors.New("cache: loader panicked")

// loaderPanicError keeps an error-valued panic discoverable without exposing its text.
type loaderPanicError struct{ cause error }

func (*loaderPanicError) Error() string        { return ErrLoaderPanic.Error() }
func (*loaderPanicError) Is(target error) bool { return target == ErrLoaderPanic }
func (p *loaderPanicError) Unwrap() error      { return p.cause }

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
	c.loading = make(map[string]*loadEntry[T], c.maxInflight)
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
// 监听传入的 ctx），同时在途的不同键受 WithMaxInflight 约束，名额满时立即
// 返回 ErrLoadsSaturated。同键等待者共享一个完成通知，各自响应自己的取消。
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

	// 等锁期间前一次加载可能已经提交；二次读取与新登记共享同步边界。
	// 注入的时钟在锁外调用，避免回调重入。
	now := c.now()
	c.loadMu.Lock()
	if value, ok := c.getAt(key, now); ok {
		c.loadMu.Unlock()
		return value, nil
	}
	state, joining := c.loading[key]
	if !joining {
		if len(c.loading) >= c.maxInflight {
			c.loadMu.Unlock()
			return zero, fmt.Errorf("%w (limit %d, key %q)", ErrLoadsSaturated, c.maxInflight, key)
		}
		state = &loadEntry[T]{done: make(chan struct{})}
		c.loading[key] = state
	}
	c.loadMu.Unlock()
	if !joining {
		go c.runLoad(ctx, key, state, load)
	}

	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-state.done:
		if state.err != nil {
			return zero, state.err
		}
		return state.value, nil
	}
}

// runLoad 只由登记者启动；名额一直保留到 loader 真实返回或 panic 恢复。
func (c *Cache[T]) runLoad(ctx context.Context, key string, state *loadEntry[T], load func(context.Context) (T, error)) {
	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.loadTimeout)
	defer cancel()

	var value T
	var loadErr error
	var expiresAt time.Time
	defer func() { c.finishLoad(key, state, value, loadErr, expiresAt) }()
	defer func() {
		if recovered := recover(); recovered != nil {
			failure := &loaderPanicError{}
			if cause, ok := recovered.(error); ok {
				failure.cause = cause
			}
			var zero T
			value = zero
			loadErr = goerr.New(failure, goerr.StatusInternalServer())
			logger.ErrorCtx(loadCtx, "cache loader panic recovered",
				zap.String("stack", string(debug.Stack())))
		}
	}()

	value, loadErr = load(loadCtx)
	if loadErr == nil {
		// 时钟与抖动函数可能被注入，必须在提交锁外执行。
		expiresAt = c.now().Add(c.jitter(c.ttl, c.jitterRatio))
	}
}

// get 返回未过期的缓存值。
func (c *Cache[T]) get(key string) (T, bool) {
	return c.getAt(key, c.now())
}

func (c *Cache[T]) getAt(key string, now time.Time) (T, bool) {
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
	if !now.Before(item.expiresAt) {
		// 过期即就地删除：留着它只会白占容量，把还有效的条目挤出去。
		c.removeElementLocked(element)
		return zero, false
	}

	c.order.MoveToFront(element)
	return item.value, true
}

// setLocked 写入并淘汰超出容量的最旧条目；调用方持有 mu。
func (c *Cache[T]) setLocked(key string, value T, expiresAt time.Time) {
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
// 在同一临界区阻止旧加载回填并删除缓存。已经参与旧加载的等待者仍可能拿到
// 该次结果；等它结束后新发起的加载可正常回填。
func (c *Cache[T]) Invalidate(key string) {
	c.loadMu.Lock()
	defer c.loadMu.Unlock()
	if state, loading := c.loading[key]; loading {
		state.fenced = true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		c.removeElementLocked(element)
	}
}

// finishLoad 把归属核对、屏障检查、缓存提交及登记移除放在同一同步边界内。
func (c *Cache[T]) finishLoad(key string, state *loadEntry[T], value T, loadErr error, expiresAt time.Time) {
	c.loadMu.Lock()
	if c.loading[key] == state {
		if loadErr == nil && !state.fenced {
			c.mu.Lock()
			c.setLocked(key, value, expiresAt)
			c.mu.Unlock()
		}
		delete(c.loading, key)
	}
	state.value, state.err = value, loadErr
	c.loadMu.Unlock()
	close(state.done)
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
