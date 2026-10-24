// Package breaker 是 sony/gobreaker/v2 的薄适配（outbound-http-resilience spec）。
//
// 只做四件事：具名实例（装配期登记、构造后冻结）、配置阈值翻译、失败分类、
// 状态变更接日志。刻意不自研状态机——熔断的难点全在状态迁移的边界语义上，
// 成熟库已把半开探测、并发计数这些坑蹚平。
//
// 配置门控语义：本包没有全局状态，不构造 Registry 即零开销；
// 与出站 HTTP 客户端正交组合——熔断包住"对某个具名下游的一次调用"，
// 不感知调用用的是什么客户端。
package breaker

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/gtkit/gin-api/internal/pkg/errx"

	"github.com/gtkit/logger/v2"
	"github.com/sony/gobreaker/v2"
)

// defaultConsecutiveFailures 与 gobreaker 自身的默认判据一致（连续失败 > 5）。
const defaultConsecutiveFailures = 5

// ErrUnknownDownstream 表示调用了未在装配期登记的下游标识。
//
// 不隐式建实例：按运行期传入的名字建实例会让注册表随请求内容无界增长，
// 而下游集合是**启动期事实**——拼错一个名字应当立刻报错，而不是悄悄
// 得到一个永远不会跳闸的独立熔断器。
var ErrUnknownDownstream = errors.New("breaker: downstream is not registered")

// Config 是单个熔断实例的阈值与失败判据。零值字段取保守默认。
type Config struct {
	// ConsecutiveFailures 连续失败达到该值即熔断；<=0 取 5（库默认同值）。
	ConsecutiveFailures int
	// OpenTimeout 是熔断后到半开探测的等待；<=0 取库默认 60s。
	OpenTimeout time.Duration
	// HalfOpenMaxRequests 是半开期放行的探测请求数；<=0 取 1（库默认同值）。
	HalfOpenMaxRequests int

	// IsFailure 判定一个错误是否计入熔断。为 nil 时用 DefaultIsFailure。
	//
	// 必须可注入：把调用方取消与业务错误（HTTP 4xx、"余额不足"这类正常
	// 业务结果）计进去，会让一次客户端批量取消或一批正常的参数错误触发熔断——
	// 把下游的健康判断建立在与下游健康无关的信号上。
	IsFailure func(error) bool
}

// DefaultIsFailure 是默认失败判据：**纯粹**的调用方取消与超时不计入熔断。
//
// 取消来自上游（客户端断开、请求超时、进程关停），它不说明下游有任何问题；
// 计进去会让一次流量抖动把健康的下游熔断掉。
//
// 用 errx.IsPureAbort 而不是 errors.Is：errors.Is 对聚合错误的**任一**成员
// 命中即真，于是 errors.Join(context.Canceled, realFailure) 会被整体判成
// "只是取消"，真实的下游故障因此从熔断统计里彻底消失——下游一直在坏，
// 熔断器却永远不跳。这个陷阱本仓已在 errx 里解决过一次，这里必须复用同一份
// 判定，而不是再写一遍 errors.Is。
func DefaultIsFailure(err error) bool {
	if err == nil {
		return false
	}
	return !errx.IsPureAbort(err)
}

// Registry 持有装配期登记的具名熔断实例，构造后不可变。
//
// 实例长存是必需的：熔断状态必须跨请求累计，每次调用新建实例的"熔断"
// 永远不会熔断。不可变则让注册表有界，且"这个进程保护哪些下游"是
// 启动期就能读出的事实。
type Registry struct {
	breakers  map[string]*gobreaker.CircuitBreaker[struct{}]
	isFailure func(error) bool
}

// NewRegistry 按下游名登记熔断实例。
//
// names 为空即没有任何下游受保护——这是合法的（本进程不出站），
// 但此后任何 Do 调用都会返回 ErrUnknownDownstream。
func NewRegistry(cfg Config, names ...string) *Registry {
	isFailure := cfg.IsFailure
	if isFailure == nil {
		isFailure = DefaultIsFailure
	}

	r := &Registry{
		breakers:  make(map[string]*gobreaker.CircuitBreaker[struct{}], len(names)),
		isFailure: isFailure,
	}
	for _, name := range names {
		if _, dup := r.breakers[name]; dup {
			continue
		}
		r.breakers[name] = gobreaker.NewCircuitBreaker[struct{}](r.settings(cfg, name))
	}
	return r
}

// Do 在名为 name 的熔断实例内执行 fn。
//
// 熔断开启时不执行 fn、立即返回熔断错误（用 IsOpen 判别）；
// 半开期超出探测配额的调用同样快速失败。
// name 未登记时返回 ErrUnknownDownstream，且**不执行 fn**——
// 一个拼错的名字若被放行，那次调用就完全没有熔断保护。
func (r *Registry) Do(name string, fn func() error) error {
	cb, ok := r.breakers[name]
	if !ok {
		return fmt.Errorf("%w: %q (register it at assembly time)", ErrUnknownDownstream, name)
	}
	_, err := cb.Execute(func() (struct{}, error) {
		return struct{}{}, fn()
	})
	return err
}

// Downstreams 返回已登记的下游名数量，供启动日志与诊断使用。
func (r *Registry) Downstreams() int { return len(r.breakers) }

// IsOpen 报告错误是否为熔断快速失败（开启态拒绝或半开配额已满）。
//
// 调用方据此把"下游被熔断"与"下游真实错误"分开处置：前者应立即降级，
// 重试它只会空转；后者按下游错误的语义处理。
func IsOpen(err error) bool {
	return errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests)
}

// settings 把配置翻译成库的设置。
func (r *Registry) settings(cfg Config, name string) gobreaker.Settings {
	threshold := boundedCount(cfg.ConsecutiveFailures, defaultConsecutiveFailures)
	return gobreaker.Settings{
		Name:    name,
		Timeout: cfg.OpenTimeout,
		// 0 交给库取它自己的默认（半开期放行 1 个探测请求）。
		MaxRequests: boundedCount(cfg.HalfOpenMaxRequests, 0),
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= threshold
		},
		// **必须用 IsExcluded 而不是 IsSuccessful 表达"不计入统计"。**
		//
		// IsSuccessful 返回 true 的语义是"这次算成功"，它会**清零**
		// ConsecutiveFailures。用它表达排除的后果是熔断实际失效：下游连续
		// 失败 4 次（阈值 5）之后夹进一次客户端取消，计数即归零，于是永远
		// 到不了阈值——保护看起来在，实际不在。已实测复现（见 breaker_test）。
		//
		// IsExcluded 才是"这次调用不进任何计数"：库内 onExclusion 只递增
		// TotalExclusions，不动连续失败数。IsSuccessful 留空用库默认
		// （err == nil 即成功），正是所需语义。
		//
		// err != nil 这一条不能省：库在 isSuccessful **之前**调用 isExcluded，
		// 且 nil error 也会走它。漏掉判断会把成功调用一并排除——连续失败
		// 计数从此永不清零，一个早已恢复的下游会在下次失败时立刻跳闸。
		IsExcluded: func(err error) bool { return err != nil && !r.isFailure(err) },
		OnStateChange: func(name string, from, to gobreaker.State) {
			// 状态变更是低基数事件，每次都记：它是"下游为什么突然全部快速失败/
			// 恢复"的第一手线索。
			logger.Warnf("circuit breaker %q state changed: %s -> %s", name, from, to)
		},
	}
}

// boundedCount 把配置的计数翻译成库要的 uint32。
//
// 显式校验范围而不是裸转换：int 在 64 位平台上装得下远超 uint32 的值，
// 裸转换会把一个"很大的阈值"回绕成一个很小的值——熔断因此在远低于
// 配置意图的失败数上跳闸，而配置文件看起来完全正常。
// 非正值取 fallback（0 表示交给库的默认）。
func boundedCount(v int, fallback uint32) uint32 {
	switch {
	case v <= 0:
		return fallback
	case v > math.MaxUint32:
		return math.MaxUint32
	default:
		return uint32(v)
	}
}
