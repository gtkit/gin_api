package errx

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestPureJudgementsRejectMixedTrees 钉住两条判定的核心性质
// （runtime-state-machine spec）：聚合错误里混入真实故障时，判定必须为假。
//
// errors.Is 对聚合错误的任一成员命中即真，用它整体判定会让
// errors.Join(Canceled, realErr) 被当成"正常关停/被中止"，那个故障因此
// 从退出码、指标与告警里一起消失——本包存在的全部理由就是消灭这个形状。
func TestPureJudgementsRejectMixedTrees(t *testing.T) {
	t.Parallel()

	genuine := errors.New("disk full")
	tests := []struct {
		name          string
		err           error
		wantCancelled bool
		wantAbort     bool
	}{
		{name: "nil", err: nil},
		{name: "裸 Canceled", err: context.Canceled, wantCancelled: true, wantAbort: true},
		{name: "裸 DeadlineExceeded", err: context.DeadlineExceeded, wantAbort: true},
		{
			name: "包装链内的 Canceled", err: fmt.Errorf("run: %w", context.Canceled),
			wantCancelled: true, wantAbort: true,
		},
		{
			name: "全取消的 Join", err: errors.Join(context.Canceled, context.Canceled),
			wantCancelled: true, wantAbort: true,
		},
		{
			name: "取消与超时混合的 Join（对 abort 为真、对 cancel 为假）",
			err:  errors.Join(context.Canceled, context.DeadlineExceeded), wantAbort: true,
		},
		{name: "取消混入真实故障", err: errors.Join(context.Canceled, genuine)},
		{name: "超时混入真实故障", err: errors.Join(context.DeadlineExceeded, genuine)},
		{name: "外层包装的混合 Join", err: fmt.Errorf("x: %w", errors.Join(context.Canceled, genuine))},
		{name: "纯真实故障", err: genuine},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsPureCancellation(tt.err); got != tt.wantCancelled {
				t.Errorf("IsPureCancellation(%v) = %v, want %v", tt.err, got, tt.wantCancelled)
			}
			if got := IsPureAbort(tt.err); got != tt.wantAbort {
				t.Errorf("IsPureAbort(%v) = %v, want %v", tt.err, got, tt.wantAbort)
			}
		})
	}
}
