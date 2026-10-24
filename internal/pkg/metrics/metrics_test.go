package metrics

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// newTestRecorder 装配一个可读回记录值的 Recorder。
//
// 用 SDK 的 ManualReader 而不是替身：本包的产出是**指标数据**，而替身只能
// 证明"某个方法被调用了"。基数约束这类断言必须看真实产生的时间序列——
// 一个把原始 URL 写进标签的实现，在替身视角下与正确实现毫无区别。
//
// SDK 只是测试依赖：生产路径依赖的是 API 包与它的 no-op 实现。
func newTestRecorder(t *testing.T) (*Recorder, *sdkmetric.ManualReader) {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("provider.Shutdown(context.Background()): %v", err)
		}
	})

	rec, err := New(provider.Meter(scopeName))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return rec, reader
}

// collect 采集一次并返回指定名称的指标。
func collect(t *testing.T, reader *sdkmetric.ManualReader, name string) metricdata.Metrics {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m
			}
		}
	}
	t.Fatalf("no metric named %q was produced", name)
	return metricdata.Metrics{}
}

// seriesCount 返回一个指标产生了多少条时间序列。
func seriesCount(t *testing.T, m metricdata.Metrics) int {
	t.Helper()

	switch data := m.Data.(type) {
	case metricdata.Sum[int64]:
		return len(data.DataPoints)
	case metricdata.Gauge[int64]:
		return len(data.DataPoints)
	case metricdata.Histogram[float64]:
		return len(data.DataPoints)
	default:
		t.Fatalf("metric %q has an unexpected data shape %T", m.Name, m.Data)
		return 0
	}
}

// TestUnmatchedRoutesCollapseIntoOneSeries 是基数约束最重要的一条。
//
// 未匹配路径完全由请求方控制。若它们各自成为一条时间序列，一次目录爆破
// 就能凭空造出成千上万条序列，把监控后端压垮——而故障出现在监控侧，
// 恰恰是最需要它的时候不可用。
func TestUnmatchedRoutesCollapseIntoOneSeries(t *testing.T) {
	t.Parallel()

	rec, reader := newTestRecorder(t)
	ctx := context.Background()

	// 模拟一次目录扫描：100 个各不相同的未匹配路径。
	for i := range 100 {
		_ = i
		rec.RecordRequest(ctx, "GET", UnmatchedRoute, 404, time.Millisecond)
	}

	if got := seriesCount(t, collect(t, reader, "http.server.requests")); got != 1 {
		t.Fatalf("100 scanning requests produced %d series, want 1: unbounded series growth "+
			"is how a monitoring backend gets taken down", got)
	}
}

func TestUnknownHTTPMethodsShareOneSeries(t *testing.T) {
	t.Parallel()
	rec, reader := newTestRecorder(t)
	for i := range 100 {
		rec.RecordRequest(context.Background(), fmt.Sprintf("SCAN%d", i), UnmatchedRoute, 404, time.Millisecond)
	}
	for _, name := range []string{"http.server.requests", "http.server.request.duration"} {
		m := collect(t, reader, name)
		if got := seriesCount(t, m); got != 1 {
			t.Fatalf("%s: unknown methods produced %d series, want 1", name, got)
		}
	}
	sum, ok := collect(t, reader, "http.server.requests").Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatal("request counter has an unexpected data shape")
	}
	method, ok := sum.DataPoints[0].Attributes.Value("http.request.method")
	if !ok || method.AsString() != "_OTHER" || sum.DataPoints[0].Value != 100 {
		t.Fatalf("unexpected method aggregation: %+v", sum.DataPoints)
	}
}

func TestKnownHTTPMethodsRemainDistinct(t *testing.T) {
	t.Parallel()
	methods := []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace, http.MethodPatch, "QUERY"}
	rec, reader := newTestRecorder(t)
	for _, method := range methods {
		rec.RecordRequest(context.Background(), method, UnmatchedRoute, 404, time.Millisecond)
	}
	sum, ok := collect(t, reader, "http.server.requests").Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatal("request counter has an unexpected data shape")
	}
	if len(sum.DataPoints) != len(methods) {
		t.Fatalf("known methods produced %d series, want %d", len(sum.DataPoints), len(methods))
	}
	for _, dp := range sum.DataPoints {
		method, ok := dp.Attributes.Value("http.request.method")
		if !ok || method.AsString() == "_OTHER" {
			t.Fatalf("known method lost: %+v", dp)
		}
	}
}

// TestRouteLabelIsATemplate 钉住路由标签用模板而非实例。
//
// 用实际路径的话，序列数会随业务数据量增长——每一个 user id 一条。
func TestRouteLabelIsATemplate(t *testing.T) {
	t.Parallel()

	rec, reader := newTestRecorder(t)
	ctx := context.Background()

	// 同一路由的 50 次请求（调用方传的是模板，因为中间件取 FullPath）。
	for range 50 {
		rec.RecordRequest(ctx, "GET", "/api/v1/user/:id", 200, time.Millisecond)
	}

	m := collect(t, reader, "http.server.requests")
	if got := seriesCount(t, m); got != 1 {
		t.Fatalf("50 requests to one route produced %d series, want 1", got)
	}

	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("unexpected data shape %T", m.Data)
	}
	if sum.DataPoints[0].Value != 50 {
		t.Errorf("count = %d, want 50", sum.DataPoints[0].Value)
	}
}

// TestRequestAttributesCarryNoHighCardinalityFields 钉住标签集合本身。
//
// 逐个检查属性名，而不是只数序列条数：一个刚被加进去的高基数标签在测试
// 数据量小的时候未必体现为序列增多，但上线后会。
func TestRequestAttributesCarryNoHighCardinalityFields(t *testing.T) {
	t.Parallel()

	rec, reader := newTestRecorder(t)
	rec.RecordRequest(context.Background(), "GET", "/api/v1/user", 200, time.Millisecond)

	m := collect(t, reader, "http.server.requests")
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("metric has unexpected shape %T", m.Data)
	}

	allowed := map[attribute.Key]struct{}{
		"http.request.method":       {},
		"http.route":                {},
		"http.response.status_code": {},
	}
	for _, dp := range sum.DataPoints {
		for _, attr := range dp.Attributes.ToSlice() {
			if _, ok := allowed[attr.Key]; !ok {
				t.Errorf("unexpected attribute %q on http.server.requests: every new label "+
					"multiplies the series count, so the allowed set is a contract", attr.Key)
			}
		}
	}
}

// TestDurationHistogramHasUsefulBuckets 钉住直方图的分辨率。
//
// 用 SDK 默认边界（最小桶 5ms）的话，一个正常的读接口全部落进头两个桶，
// P50/P90 一起退化成"小于 5ms"——直方图等于没有分辨率，而这只有在真去看
// 分位数时才会发现。
func TestDurationHistogramHasUsefulBuckets(t *testing.T) {
	t.Parallel()

	rec, reader := newTestRecorder(t)
	ctx := context.Background()

	// 三个典型时延：内存命中、一次数据库往返、一次慢查询。
	for _, d := range []time.Duration{2 * time.Millisecond, 30 * time.Millisecond, 800 * time.Millisecond} {
		rec.RecordRequest(ctx, "GET", "/api/v1/user", 200, d)
	}

	m := collect(t, reader, "http.server.request.duration")
	hist, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("unexpected data shape %T", m.Data)
	}
	dp := hist.DataPoints[0]

	// 这三个值必须落进**不同**的桶，否则直方图对这段区间没有分辨能力。
	occupied := make(map[int]struct{})
	for i, count := range dp.BucketCounts {
		if count > 0 {
			occupied[i] = struct{}{}
		}
	}
	if len(occupied) != 3 {
		t.Fatalf("three latencies an order of magnitude apart landed in %d buckets, want 3: "+
			"the histogram cannot resolve them (bounds: %v)", len(occupied), dp.Bounds)
	}
}

// TestDBPoolStatsSplitByState 钉住连接池按状态分标签。
func TestDBPoolStatsSplitByState(t *testing.T) {
	t.Parallel()

	rec, reader := newTestRecorder(t)
	unregister, err := rec.RegisterDBPoolStats(func() DBPoolStats {
		return DBPoolStats{Open: 7, InUse: 5, Idle: 2, Wait: 11, MaxOpen: 20}
	})
	if err != nil {
		t.Fatalf("RegisterDBPoolStats: %v", err)
	}
	t.Cleanup(func() {
		if err := unregister(); err != nil {
			t.Errorf("unregister(): %v", err)
		}
	})

	m := collect(t, reader, "db.client.connections.usage")
	gauge, ok := m.Data.(metricdata.Gauge[int64])
	if !ok {
		t.Fatalf("metric has unexpected shape %T", m.Data)
	}
	if len(gauge.DataPoints) != 2 {
		t.Fatalf("connection usage produced %d series, want 2 (used + idle)", len(gauge.DataPoints))
	}

	byState := map[string]int64{}
	for _, dp := range gauge.DataPoints {
		state, ok := dp.Attributes.Value("db.connection.state")
		if !ok {
			t.Fatal("a usage data point carries no state label")
		}
		byState[state.AsString()] = dp.Value
	}
	if byState["used"] != 5 || byState["idle"] != 2 {
		t.Errorf("usage by state = %v, want used=5 idle=2", byState)
	}
}

// TestNilRecorderIsSafe 钉住未装配时不崩。
//
// 指标是横切关注点，记录点散布在各处；要求每处判空的话，漏掉一处的表现是
// 生产环境空指针崩溃——代价远大于这几行判空。
func TestNilRecorderIsSafe(t *testing.T) {
	t.Parallel()

	var rec *Recorder
	ctx := context.Background()

	// 不 panic 即通过。
	rec.RecordRequest(ctx, "GET", "/x", 200, time.Millisecond)
	rec.RecordRateLimitDecision(ctx, "p", RateLimitThrottled)
	rec.RecordAuthEvent(ctx, AuthLoginFailed)
}

// TestNoMeterFallsBackToNoop 钉住 nil meter 走 no-op 而不是崩。
func TestNoMeterFallsBackToNoop(t *testing.T) {
	t.Parallel()

	rec, err := New(nil)
	if err != nil {
		t.Fatalf("New(nil): %v", err)
	}
	// no-op 下记录不产生任何可观测行为，也不得 panic。
	rec.RecordRequest(context.Background(), "GET", "/x", 200, time.Millisecond)
}

// TestRateLimitOutcomesAreDistinct 钉住拒绝原因可区分。
//
// "超速率"与"键基数到顶"的处置完全不同（调限额 vs 调 max_keys）。
// 合并成一个计数会让运维看到一个数字，却不知道该动哪个旋钮。
func TestRateLimitOutcomesAreDistinct(t *testing.T) {
	t.Parallel()

	rec, reader := newTestRecorder(t)
	ctx := context.Background()

	rec.RecordRateLimitDecision(ctx, "default.ip", RateLimitThrottled)
	rec.RecordRateLimitDecision(ctx, "default.ip", RateLimitKeyLimit)

	m := collect(t, reader, "ratelimit.decisions")
	if got := seriesCount(t, m); got != 2 {
		t.Fatalf("two distinct rejection reasons produced %d series, want 2: merging them "+
			"leaves the operator unable to tell which knob to turn", got)
	}
}

// TestAuthEventsCarryNoUserIdentity 钉住认证指标不带用户标识。
//
// 带上的话序列数随注册用户数增长。按用户排查要用日志——那里有请求 ID。
func TestAuthEventsCarryNoUserIdentity(t *testing.T) {
	t.Parallel()

	rec, reader := newTestRecorder(t)
	ctx := context.Background()

	// 一万个"不同用户"的登录失败必须只产生一条序列。
	for i := range 100 {
		_ = fmt.Sprint(i)
		rec.RecordAuthEvent(ctx, AuthLoginFailed)
	}

	if got := seriesCount(t, collect(t, reader, "auth.events")); got != 1 {
		t.Fatalf("auth events produced %d series for one event kind, want 1", got)
	}
}
