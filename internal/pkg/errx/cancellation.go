// Package errx 提供跨层复用的错误判定。
//
// 独立成包是为了让 bootstrap 与 internal/pkg 下的组件（singleton 等）
// 共用同一份判定——同一个缺陷模式在多处各写一遍，就会在其中几处写错。
// 本包不依赖仓库内任何其他包。
package errx

import (
	"context"
	"errors"
)

// IsPureCancellation 报告非 nil 错误树的**全部**分支是否都归结为 context.Canceled。
//
// 用于把"因关停请求而中止"归一化为信号语义（零退出码）：errors.Is 对聚合
// 错误的任一成员命中即真，用它整体判定会把"取消 + 回滚失败"当成正常退出，
// 回滚失败随退出码 0 静默消失。
//
// deadline 类错误不算取消——预算耗尽是失败，不是关停请求。
func IsPureCancellation(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		members := joined.Unwrap()
		if len(members) == 0 {
			return false
		}
		for _, member := range members {
			if !IsPureCancellation(member) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		if inner := wrapped.Unwrap(); inner != nil {
			return IsPureCancellation(inner)
		}
	}
	return errors.Is(err, context.Canceled)
}

// IsPureAbort 报告错误树的**全部**分支是否都归结为"被中止"
// （context.Canceled 或 context.DeadlineExceeded）。
//
// 比 IsPureCancellation 宽一档，供那些把取消与超时同等看待的判定点使用
// （如单实例执行器：两者都不算任务失败，因为原因是关停或失去租约）。
// 同样必须逐成员判定：混入真实故障的聚合错误若被整体判成"中止"，
// 那个故障就从指标与告警里消失了。
func IsPureAbort(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		members := joined.Unwrap()
		if len(members) == 0 {
			return false
		}
		for _, member := range members {
			if !IsPureAbort(member) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		if inner := wrapped.Unwrap(); inner != nil {
			return IsPureAbort(inner)
		}
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// IsPureDeadline 报告错误树的**全部**分支是否都归结为 context.DeadlineExceeded。
//
// 与 IsPureCancellation 对称，供需要把"预算耗尽"与"调用方断开"分开处置的
// 判定点使用（终态中间件：超时回 504，断开一个字节都不写）。
//
// 同样必须逐成员判定：`errors.Is(errors.Join(DeadlineExceeded, dbFailure), …)`
// 整体判定为真，于是一次真实的下游故障被表达成"这次超时了"，
// 客户端据此重试，而真正的原因从日志与指标里消失。
func IsPureDeadline(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		members := joined.Unwrap()
		if len(members) == 0 {
			return false
		}
		for _, member := range members {
			if !IsPureDeadline(member) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		if inner := wrapped.Unwrap(); inner != nil {
			return IsPureDeadline(inner)
		}
	}
	return errors.Is(err, context.DeadlineExceeded)
}
