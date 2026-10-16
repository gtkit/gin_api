package dao

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/gtkit/ormx"
	"github.com/gtkit/redisx"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"github.com/gtkit/gin-api/internal/testutil"
)

// sentinelBind 是反证测试的哨兵：它一旦出现在任何 span 属性里，脱敏声明即为假。
// 取形如手机号的值——绑定参数里真正流动的就是这类敏感数据。
const sentinelBind = "13800001111"

// newRecorder 返回采集用 provider 与其 recorder。
func newRecorder() (*sdktrace.TracerProvider, *tracetest.SpanRecorder) {
	rec := tracetest.NewSpanRecorder()
	return sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)), rec
}

// newSQLMockGorm 构造以 sqlmock 为连接的 *gorm.DB。
//
// 用 sqlmock 而不是 gorm 的 DummyDialector：finisher 需要 ConnPool（DummyDialector
// 下直接 nil deref），DryRun 会话又被 otelgorm 的守卫跳过——sqlmock 是唯一能离线
// 走完与生产相同的非 DryRun 回调路径的形态（design.md 有 spike 记录）。
func newSQLMockGorm(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      sqlDB,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return db, mock
}

// newSQLMockOrmxClient 构造以 sqlmock 为连接的 *ormx.Client，供直接拼装 Resources。
func newSQLMockOrmxClient(t *testing.T) (*ormx.Client, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	client, err := ormx.OpenWithDB(context.Background(), sqlDB,
		ormx.WithSkipInitializeWithVersion(true),
		ormx.WithServerVersion("8.0.36"),
		ormx.WithStartupPing(false),
	)
	if err != nil {
		t.Fatalf("ormx.OpenWithDB: %v", err)
	}
	return client, mock
}

// spanAttrsContain 报告 spans 的任一属性值是否含 needle。
func spanAttrsContain(spans []sdktrace.ReadOnlySpan, needle string) bool {
	for _, s := range spans {
		for _, a := range s.Attributes() {
			if strings.Contains(a.Value.String(), needle) {
				return true
			}
		}
	}
	return false
}

// TestInstrumentGormSpansRedactBindVariables 钉住 spec「DB 查询产生链路 span 且
// 绝不携带绑定参数」的两个场景：子 span 挂在父 trace 下；哨兵绑定参数不出现在
// 任何属性中，db.query.text 仅含占位符。若有人移除 WithoutQueryVariables，
// 哨兵会出现在 db.query.text（spike 已验证判别力），本测试即失败——这是反证形态。
func TestInstrumentGormSpansRedactBindVariables(t *testing.T) {
	t.Parallel()

	db, mock := newSQLMockGorm(t)
	tp, rec := newRecorder()
	if err := instrumentGorm(db, "app", tp); err != nil {
		t.Fatalf("instrumentGorm: %v", err)
	}

	ctx, parent := tp.Tracer("test").Start(context.Background(), "parent")
	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	var ids []int64
	if err := db.WithContext(ctx).
		Raw("SELECT id FROM users WHERE phone = ?", sentinelBind).
		Scan(&ids).Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	parent.End()

	spans := rec.Ended()
	var client sdktrace.ReadOnlySpan
	for _, s := range spans {
		if s.SpanKind() == trace.SpanKindClient {
			client = s
		}
	}
	if client == nil {
		t.Fatalf("no client span recorded, spans=%d", len(spans))
	}
	if got, want := client.SpanContext().TraceID(), parent.SpanContext().TraceID(); got != want {
		t.Errorf("client span trace id = %s, want parent's %s", got, want)
	}

	stmt := ""
	for _, a := range client.Attributes() {
		if a.Key == "db.query.text" {
			stmt = a.Value.String()
		}
	}
	if stmt == "" {
		t.Error("db.query.text attribute missing: placeholder SQL should be present")
	}
	if !strings.Contains(stmt, "?") {
		t.Errorf("db.query.text %q lacks placeholder", stmt)
	}
	if spanAttrsContain(spans, sentinelBind) {
		t.Errorf("bind variable leaked into span attributes; db.query.text=%q", stmt)
	}
}

// TestInstrumentRedisSpansOmitStatement 钉住 spec「Redis 命令产生链路 span 且
// 绝不携带命令语句」：SET 哨兵值后必须有 span，且无 db.statement 键、任何属性
// 不含哨兵。若有人移除 WithDBStatement(false)，语句（含哨兵）会出现在属性里
// （spike 已验证判别力），本测试即失败。
func TestInstrumentRedisSpansOmitStatement(t *testing.T) {
	t.Parallel()
	testutil.RequireLoopback(t)

	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	tp, rec := newRecorder()
	if err := instrumentRedis(client, tp); err != nil {
		t.Fatalf("instrumentRedis: %v", err)
	}

	if err := client.Set(context.Background(), "vc:code", sentinelBind, 0).Err(); err != nil {
		t.Fatalf("set: %v", err)
	}

	spans := rec.Ended()
	if len(spans) == 0 {
		t.Fatal("no spans recorded")
	}
	for _, s := range spans {
		for _, a := range s.Attributes() {
			if a.Key == "db.statement" {
				t.Errorf("span %q carries db.statement %q", s.Name(), a.Value.String())
			}
		}
	}
	if spanAttrsContain(spans, sentinelBind) {
		t.Error("redis command value leaked into span attributes")
	}
}

// TestInstrumentTracingCoversAllRedisDBs 钉住 spec「多 DB 全部覆盖」与方法级接线：
// 经 Resources.InstrumentTracing（生产入口，走全局 provider）挂载后，
// 每个已初始化 DB 上的命令都产生 span，GORM 查询同样产生 span。
//
// 不能 t.Parallel：需要临时替换全局 TracerProvider（先例：telemetry_test.go）。
func TestInstrumentTracingCoversAllRedisDBs(t *testing.T) {
	testutil.RequireLoopback(t)

	srv := miniredis.RunT(t)
	rx, err := redisx.NewClient(
		redisx.WithAddr(srv.Addr()),
		redisx.WithInitDBs(0, 1),
	)
	if err != nil {
		t.Fatalf("redisx.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = rx.Close() })

	client, mock := newSQLMockOrmxClient(t)
	res := &Resources{mysql: client, redis: rx}

	tp, rec := newRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	if err := res.InstrumentTracing("app"); err != nil {
		t.Fatalf("InstrumentTracing: %v", err)
	}

	ctx := context.Background()
	for _, db := range []int{0, 1} {
		if err := rx.MustSelectDB(db).Set(ctx, "k", "v", 0).Err(); err != nil {
			t.Fatalf("set on db %d: %v", db, err)
		}
	}
	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	var ids []int64
	if err := res.Mdb().WithContext(ctx).Raw("SELECT id FROM t").Scan(&ids).Error; err != nil {
		t.Fatalf("gorm query: %v", err)
	}

	setSpans, gormSpans := 0, 0
	for _, s := range rec.Ended() {
		if s.Name() == "set" {
			setSpans++
		}
		if strings.HasPrefix(s.Name(), "gorm.") {
			gormSpans++
		}
	}
	if setSpans != 2 {
		t.Errorf("set spans = %d, want 2 (one per initialized redis db)", setSpans)
	}
	if gormSpans == 0 {
		t.Error("no gorm span: method did not wire gorm through the global provider")
	}
}

// TestInstrumentTracingRegistersNoPoolMetrics 钉住 spec「DB 连接池指标保持单一属主」：
// 以可采集的 MeterProvider 为全局 provider 完成挂载后，采集结果必须为空——
// 挂载不得注册任何指标。若有人移除 WithoutMetrics，otelgorm 会经 otelsql 注册
// 一组 DBStats 仪表，本测试即失败。
//
// 不能 t.Parallel：需要临时替换全局 MeterProvider。
func TestInstrumentTracingRegistersNoPoolMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() { otel.SetMeterProvider(prev) })

	client, _ := newSQLMockOrmxClient(t)
	res := &Resources{mysql: client}

	if err := res.InstrumentTracing("app"); err != nil {
		t.Fatalf("InstrumentTracing: %v", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			t.Errorf("instrumentation registered metric %q (scope %s); pool metrics owner is metrics.Recorder", m.Name, scope.Scope.Name)
		}
	}
}

// TestInstrumentGormNoopWithoutProvider 钉住 spec「导出关闭时行为不变」：
// 不注入 provider（生产路径落到全局 no-op）时，查询结果与错误语义与挂载前一致。
func TestInstrumentGormNoopWithoutProvider(t *testing.T) {
	db, mock := newSQLMockGorm(t)
	if err := instrumentGorm(db, "app", nil); err != nil {
		t.Fatalf("instrumentGorm: %v", err)
	}

	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
	var ids []int64
	if err := db.WithContext(context.Background()).Raw("SELECT id FROM t").Scan(&ids).Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(ids) != 1 || ids[0] != 7 {
		t.Errorf("scan result = %v, want [7]: instrumentation must not alter query semantics", ids)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestInstrumentTracingRequiresMySQL 钉住入口契约：mysql 未初始化时报错而不是静默跳过。
func TestInstrumentTracingRequiresMySQL(t *testing.T) {
	t.Parallel()

	var nilRes *Resources
	if err := nilRes.InstrumentTracing("app"); err == nil {
		t.Error("nil Resources: want error")
	}
	if err := (&Resources{}).InstrumentTracing("app"); err == nil {
		t.Error("mysql-less Resources: want error")
	}
}
