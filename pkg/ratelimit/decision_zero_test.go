package ratelimit

import "testing"

// TestZeroDecisionIsNotAllowed 钉住判定的零值落在拒绝方向（rate-limiting spec）。
//
// Go 的零值无处不在——结构体字面量漏填、切片扩容、map 取不到键都会产出它。
// 让 0 位是"放行"，意味着任何一次疏忽都朝不设防的方向失败，而这类疏忽在
// 编译期与测试里都不显形。
func TestZeroDecisionIsNotAllowed(t *testing.T) {
	t.Parallel()

	var zero Decision
	if zero.Allowed() {
		t.Fatal("Decision{}.Allowed() = true, want false: 未赋值的判定不得放行")
	}
	if zero.Outcome != OutcomeUnspecified {
		t.Fatalf("Decision{}.Outcome = %d, want OutcomeUnspecified (它必须占住零值)", zero.Outcome)
	}

	// map 取不到键、切片扩容都会走到这条路径，不只是显式的字面量。
	byPolicy := map[string]Decision{}
	if byPolicy["missing"].Allowed() {
		t.Fatal("map 取不到键得到的判定放行了")
	}
}

// TestOutcomeStringDistinguishesUnspecified 钉住未指定值在文本上可辨识。
//
// 回落到 "allowed" 会把证据抹掉：日志里看到 unspecified 才能定位到
// "有人返回了未赋值或越界的判定"。
func TestOutcomeStringDistinguishesUnspecified(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		outcome Outcome
		want    string
	}{
		{name: "零值", outcome: Outcome(0), want: "unspecified"},
		{name: "未指定", outcome: OutcomeUnspecified, want: "unspecified"},
		{name: "放行", outcome: OutcomeAllowed, want: "allowed"},
		{name: "限速", outcome: OutcomeRateLimited, want: "rate_limited"},
		{name: "键基数上限", outcome: OutcomeKeyBudgetExhausted, want: "key_budget_exhausted"},
		{name: "后端不可用", outcome: OutcomeBackendUnavailable, want: "backend_unavailable"},
		{name: "越界值", outcome: Outcome(200), want: "unspecified"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.outcome.String(); got != tt.want {
				t.Fatalf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}
