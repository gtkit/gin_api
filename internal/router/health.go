package router

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	logger "github.com/gtkit/gin-api/internal/pkg/log"

	"golang.org/x/sync/errgroup"
)

const (
	// readyDepTimeout 是单个依赖检查的预算上限，实际取 min(它, 全局预算剩余)。
	readyDepTimeout = 2 * time.Second
	// readyGlobalDeadline 是整个 readiness 检查的**全局**预算。
	//
	// 没有它，依赖数量增加会让最坏耗时线性增长（旧实现的两项依赖是 map 字面量
	// 按序求值，串行执行、各 2s，最坏 4s），探针超时后编排器会误判实例不健康——
	// 一个由探针自身造成的故障。
	readyGlobalDeadline = 2 * time.Second
)

// 探针的请求级超时预算。
//
// 刻意比业务路由短得多：探针的意义是快速回答"我还行吗"，
// 一个要等好几秒才失败的探针会让编排器的判定同样迟钝，摘流也就跟着变慢。
const ()

// 状态标签的取值集合固定，不随错误种类变化。
//
// 失败原因走 DependencyCheck.Error 单独承载，不拼进标签——标签是编排器
// 用来判定的，取值一旦随错误文本浮动，消费者就只能改用前缀匹配。
const (
	statusOK          = "ok"
	statusSkipped     = "skipped"
	statusDegraded    = "degraded"
	statusUnavailable = "unavailable"
)

// maxDepErrorBytes 是响应体中单条依赖错误的字节上限。
//
// err.Error() 的长度不受本项目控制：驱动可能把整条 SQL、TLS 证书链或上游返回的
// HTML 错误页放进去。探针必须在故障期保持可用，而一个膨胀到 LB 拒绝解析的响应
// 恰好在最需要它的时候失效。256 字节足以容纳
// `dial tcp <addr>: connect: connection refused` 这类典型错误的完整信息。
const maxDepErrorBytes = 256

// depFailureLogInterval 是同一依赖同一错误类别的重复失败日志间隔。
//
// 依赖持续故障时探针每秒都在跑，不限频就会用同一条消息淹没日志，
// 把真正的新信息埋掉。首次立即打印，之后最多每 30s 一次；
// 错误类别变化立即打印；恢复时立即打印一次并重置。
const depFailureLogInterval = 30 * time.Second

// HealthChecker 定义健康探针需要的外部依赖检查能力。
// bootstrap 层负责把 DB / Redis 的 Ping 注入进来。
type HealthChecker interface {
	PingDB(ctx context.Context) error
	PingRedis(ctx context.Context) error
}

// ProcessStatus 提供进程自身的就绪信息（相位与能力状态）。
//
// readiness 不只是 DB/Redis 的存活：一个正在 draining 的实例、
// 一个 required worker 已经异常退出的实例，依赖可能全都健康，
// 但它已经不该再接流量。
type ProcessStatus interface {
	// Draining 报告进程是否已进入优雅关停。
	Draining() bool
	// DegradedCapabilities 返回已降级的可选能力名（低基数，稳定排序）。
	DegradedCapabilities() []string
	// FailedRequirements 返回已失效的必需能力名（低基数，稳定排序）。
	FailedRequirements() []string
}

// dependency 是一次 readiness 检查中的单个依赖。
type dependency struct {
	name string
	// required 决定它的失败如何影响整体结论：required 失败 → 503；
	// optional 失败 → 200 且 body 标 degraded。
	required bool
	ping     func(context.Context) error
}

// LivenessResponse 是存活探针的响应体。
//
// 用具名类型而不是 gin.H：探针的响应形状是编排器的契约。用 map 的话，
// 字段名散落在各个构造点，改一次要靠人找全所有位置，漏一处就是探针
// 在某条路径上返回了另一种形状。
type LivenessResponse struct {
	Status string `json:"status"`
}

// DependencyCheck 是单个依赖在响应体中的呈现。
//
// 用结构体而不是把错误拼进状态串（`"unavailable: dial tcp ..."`）：拼接会让
// 消费者用前缀匹配判断状态，状态判定从此依赖错误文本的措辞。分成两个字段后，
// Status 保持固定取值集合，Error 是独立的附加信息。
type DependencyCheck struct {
	Status string `json:"status"`
	// Error 是检查失败的原因，仅失败时出现，长度受 maxDepErrorBytes 约束。
	//
	// **该字段会携带内网拓扑与账号信息**（典型如
	// `Access denied for user 'appuser'@'10.0.3.12'`）。这是为缩短排障路径
	// 作出的取舍，代价是 /ready 不得对公网开放——见 SCAFFOLD.md 的说明。
	Error string `json:"error,omitempty"`
}

// ReadinessResponse 是就绪探针的响应体。
type ReadinessResponse struct {
	Status string `json:"status"`
	// Checks 是各依赖的状态与失败原因，键为依赖名。
	Checks map[string]DependencyCheck `json:"checks,omitempty"`
	// Phase 仅在关停期出现。
	Phase string `json:"phase,omitempty"`
	// Failed 是导致不可用的进程能力。
	Failed []string `json:"failed,omitempty"`
	// Degraded 是已降级但不影响接流量的能力。
	Degraded []string `json:"degraded,omitempty"`
}

func healthHandler(c *gin.Context) {
	// liveness 在 draining 期间**继续返回 200**，直到进程真正退出。
	// 否则编排器会在优雅关停中途判定进程已死并 kill 它，在飞请求被直接截断——
	// 优雅关停的全部工作因此白做。
	c.JSON(http.StatusOK, LivenessResponse{Status: statusOK})
}

// readyHandler 按四态模型回答就绪性。
//
//	required 全通    → 200 ok
//	optional 有故障  → 200 degraded（实例仍可接流量，只是部分能力不可用）
//	required 故障    → 503 unavailable
//	draining         → 503 unavailable（且不再执行依赖检查）
func readyHandler(checker HealthChecker, status ProcessStatus, redisRequired bool) gin.HandlerFunc {
	limiter := newDepFailureLogLimiter()

	return func(c *gin.Context) {
		// fail-closed：未配置 checker 属装配错误，报"未就绪"而非"就绪"，
		// 避免错误装配的实例仍被纳入流量。
		if checker == nil {
			logger.Warn("readiness checker not configured; reporting not ready")
			c.JSON(http.StatusServiceUnavailable, ReadinessResponse{Status: statusUnavailable})
			return
		}

		// draining 优先短路：此时结论已经确定，再去 ping 依赖只是徒增关停期的负载。
		if status != nil && status.Draining() {
			c.JSON(http.StatusServiceUnavailable, ReadinessResponse{
				Status: statusUnavailable,
				Phase:  "draining",
			})
			return
		}

		deps := []dependency{
			{name: "database", required: true, ping: checker.PingDB},
			{name: "redis", required: redisRequired, ping: checker.PingRedis},
		}
		if configured, ok := checker.(interface{ RedisConfigured() bool }); ok && !configured.RedisConfigured() && !redisRequired {
			deps[1].ping = nil
		}

		results, anyRequiredFailed, anyOptionalFailed := runDependencyChecks(
			c.Request.Context(), deps, limiter)

		body := ReadinessResponse{Checks: results}

		if status != nil {
			if failed := status.FailedRequirements(); len(failed) > 0 {
				anyRequiredFailed = true
				body.Failed = failed
			}
			if degraded := status.DegradedCapabilities(); len(degraded) > 0 {
				anyOptionalFailed = true
				body.Degraded = degraded
			}
		}

		switch {
		case anyRequiredFailed:
			body.Status = statusUnavailable
			c.JSON(http.StatusServiceUnavailable, body)
		case anyOptionalFailed:
			// 200 而不是 503：可选能力故障不该让整个实例被摘流，
			// 但必须在 body 里说清楚，否则"部分能力已失效"完全不可见。
			body.Status = statusDegraded
			c.JSON(http.StatusOK, body)
		default:
			body.Status = statusOK
			c.JSON(http.StatusOK, body)
		}
	}
}

// depOutcome 是单个依赖的检查结论。
type depOutcome struct {
	name     string
	status   string
	detail   string
	required bool
	failed   bool
}

// truncateDepError 把错误文本收敛到 maxDepErrorBytes 以内。
//
// 按字节而不是按字符截断，可能切断多字节字符的中间。这里可接受：错误串是给人读的
// 排障线索，不是需要精确往返的数据；且 encoding/json 会把非法 UTF-8 字节编码为
// U+FFFD 而不是报错，响应因此仍然可解析——而"响应可解析"正是设上限要保住的东西。
func truncateDepError(err error) string {
	if err == nil {
		return ""
	}
	detail := err.Error()
	if len(detail) <= maxDepErrorBytes {
		return detail
	}
	return detail[:maxDepErrorBytes]
}

// runDependencyChecks 在全局预算内**并发**执行全部依赖检查。
//
// 并发而不是串行：串行时最坏耗时随依赖数量线性增长，很快就会超过探针超时，
// 于是探针本身成了故障源。单项预算取 min(单项上限, 全局预算剩余)，
// 保证任何一项都不能吃掉别人的预算。
//
// 结果按依赖名**稳定排序**收集：JSON 字段顺序抖动会干扰比对与告警去重。
func runDependencyChecks(
	parent context.Context, deps []dependency, limiter *depFailureLogLimiter,
) (map[string]DependencyCheck, bool, bool) {
	ctx, cancel := context.WithTimeout(parent, readyGlobalDeadline)
	defer cancel()

	outcomes := make([]depOutcome, len(deps))
	group, groupCtx := errgroup.WithContext(ctx)

	for i, dep := range deps {
		group.Go(func() error {
			if dep.ping == nil {
				outcomes[i] = depOutcome{name: dep.name, status: statusSkipped}
				return nil
			}
			depCtx, cancelDep := boundedTimeout(groupCtx, readyDepTimeout)
			defer cancelDep()

			err := dep.ping(depCtx)
			limiter.observe(dep.name, err)

			switch {
			case err == nil:
				outcomes[i] = depOutcome{name: dep.name, status: statusOK, required: dep.required}
			case !dep.required:
				// 可选依赖的失败不是"不可用"，而是"这台实例少了一项能力"。
				outcomes[i] = depOutcome{
					name: dep.name, status: statusDegraded,
					detail: truncateDepError(err), failed: true,
				}
			default:
				outcomes[i] = depOutcome{
					name: dep.name, status: statusUnavailable,
					detail: truncateDepError(err), required: true, failed: true,
				}
			}
			return nil
		})
	}
	// 各子任务恒返回 nil：一个依赖失败不该取消其余依赖的检查——
	// 那会让 readiness 的输出取决于哪个依赖先失败。
	_ = group.Wait()

	slices.SortFunc(outcomes, func(a, b depOutcome) int { return cmp.Compare(a.name, b.name) })

	results := make(map[string]DependencyCheck, len(outcomes))
	var anyRequiredFailed, anyOptionalFailed bool
	for _, o := range outcomes {
		results[o.name] = DependencyCheck{Status: o.status, Error: o.detail}
		if !o.failed {
			continue
		}
		if o.required {
			anyRequiredFailed = true
		} else {
			anyOptionalFailed = true
		}
	}

	return results, anyRequiredFailed, anyOptionalFailed
}

// boundedTimeout 返回 min(limit, 父 ctx 剩余) 的子 ctx。
func boundedTimeout(parent context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	if deadline, ok := parent.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < limit {
			limit = remaining
		}
	}
	if limit <= 0 {
		return context.WithDeadline(parent, time.Now().Add(-time.Nanosecond))
	}
	return context.WithTimeout(parent, limit)
}

// depFailureLogLimiter 对重复的依赖失败日志限频。
//
// 状态表的 key 是**依赖的固定名称**，绝不是原始错误文本——后者会让表随
// 故障种类无限增长，正是高基数问题的典型形态。错误文本只用于判断
// "是否仍是同一类失败"，不作为 key 存储。
type depFailureLogLimiter struct {
	mu    sync.Mutex
	state map[string]*depFailureState
	now   func() time.Time
}

type depFailureState struct {
	lastCategory string
	lastLoggedAt time.Time
}

func newDepFailureLogLimiter() *depFailureLogLimiter {
	return &depFailureLogLimiter{state: map[string]*depFailureState{}, now: time.Now}
}

// observe 记录一次检查结果，并按限频规则决定是否打印。
//
// 规则：首次失败立即打印；持续失败期间最多每 depFailureLogInterval 打印一次；
// 错误类别变化立即打印；恢复时立即打印一次并重置状态。
func (l *depFailureLogLimiter) observe(name string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	st, ok := l.state[name]
	if !ok {
		st = &depFailureState{}
		l.state[name] = st
	}

	if err == nil {
		if st.lastCategory != "" {
			logger.Info("readiness dependency recovered", logger.String("dependency", name))
			st.lastCategory = ""
			st.lastLoggedAt = time.Time{}
		}
		return
	}

	category := err.Error()
	now := l.now()
	if st.lastCategory == category && now.Sub(st.lastLoggedAt) < depFailureLogInterval {
		return
	}

	logger.Error("readiness dependency check failed",
		logger.String("dependency", name), logger.Err(err))
	st.lastCategory = category
	st.lastLoggedAt = now
}
