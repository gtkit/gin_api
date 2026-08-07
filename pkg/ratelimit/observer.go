package ratelimit

// Observer 接收限流器的运行期事件。
//
// **本包不写日志**，事件一律交出去。包内直接写日志会把使用者绑定到某一个
// 日志栈——而换用另一个库只是换个绑定对象，问题不变。交出去之后，包对
// "日志"这件事一无所知，使用者接入自己的栈即可。
//
// 事件都是**低基数**的：相位迁移与首次降级在一次故障期间各自只发生一次，
// 因此实现可以放心地每次都记录。这里绝不暴露每请求级别的事件——那会让
// 一次故障把调用方的日志刷满，而信息量只有第一条。
//
// 实现必须并发安全：事件从限流判定路径上同步发出，而那条路径会被多个
// 请求并发进入。实现也应当只做记录——它在持锁状态下被调用，阻塞会挡住
// 所有限流判定。
type Observer interface {
	// BackendPhaseChanged 在后端健康相位迁移时调用。
	//
	// 它是排查"限流为什么突然放宽或收紧"的第一手线索。
	BackendPhaseChanged(from, to HealthPhase)

	// FellBackToLocal 在首次切到本地影子桶时调用一次。
	//
	// 调用方需要知道的关键事实：算法模型一致，但**状态不连续**——
	// 后端里的计数不会迁移，本地桶从满桶起算，因此瞬时会多放行至多一个 Burst。
	FellBackToLocal(policyID string)
}

// nopObserver 是未注入 Observer 时的空实现。
//
// 用空实现而不是每处判 nil：判 nil 要散布到每个事件点，漏掉一处的表现是
// 限流判定路径上的空指针崩溃——那比这几行的代价大得多。
type nopObserver struct{}

func (nopObserver) BackendPhaseChanged(HealthPhase, HealthPhase) {}
func (nopObserver) FellBackToLocal(string)                       {}

// ObserverFunc 让一组函数满足 Observer，省去为一次性用途定义类型。
//
// 各字段可为 nil，此时对应事件被忽略。
type ObserverFunc struct {
	OnPhaseChanged    func(from, to HealthPhase)
	OnFellBackToLocal func(policyID string)
}

func (f ObserverFunc) BackendPhaseChanged(from, to HealthPhase) {
	if f.OnPhaseChanged != nil {
		f.OnPhaseChanged(from, to)
	}
}

func (f ObserverFunc) FellBackToLocal(policyID string) {
	if f.OnFellBackToLocal != nil {
		f.OnFellBackToLocal(policyID)
	}
}

// Option 配置后端的可选项。
//
// 用 Functional Options 而不是位置参数：键前缀与事件观察者都是可选的，
// 位置参数意味着每加一个可选项就变更一次签名——而这是对外发布的包，
// 签名变更是使用者的编译错误。
type Option func(*options)

type options struct {
	keyPrefix string
	observer  Observer
	// degradedStart 允许后端在启动期不可用时以 Degraded 相位构造成功，
	// 由恢复探测把它带回 Healthy（见 WithDegradedStart）。
	degradedStart bool
}

// newOptions 应用调用方给出的选项，并在此集中给出默认值。
//
// 默认值散落在各构造函数里的话，同一个选项在不同后端上可能有不同缺省，
// 而那种差异不会有任何提示。
func newOptions(opts []Option) options {
	resolved := options{observer: nopObserver{}}
	for _, apply := range opts {
		if apply != nil {
			apply(&resolved)
		}
	}
	if resolved.observer == nil {
		resolved.observer = nopObserver{}
	}
	return resolved
}

// WithKeyPrefix 设置限流键的前缀。
//
// 多个服务共用一个后端实例时必须设置：不设前缀时，两个服务里同名的策略会
// 共享同一个桶，于是各自的限额都不是配置里写的那个值。
func WithKeyPrefix(prefix string) Option {
	return func(o *options) { o.keyPrefix = prefix }
}

// WithDegradedStart 允许后端在启动期不可用时以 Degraded 相位构造成功。
//
// 供降级失败模式（fail_open / local_fallback）的调用方使用：那些模式的
// 全部意义是可用性优先，后端故障期间连进程都起不来与其语义自相矛盾。
// 构造期的失败（脚本装载、能力探测）转为 Degraded 初始相位，由既有的
// 恢复探测把后端带回 Healthy；期间的判定按调用方的失败模式处理。
// fail_closed 的调用方不应使用本选项——那种模式下后端不可用即拒绝服务，
// 启动失败是更诚实的表达。
func WithDegradedStart() Option {
	return func(o *options) { o.degradedStart = true }
}

// WithObserver 注入运行期事件的接收者。不注入时事件被丢弃，包不产生任何输出。
func WithObserver(observer Observer) Option {
	return func(o *options) { o.observer = observer }
}
