// Package breaker 是 sony/gobreaker/v2 的薄适配（outbound-http-resilience spec）。
//
// 只做三件事：具名实例（同名复用）、配置阈值翻译、状态变更接日志。
// 刻意不自研状态机——熔断的难点全在状态迁移的边界语义上，成熟库已把
// 半开探测、并发计数这些坑蹚平。
//
// 配置门控语义：本包没有全局状态，不构造 Registry 即零开销；
// 与出站 HTTP 客户端正交组合——熔断包住"对某个具名下游的一次调用"，
// 不感知调用用的是什么客户端。
package breaker

import (
	"errors"
	"math"
	"sync"
	"time"

	logger "github.com/gtkit/logger/v2"
	"github.com/sony/gobreaker/v2"
)

// defaultConsecutiveFailures 与 gobreaker 自身的默认判据一致（连续失败 > 5）。
const defaultConsecutiveFailures = 5

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

// Config 是单个熔断实例的阈值。零值字段取保守默认。
type Config struct {
	// ConsecutiveFailures 连续失败达到该值即熔断；<=0 取 5（库默认同值）。
	ConsecutiveFailures int
	// OpenTimeout 是熔断后到半开探测的等待；<=0 取库默认 60s。
	OpenTimeout time.Duration
	// HalfOpenMaxRequests 是半开期放行的探测请求数；<=0 取 1（库默认同值）。
	HalfOpenMaxRequests int
}

// Registry 按下游名持有熔断实例，同名返回同实例。
//
// 并发安全；实例一经创建即长存——熔断状态必须跨请求累计，
// 每次调用新建实例的"熔断"永远不会熔断。
type Registry struct {
	defaults Config

	mu       sync.Mutex
	breakers map[string]*gobreaker.CircuitBreaker[struct{}]

	// onStateChange 是状态变更的观察缝；默认写日志，测试可注入。
	onStateChange func(name string, from, to gobreaker.State)
}

// NewRegistry 构造注册表；defaults 是未单独配置的下游共用的阈值。
func NewRegistry(defaults Config) *Registry {
	return &Registry{
		defaults: defaults,
		breakers: make(map[string]*gobreaker.CircuitBreaker[struct{}]),
		onStateChange: func(name string, from, to gobreaker.State) {
			// 状态变更是低基数事件，每次都记：它是"下游为什么突然全部快速失败/
			// 恢复"的第一手线索。
			logger.Warnf("circuit breaker %q state changed: %s -> %s", name, from, to)
		},
	}
}

// Do 在名为 name 的熔断实例内执行 fn。
//
// 熔断开启时不执行 fn、立即返回熔断错误（用 IsOpen 判别）；
// 半开期超出探测配额的调用同样快速失败。
func (r *Registry) Do(name string, fn func() error) error {
	_, err := r.instance(name).Execute(func() (struct{}, error) {
		return struct{}{}, fn()
	})
	return err
}

// IsOpen 报告错误是否为熔断快速失败（开启态拒绝或半开配额已满）。
//
// 调用方据此把"下游被熔断"与"下游真实错误"分开处置：前者应立即降级，
// 重试它只会空转；后者按下游错误的语义处理。
func IsOpen(err error) bool {
	return errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests)
}

// instance 取或建具名实例。
func (r *Registry) instance(name string) *gobreaker.CircuitBreaker[struct{}] {
	r.mu.Lock()
	defer r.mu.Unlock()

	if cb, ok := r.breakers[name]; ok {
		return cb
	}

	cfg := r.defaults
	settings := gobreaker.Settings{
		Name:    name,
		Timeout: cfg.OpenTimeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= boundedCount(cfg.ConsecutiveFailures, defaultConsecutiveFailures)
		},
		OnStateChange: r.onStateChange,
	}
	// 0 交给库取它自己的默认（半开期放行 1 个探测请求）。
	settings.MaxRequests = boundedCount(cfg.HalfOpenMaxRequests, 0)

	cb := gobreaker.NewCircuitBreaker[struct{}](settings)
	r.breakers[name] = cb
	return cb
}
