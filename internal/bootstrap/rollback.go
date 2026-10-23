package bootstrap

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
)

// rollbackStep 是一步可回滚的副作用：name 用于错误指认，fn 执行回滚动作。
type rollbackStep struct {
	name string
	fn   func() error
}

// rollbackStack 收集装配过程中产生的进程级副作用，失败时逆序回滚。
//
// buildServicesWithBudget 用独立栈登记模块资源，失败时 unwind，成功时交出清理函数。
// 仅用于 bootstrap 内部的装配与资源释放。
type rollbackStack struct {
	steps []rollbackStep
}

// push 登记一步已产生的副作用；nil 动作直接忽略。
func (r *rollbackStack) push(name string, fn func() error) {
	if fn == nil {
		return
	}
	r.steps = append(r.steps, rollbackStep{name: name, fn: fn})
}

// unwind 逆序执行全部回滚步骤：单步失败不阻断其余（否则第一个失败就让其后的
// 全部泄漏），错误聚合返回并指名失败步骤；执行后清空，重复调用不重复执行。
func (r *rollbackStack) unwind() error {
	var errs []error
	for _, v := range slices.Backward(r.steps) {
		if err := v.fn(); err != nil {
			errs = append(errs, fmt.Errorf("rollback %s: %w", v.name, err))
		}
	}
	r.steps = nil
	return errors.Join(errs...)
}

// discard 清空回滚栈而不执行任何步骤：成功路径专用。
func (r *rollbackStack) discard() {
	r.steps = nil
}

type providerCleanupPhase uint8

const (
	cleanupModules providerCleanupPhase = iota
	cleanupExtensions
	cleanupTelemetry
	cleanupMetrics
)

type providerCleanupStep struct {
	name  string
	phase providerCleanupPhase
	fn    func(context.Context) error
}

// providerCleanup 在构造成功处登记释放动作，并把同一份所有权交给 App。
// 构造与消费沿现有串行生命周期执行；App 的一次性守卫负责正常关闭准入。
type providerCleanup struct {
	steps []providerCleanupStep
}

func (c *providerCleanup) push(name string, phase providerCleanupPhase, fn func(context.Context) error) {
	if fn != nil {
		c.steps = append(c.steps, providerCleanupStep{name: name, phase: phase, fn: fn})
	}
}

// rollback 只撤销成功登记项，严格按取得顺序的逆序执行。
func (c *providerCleanup) rollback(ctx context.Context) error {
	if c == nil {
		return nil
	}
	steps := c.steps
	c.steps = nil
	var errs []error
	for _, step := range slices.Backward(steps) {
		errs = append(errs, closeResource(ctx, "rollback "+step.name, step.fn))
	}
	return errors.Join(errs...)
}

// close 沿既有阶段关闭，同阶段保持登记顺序。先消费记录，避免重入或重试重复释放。
func (c *providerCleanup) close(ctx context.Context) error {
	if c == nil {
		return nil
	}
	steps := c.steps
	c.steps = nil
	slices.SortStableFunc(steps, func(a, b providerCleanupStep) int {
		return cmp.Compare(a.phase, b.phase)
	})
	var errs []error
	for _, step := range steps {
		errs = append(errs, closeResource(ctx, step.name, step.fn))
	}
	return errors.Join(errs...)
}
