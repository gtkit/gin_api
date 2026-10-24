// Package metrics 定义进程的指标仪表。
//
// # 为什么只依赖 OTel 的 API 包
//
// 本包 import 的是 `go.opentelemetry.io/otel/metric`（API），不是 SDK，
// 也不带任何 exporter。默认走 OTel 内置的 no-op 实现：不配置导出时，
// 每一次记录都是空操作——没有后台协程、没有持续增长的内存、没有网络。
//
// 不预置 exporter 是刻意的：Prometheus 拉取、OTLP 推送、云厂商专有协议的
// 选择取决于使用者既有的监控栈，母版选任何一个都会让另外几家先拆掉再接。
// 接线只需要在装配处一行 `otel.SetMeterProvider(...)`，本包的全部记录点
// 立即开始产出数据，无需改动其中任何一处。
//
// # 标签基数是硬约束
//
// 时间序列的数量是标签取值的笛卡尔积。把原始 URL、用户 ID、IP 或订单号
// 写进标签，序列数会随流量无界增长——那是把监控系统打垮的最省事方式，
// 而且故障出现在监控侧：最需要它的时候它正好不可用。
//
// 因此本包**不提供**接受任意标签的记录接口。每个记录函数的签名就是它允许的
// 标签集合，取值范围要么是有限枚举，要么是路由模板这类由代码决定的值。
package metrics

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gtkit/gin-api/internal/pkg/errx"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

// scopeName 是本进程仪表的作用域名。
const scopeName = "github.com/gtkit/gin-api"

// requestDurationBuckets 是 HTTP 时延直方图的边界（秒）。
//
// 显式给边界而不是用 SDK 默认：默认边界面向通用场景，最小的桶通常是 5ms，
// 而一个正常的 API 读接口大多落在 1~20ms——那样几乎所有请求都挤在头两个桶里，
// P50、P90 全都退化成"小于 5ms"，直方图等于没有分辨率。
//
// 这里的边界在 1ms~10s 之间按大致对数分布，覆盖从内存命中到超时的全跨度。
var requestDurationBuckets = []float64{
	0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
}

// UnmatchedRoute 是未匹配到任何路由时的路由标签。
//
// 未匹配请求必须共用这一个取值。用原始路径的话，每个扫描器请求都会创建一条
// 新的时间序列——一次目录爆破就能把监控后端撑爆，而正常业务指标会跟着一起
// 不可用。
const UnmatchedRoute = "unmatched"

// Recorder 持有全部仪表。
//
// 一次性创建、全程复用：仪表创建要走 provider 的注册路径并可能加锁，
// 放在请求路径上创建会让每个请求都付这份成本。
type Recorder struct {
	requestDuration metric.Float64Histogram
	requestsTotal   metric.Int64Counter

	rateLimitDecisions metric.Int64Counter

	workerExecutions metric.Int64Counter
	workerDuration   metric.Float64Histogram

	authEvents metric.Int64Counter

	// meter 留着供后续注册 DB 连接池的异步回调。
	meter metric.Meter
}

// New 创建仪表集合。
//
// meter 为 nil 时使用 no-op：那让调用方不必在装配路径上写判空，
// 也让"没接导出"成为一条正常路径而不是需要特殊处理的例外。
func New(meter metric.Meter) (*Recorder, error) {
	if meter == nil {
		meter = noop.NewMeterProvider().Meter(scopeName)
	}

	var errs []error
	// 逐个创建并收集错误，而不是遇到第一个就返回：仪表名不合法是启动期
	// 问题，一次报出全部远好过修一个重启一次。
	instrument := func(err error) { errs = append(errs, err) }

	requestDuration, err := meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of inbound HTTP requests"),
		metric.WithExplicitBucketBoundaries(requestDurationBuckets...),
	)
	instrument(err)

	requestsTotal, err := meter.Int64Counter(
		"http.server.requests",
		metric.WithDescription("Count of inbound HTTP requests"),
	)
	instrument(err)

	rateLimitDecisions, err := meter.Int64Counter(
		"ratelimit.decisions",
		metric.WithDescription("Rate limit decisions by policy and outcome"),
	)
	instrument(err)

	workerExecutions, err := meter.Int64Counter("worker.executions",
		metric.WithDescription("Actual worker task invocations by outcome"))
	instrument(err)
	workerDuration, err := meter.Float64Histogram("worker.execution.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of an actual worker task invocation"),
		metric.WithExplicitBucketBoundaries(0.001, 0.01, 0.1, 1, 5, 10, 30, 60, 300, 900, 3600))
	instrument(err)

	authEvents, err := meter.Int64Counter(
		"auth.events",
		metric.WithDescription("Authentication events by kind"),
	)
	instrument(err)

	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("metrics: build instruments: %w", err)
	}

	return &Recorder{
		requestDuration:    requestDuration,
		requestsTotal:      requestsTotal,
		rateLimitDecisions: rateLimitDecisions,
		workerExecutions:   workerExecutions,
		workerDuration:     workerDuration,
		authEvents:         authEvents,
		meter:              meter,
	}, nil
}

// RecordRequest 记录一次 HTTP 请求。
//
// route 必须是**路由模板**（如 `/api/v1/user`），不是实际路径。未匹配到路由时
// 传 UnmatchedRoute。这个参数是本包最容易被误用的地方，因此调用方只有中间件
// 一处，且它从 gin 的 FullPath() 取值。
func (r *Recorder) RecordRequest(ctx context.Context, method, route string, status int, elapsed time.Duration) {
	if r == nil {
		return
	}
	// Methods are caller-controlled even on unmatched routes. Keep the label
	// bounded using the HTTP semantic convention's known method set.
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace, http.MethodPatch, "QUERY":
	default:
		method = "_OTHER"
	}

	attrs := metric.WithAttributes(
		attribute.String("http.request.method", method),
		attribute.String("http.route", route),
		// 状态码用整数而不是字符串：取值天然有限（几十个），
		// 且数值形式让"5xx 占比"这类查询可以按范围写。
		attribute.Int("http.response.status_code", status),
	)
	r.requestDuration.Record(ctx, elapsed.Seconds(), attrs)
	r.requestsTotal.Add(ctx, 1, attrs)
}

// RateLimitOutcome 是限流判定的结果，取值有限。
type RateLimitOutcome string

const (
	// RateLimitAllowed 表示放行。
	RateLimitAllowed RateLimitOutcome = "allowed"
	// RateLimitThrottled 表示因超出速率被拒。
	RateLimitThrottled RateLimitOutcome = "throttled"
	// RateLimitKeyLimit 表示因键基数达上限被拒。
	//
	// 与 throttled 分开：前者说明流量确实超了，后者说明限流器自身的容量
	// 到顶了——两者的处置完全不同（调限额 vs 调 max_keys），合并计数会让
	// 运维看到一个数字却不知道该动哪个旋钮。
	RateLimitKeyLimit RateLimitOutcome = "key_limit"
	// RateLimitFailure 表示后端故障下按 FailureMode 处置。
	RateLimitFailure RateLimitOutcome = "backend_failure"
)

// RecordRateLimitDecision 记录一次限流判定。
//
// policyID 是配置里登记的策略 ID，取值由配置决定，是有限集合——
// 这里**不接受限流键**，那才是每用户/每 IP 的高基数值。
func (r *Recorder) RecordRateLimitDecision(ctx context.Context, policyID string, outcome RateLimitOutcome) {
	if r == nil {
		return
	}
	r.rateLimitDecisions.Add(ctx, 1, metric.WithAttributes(
		attribute.String("ratelimit.policy", policyID),
		attribute.String("ratelimit.outcome", string(outcome)),
	))
}

// WorkerKind identifies a fixed execution boundary, not a task payload or ID.
type WorkerKind string

const (
	WorkerOneShot WorkerKind = "one_shot"
	WorkerQueue   WorkerKind = "queue"
)

// RecordWorkerExecution records one invocation. worker must be a name fixed at
// assembly time. The lifetime of a consumer is not an invocation.
func (r *Recorder) RecordWorkerExecution(ctx context.Context, kind WorkerKind, worker string, elapsed time.Duration, err error, panicked bool) {
	if r == nil {
		return
	}
	switch kind {
	case WorkerOneShot, WorkerQueue:
	default:
		kind = "other"
	}
	outcome := "success"
	switch {
	case panicked:
		outcome = "panic"
	case errx.IsPureCancellation(err):
		outcome = "canceled"
	case errx.IsPureDeadline(err):
		outcome = "timeout"
	case err != nil:
		outcome = "failure"
	}
	attrs := metric.WithAttributes(
		attribute.String("worker.name", worker),
		attribute.String("worker.kind", string(kind)),
		attribute.String("worker.outcome", outcome),
	)
	r.workerExecutions.Add(ctx, 1, attrs)
	r.workerDuration.Record(ctx, max(elapsed.Seconds(), 0), attrs)
}

// RunWorker observes an invocation without recovering its panic or changing its
// return value. A nil Recorder still runs the task.
func (r *Recorder) RunWorker(ctx context.Context, kind WorkerKind, worker string, run func(context.Context) error) (err error) {
	started := time.Now()
	returned := false
	defer func() {
		r.RecordWorkerExecution(ctx, kind, worker, time.Since(started), err, !returned)
	}()
	err = run(ctx)
	returned = true
	return err
}

// DBPoolStats 是连接池的当前用量，字段对应 database/sql.DBStats。
type DBPoolStats struct {
	Open    int
	InUse   int
	Idle    int
	Wait    int64
	MaxOpen int
}

// RegisterDBPoolStats 以异步回调暴露数据库连接池用量。
//
// 同样走异步：连接池用量每时每刻都在变，没有一个"业务事件"可以挂靠。
func (r *Recorder) RegisterDBPoolStats(snapshot func() DBPoolStats) (func() error, error) {
	if r == nil || snapshot == nil {
		return func() error { return nil }, nil
	}

	connections, err := r.meter.Int64ObservableGauge(
		"db.client.connections.usage",
		metric.WithDescription("Database connections by state"),
	)
	if err != nil {
		return nil, fmt.Errorf("metrics: build db connection gauge: %w", err)
	}

	maxOpen, err := r.meter.Int64ObservableGauge(
		"db.client.connections.max",
		metric.WithDescription("Configured maximum number of open connections"),
	)
	if err != nil {
		return nil, fmt.Errorf("metrics: build db max-connection gauge: %w", err)
	}

	waitCount, err := r.meter.Int64ObservableCounter(
		"db.client.connections.wait",
		metric.WithDescription("Total number of connection waits"),
	)
	if err != nil {
		return nil, fmt.Errorf("metrics: build db wait counter: %w", err)
	}

	// 用 state 标签区分 in_use / idle，而不是拆成两个仪表：
	// 这样"池子有多满"可以直接对同一指标按 state 求和后比 max，
	// 拆成两个指标的话，那个查询要跨指标做算术。
	inUse := metric.WithAttributes(attribute.String("db.connection.state", "used"))
	idle := metric.WithAttributes(attribute.String("db.connection.state", "idle"))

	reg, err := r.meter.RegisterCallback(
		func(_ context.Context, o metric.Observer) error {
			stats := snapshot()
			o.ObserveInt64(connections, int64(stats.InUse), inUse)
			o.ObserveInt64(connections, int64(stats.Idle), idle)
			o.ObserveInt64(maxOpen, int64(stats.MaxOpen))
			o.ObserveInt64(waitCount, stats.Wait)
			return nil
		},
		connections, maxOpen, waitCount,
	)
	if err != nil {
		return nil, fmt.Errorf("metrics: register db pool callback: %w", err)
	}
	return reg.Unregister, nil
}

// AuthEvent 是认证事件类型，取值有限。
type AuthEvent string

const (
	// AuthLoginSucceeded 登录成功。
	AuthLoginSucceeded AuthEvent = "login_succeeded"
	// AuthLoginFailed 登录失败（凭据错误或账号不存在，两者不区分）。
	AuthLoginFailed AuthEvent = "login_failed"
	// AuthRefreshReused 检测到刷新令牌重用——这是安全事件，值得单独一条告警。
	AuthRefreshReused AuthEvent = "refresh_reused"
	// AuthLogout 登出。
	AuthLogout AuthEvent = "logout"
)

// RecordAuthEvent 记录一次认证事件。
//
// **不带用户标识**：那会让序列数随注册用户数增长。需要按用户排查时用日志，
// 那里有请求 ID 可以关联。
func (r *Recorder) RecordAuthEvent(ctx context.Context, event AuthEvent) {
	if r == nil {
		return
	}
	r.authEvents.Add(ctx, 1, metric.WithAttributes(
		attribute.String("auth.event", string(event)),
	))
}
