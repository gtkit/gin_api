package jwtauth

import (
	"container/list"
	"crypto/sha256"
	"sync"
	"time"

	"github.com/gtkit/gojwt/claims"
)

// claimsCache 是实例级的有界 LRU 验签结果缓存。
//
// 三条设计约束，每条都对应一个具体风险：
//
//  1. **key 是 token 的 SHA-256 digest，不是 token 本身**。完整 bearer token
//     长期驻留在进程内存里，会出现在任何一次堆转储（包括 pprof 的 heap profile）
//     中——而 pprof 端点的整个安全模型是"里面有敏感数据，所以只绑回环"。
//     digest 不可逆，转储里拿到它也换不回令牌。
//  2. **只缓存成功结果，不做负缓存**。缓存失败会让一个刚被撤销、或刚过期的
//     令牌在缓存窗口内持续返回同一个失败——听起来无害，但它同时意味着
//     一个刚刚变得有效的令牌（时钟偏移边界）也会被持续拒绝。
//  3. **条目最晚在 token 自身的 exp 到期**。缓存不能让一个过期令牌继续可用，
//     哪怕只多一秒。
//
// 并发安全。
type claimsCache struct {
	maxEntries int

	mu    sync.Mutex
	items map[[32]byte]*list.Element
	order *list.List
}

// cacheEntry 是 LRU 链表里的一个条目。
type cacheEntry struct {
	key       [32]byte
	value     *claims.Claims
	expiresAt time.Time
}

// newClaimsCache 创建缓存；maxEntries <= 0 返回 nil（禁用）。
func newClaimsCache(maxEntries int) *claimsCache {
	if maxEntries <= 0 {
		return nil
	}
	return &claimsCache{
		maxEntries: maxEntries,
		items:      make(map[[32]byte]*list.Element, maxEntries),
		order:      list.New(),
	}
}

// get 取缓存结果并刷新 LRU 位置。
//
// 返回**深拷贝**：共享指针的话，任何一个调用方修改返回值都会污染后续
// 所有命中，而那种污染取决于请求顺序，在测试里几乎不可能复现。
func (c *claimsCache) get(token string, now time.Time) (*claims.Claims, bool) {
	if c == nil {
		return nil, false
	}

	key := sha256.Sum256([]byte(token))

	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.items[key]
	if !ok {
		return nil, false
	}

	entry, ok := elem.Value.(*cacheEntry)
	if !ok {
		c.removeLocked(elem)
		return nil, false
	}

	// 到期条目立即移除：缓存绝不能让一个过期令牌继续可用。
	if !now.Before(entry.expiresAt) {
		c.removeLocked(elem)
		return nil, false
	}

	c.order.MoveToFront(elem)
	return cloneClaims(entry.value), true
}

// put 写入成功验签的结果。
func (c *claimsCache) put(token string, value *claims.Claims) {
	if c == nil || value == nil || value.ExpiresAt == nil {
		return
	}

	key := sha256.Sum256([]byte(token))

	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.items[key]; ok {
		c.order.MoveToFront(elem)
		if entry, ok := elem.Value.(*cacheEntry); ok {
			entry.value = cloneClaims(value)
			entry.expiresAt = value.ExpiresAt.Time
		}
		return
	}

	entry := &cacheEntry{key: key, value: cloneClaims(value), expiresAt: value.ExpiresAt.Time}
	c.items[key] = c.order.PushFront(entry)

	// 达上限淘汰最久未用的条目。这里可以用 LRU：缓存是纯粹的性能优化，
	// 淘汰只会让下一次请求多做一次验签，不影响任何正确性判定。
	// （与限流的键基数上限不同——那里淘汰活跃桶等于放弃限额。）
	for c.order.Len() > c.maxEntries {
		c.removeLocked(c.order.Back())
	}
}

// removeLocked 从链表与索引里移除一个条目。调用方必须持锁。
func (c *claimsCache) removeLocked(elem *list.Element) {
	if elem == nil {
		return
	}
	c.order.Remove(elem)
	if entry, ok := elem.Value.(*cacheEntry); ok {
		delete(c.items, entry.key)
	}
}

// len 返回当前条目数（测试用）。
func (c *claimsCache) len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
