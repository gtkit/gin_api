package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
)

// WorkerKind 决定 worker 在**关停排序**中的位置。
//
// 零值非法：类别必须显式声明。若零值被解释成某个具体类别，
// 忘记声明的 worker 就会静默落进那一类，而它错的方向恰恰最难发现——
// 一个本该最先停的 producer 被当成 consumer，会在 HTTP 关停期间继续生产新任务。
type WorkerKind uint8

const (
	// KindUnspecified 是零值，注册期即拒绝。
	KindUnspecified WorkerKind = iota
	// KindBootstrap 是启动期/一次性任务，不参与 producer-consumer 排序，
	// 只服从自身 ctx 与 Cleanup。
	KindBootstrap
	// KindProducer 是任务生产者（cron/scheduler），关停时**最先**停。
	KindProducer
	// KindConsumer 是任务消费者（队列/outbox），关停时在 HTTP 之后 drain。
	KindConsumer
)

// String 返回稳定的小写类别名（低基数，可直接作日志字段）。
func (k WorkerKind) String() string {
	switch k {
	case KindBootstrap:
		return "bootstrap"
	case KindProducer:
		return "producer"
	case KindConsumer:
		return "consumer"
	case KindUnspecified:
		return "unspecified"
	default:
		return "unspecified"
	}
}

// Lifecycle 声明 worker 的运行形态。
//
// 零值非法。这个声明**必须由 worker 自己给出**，不能由管理器推断：
// 管理器唯一能观察到的信号是"Run 返回了"，而"长期任务意外退出"与
// "一次性任务正常完成"在这个信号上完全一样。推断的结果必然是二者之一被静默误判——
// 误判长期 worker 的代价是进程继续对外服务、功能却已经丢了。
type Lifecycle uint8

const (
	// LifecycleUnspecified 是零值，注册期即拒绝。
	LifecycleUnspecified Lifecycle = iota
	// LifecycleLongRunning 表示 Run 应阻塞直到 ctx 取消。
	// 在 ctx 取消之前返回（**包括返回 nil**）一律视为 ErrUnexpectedExit。
	LifecycleLongRunning
	// LifecycleOneShot 表示 Run 执行一次任务后正常返回 nil。
	LifecycleOneShot
)

// String 返回稳定的小写生命周期名。
func (l Lifecycle) String() string {
	switch l {
	case LifecycleLongRunning:
		return "long-running"
	case LifecycleOneShot:
		return "one-shot"
	case LifecycleUnspecified:
		return "unspecified"
	default:
		return "unspecified"
	}
}

// Worker represents a managed background task.
//
// 两阶段（Prepare / Run）而不是一个 Ready channel：Prepare 返回即代表就绪，
// 错误直接是返回值。channel 方案还要额外约定关闭时序，并处理
// "未就绪就退出"的竞态——多出来的复杂度没有换到任何能力。
type Worker interface {
	// Name 返回低基数的稳定标识，用于日志与错误上下文。
	Name() string
	// Kind 决定关停排序中的位置。
	Kind() WorkerKind
	// Required 为 true 时：Prepare 失败即启动失败，运行期异常触发全进程优雅关停并非零退出。
	Required() bool
	// Lifecycle 声明运行形态，见 Lifecycle 的说明。
	Lifecycle() Lifecycle
	// Prepare 做资源获取与前置校验（建连、抢锁、检查前置条件），返回即代表可执行。
	// LongRunning 的启动期失败必须在此暴露；OneShot 的任务执行结果仍由 Run 返回。
	Prepare(ctx context.Context) error
	// Run 阻塞运行直到 ctx 取消（LongRunning），或执行一次任务后返回（OneShot）。
	Run(ctx context.Context) error
	// Cleanup 释放 Prepare 获得的资源，**必须幂等**。
	//
	// 契约：Prepare 成功后，管理器保证 Cleanup 最终**恰好被调用一次**。
	// 三条调用路径——启动回滚（其他 worker 准备失败/启动被取消，按逆序）、
	// 正常关停（Run 返回后）、OneShot 完成后。
	//
	// 只在"不会进入 Run"时调用是不够的：那样正常 Run 结束后，
	// Prepare 建立的连接与持有的锁没有任何人释放，属真实泄漏。
	Cleanup(ctx context.Context) error
}

var (
	// ErrUnexpectedExit 表示长期 worker 在 ctx 取消之前返回。
	//
	// **包括返回 nil**。旧实现只在 err != nil 时上报，于是长期 worker
	// 在 ctx 未取消时返回 nil 会静默消失：进程继续对外提供服务，
	// 而它负责的功能已经不工作了，也没有任何信号。
	ErrUnexpectedExit = errors.New("worker exited before its context was canceled")

	// ErrWorkerContractViolation 表示 worker 的声明本身非法（零值类别/生命周期、非法组合）。
	ErrWorkerContractViolation = errors.New("worker contract violation")
)

// WorkerPanicError 携带 panic 值、worker 名与栈。
//
// panic 不直接崩进程：那与 optional worker 的契约冲突——一个可选能力的
// 编程错误不该拖垮整个服务。也不能只记录后继续把该 worker 当作健康：
// 它已经不在跑了。定稿是转成普通运行期异常，再走与之完全相同的 Required 语义。
type WorkerPanicError struct {
	Worker string
	Value  any
	Stack  []byte
}

func (e *WorkerPanicError) Error() string {
	return fmt.Sprintf("worker %s panicked: %v", e.Worker, e.Value)
}

// recoverWorkerPanic 把 panic 转成 WorkerPanicError。
//
// 栈必须在 recover 的同一层抓取：延后到别处再取，拿到的是恢复之后的栈，
// 而那恰好丢掉了唯一有用的信息。
func recoverWorkerPanic(name string, recovered any) *WorkerPanicError {
	return &WorkerPanicError{Worker: name, Value: recovered, Stack: debug.Stack()}
}

// validateWorkerContract 校验 worker 的声明自洽。
//
// 在**注册期**一次性校验，而不是等到运行期：非法组合是编码错误，
// 它在启动时是零成本可判的，留到运行期就只能靠观察行为反推。
func validateWorkerContract(w Worker) error {
	if w == nil {
		return fmt.Errorf("%w: nil worker", ErrWorkerContractViolation)
	}

	name := w.Name()
	if name == "" {
		return fmt.Errorf("%w: worker name must not be empty", ErrWorkerContractViolation)
	}

	kind, lifecycle := w.Kind(), w.Lifecycle()

	var errs []error
	if kind == KindUnspecified {
		errs = append(errs, fmt.Errorf(
			"%w: worker %q must declare a kind (bootstrap|producer|consumer)", ErrWorkerContractViolation, name))
	}
	if lifecycle == LifecycleUnspecified {
		errs = append(errs, fmt.Errorf(
			"%w: worker %q must declare a lifecycle (long-running|one-shot)", ErrWorkerContractViolation, name))
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	// 组合约束：
	//   OneShot 只能是 Bootstrap —— 一次性任务不参与 producer/consumer 的排序语义；
	//   LongRunning 只能是 Producer 或 Consumer —— 关停顺序必须能确定它排在哪一相。
	switch lifecycle {
	case LifecycleOneShot:
		if kind != KindBootstrap {
			return fmt.Errorf(
				"%w: worker %q is one-shot and therefore must be of kind bootstrap, got %s",
				ErrWorkerContractViolation, name, kind)
		}
	case LifecycleLongRunning:
		if kind != KindProducer && kind != KindConsumer {
			return fmt.Errorf(
				"%w: worker %q is long-running and therefore must be producer or consumer, got %s",
				ErrWorkerContractViolation, name, kind)
		}
	case LifecycleUnspecified:
		// 已在上面拒绝。
	}

	return nil
}
