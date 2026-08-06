package bootstrap

import (
	"errors"
	"fmt"
)

// rollbackStep 是一步可回滚的副作用：name 用于错误指认，fn 执行回滚动作。
type rollbackStep struct {
	name string
	fn   func() error
}

// rollbackStack 收集装配过程中产生的进程级副作用，失败时逆序回滚。
//
// 仅供 newProviders 的失败路径使用：成功路径 discard——副作用的释放所有权
// 移交给 App 的关停序列；失败路径 unwind。不做成导出抽象，消费者只有一处。
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
	for i := len(r.steps) - 1; i >= 0; i-- {
		if err := r.steps[i].fn(); err != nil {
			errs = append(errs, fmt.Errorf("rollback %s: %w", r.steps[i].name, err))
		}
	}
	r.steps = nil
	return errors.Join(errs...)
}

// discard 清空回滚栈而不执行任何步骤：成功路径专用。
func (r *rollbackStack) discard() {
	r.steps = nil
}
