package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
)

// Limiter 是一条策略的执行体。
type Limiter interface {
	// Check 判定一次请求。err 非 nil 表示后端故障，Decision 的 Outcome
	// 为 OutcomeBackendUnavailable，调用方按 FailureMode 处理。
	Check(ctx context.Context, key string) (Decision, error)
	// Policy 返回该 limiter 的策略。
	Policy() Policy
	// Close 释放资源。
	Close()
}

// Registry 是策略与执行体的注册表。
//
// 冻结后运行期不可变：策略集合是**启动期事实**，运行期可变会让
// "这条路由现在到底走哪条限额"失去确定答案，也让审计无从下手。
type Registry struct {
	mu       sync.RWMutex
	limiters map[string]Limiter
	frozen   bool
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{limiters: make(map[string]Limiter)}
}

// Register 登记一条策略的执行体。
//
// 重复 ID **报错而不是覆盖**：覆盖意味着最终生效的是"最后注册的那个"，
// 而注册顺序在装配代码里往往不明显，于是两条同名策略里哪条生效变成
// 一个要读代码才能回答的问题。
func (r *Registry) Register(limiter Limiter) error {
	if limiter == nil {
		return errors.New("rate limit registry: nil limiter")
	}
	policy := limiter.Policy()
	if err := policy.Validate(); err != nil {
		return fmt.Errorf("register rate limit policy: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.frozen {
		return fmt.Errorf("rate limit registry is frozen; cannot register %q", policy.ID)
	}
	if _, dup := r.limiters[policy.ID]; dup {
		return fmt.Errorf("duplicate rate limit policy id %q", policy.ID)
	}
	r.limiters[policy.ID] = limiter
	return nil
}

// Freeze 冻结注册表。
func (r *Registry) Freeze() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frozen = true
}

// Lookup 按 ID 取执行体。
func (r *Registry) Lookup(id string) (Limiter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	l, ok := r.limiters[id]
	return l, ok
}

// IDs 返回全部策略 ID，稳定排序。
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]string, 0, len(r.limiters))
	for id := range r.limiters {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Close 关闭全部执行体，错误聚合返回。
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, l := range r.limiters {
		l.Close()
	}
	return nil
}

// 编译期确认三种执行体都满足 Limiter 契约。
//
// 显式断言而不是靠注册时的隐式转换：后者要等到运行到那行才失败，
// 而这三者是本包对外的全部实现，任何一个签名漂移都应当立刻编译不过。
var (
	_ Limiter = (*LocalBackend)(nil)
	_ Limiter = (*RedisLimiter)(nil)
	_ Limiter = (*FallbackBackend)(nil)
)
