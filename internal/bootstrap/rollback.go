package bootstrap

import (
	"cmp"
	"context"
	"errors"
	"slices"
)

type providerCleanupPhase uint8

const (
	cleanupExtensions providerCleanupPhase = iota
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
