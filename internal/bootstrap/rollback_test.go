package bootstrap

import (
	"errors"
	"strings"
	"testing"
)

// TestRollbackStackUnwindsInReverseAndAggregates 钉住回滚栈的核心契约：
// 逆序执行、单步失败不阻断其余、错误聚合并指名失败步骤、执行后清空。
func TestRollbackStackUnwindsInReverseAndAggregates(t *testing.T) {
	var order []string
	rb := &rollbackStack{}
	rb.push("first", func() error { order = append(order, "first"); return nil })
	rb.push("second", func() error { order = append(order, "second"); return errors.New("boom") })
	rb.push("third", func() error { order = append(order, "third"); return nil })

	err := rb.unwind()
	if err == nil {
		t.Fatal("unwind swallowed a step failure")
	}
	if !strings.Contains(err.Error(), "rollback second") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error %q does not name the failing step and its cause", err)
	}
	if want := []string{"third", "second", "first"}; len(order) != 3 ||
		order[0] != want[0] || order[1] != want[1] || order[2] != want[2] {
		t.Fatalf("unwind order = %v, want %v (reverse of push order)", order, want)
	}

	// 执行后清空：再次 unwind 不重复执行任何步骤。
	order = nil
	if err := rb.unwind(); err != nil {
		t.Fatalf("second unwind = %v, want nil", err)
	}
	if len(order) != 0 {
		t.Fatalf("second unwind re-executed steps: %v", order)
	}
}

// TestRollbackStackDiscardPreventsUnwind 钉住成功路径语义：
// discard 之后 unwind 不执行任何步骤——副作用的所有权已移交关停序列。
func TestRollbackStackDiscardPreventsUnwind(t *testing.T) {
	var calls int
	rb := &rollbackStack{}
	rb.push("step", func() error { calls++; return nil })
	rb.push("", nil) // nil 步骤直接忽略

	rb.discard()
	if err := rb.unwind(); err != nil {
		t.Fatalf("unwind after discard = %v, want nil", err)
	}
	if calls != 0 {
		t.Fatalf("unwind after discard executed %d step(s), want 0", calls)
	}
}
