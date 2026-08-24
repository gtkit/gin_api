package dataisolate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"github.com/gtkit/gin-api/internal/shared/tenant"
)

// ── 测试模型 ────────────────────────────────────────────────────────────

// scopedRow 声明了判别列，因此受控。
type scopedRow struct {
	ID       int64  `gorm:"column:id;primaryKey"`
	Name     string `gorm:"column:name"`
	TenantID string `gorm:"column:tenant_id"`
}

func (scopedRow) TableName() string { return "scoped_rows" }

// globalRow 没有判别列，因此不受控（全局字典表这类）。
type globalRow struct {
	ID   int64  `gorm:"column:id;primaryKey"`
	Code string `gorm:"column:code"`
}

func (globalRow) TableName() string { return "global_rows" }

// softRow 受控且启用软删除，用于验证软删除条件不被算作调用方条件。
type softRow struct {
	ID        int64          `gorm:"column:id;primaryKey"`
	TenantID  string         `gorm:"column:tenant_id"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at"`
}

func (softRow) TableName() string { return "soft_rows" }

func testModels() []any {
	return []any{&scopedRow{}, &globalRow{}, &softRow{}}
}

// ── 测试脚手架 ──────────────────────────────────────────────────────────

// openMockDB 构造以 sqlmock 为连接的 *gorm.DB（未装插件）。
//
// 与 internal/platform/data 的测试同一形态：DummyDialector 下 finisher 会
// nil deref，sqlmock 是唯一能离线走完真实回调路径的连接。
//
// SkipDefaultTransaction 是必需的，且与被测行为无关：写路径默认包一层事务，
// 而 BeginTransaction 只看 SkipDefaultTransaction、**不看 DryRun**
// （gorm/callbacks/transaction.go:8）。不关掉它，sqlmock 上未预期的 Begin
// 会先把 db.Error 置位，本插件的钩子随即按设计提前返回——于是每个写路径用例
// 测到的都是"事务没开起来"，而不是注入逻辑。
func openMockDB(t *testing.T) *gorm.DB {
	t.Helper()

	sqlDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      sqlDB,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return db
}

// newDB 构造已装插件的句柄。
func newDB(t *testing.T, opts Options) *gorm.DB {
	t.Helper()
	db, _ := newDBWithMock(t, opts)
	return db
}

// newDBWithMock 同上，并返回底层 mock，供需要设置期望的用例使用。
func newDBWithMock(t *testing.T, opts Options) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()

	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      sqlDB,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}

	plugin, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := db.Use(plugin); err != nil {
		t.Fatalf("db.Use: %v", err)
	}
	return db, mock
}

func defaultOptions() Options {
	return Options{Column: "tenant_id", Models: testModels()}
}

// dryRun 返回 DryRun 会话：只生成 SQL 不执行，SQL 与参数在语句结束后仍可读。
func dryRun(db *gorm.DB, ctx context.Context) *gorm.DB {
	return db.Session(&gorm.Session{DryRun: true}).WithContext(ctx)
}

func acme() context.Context {
	return tenant.With(context.Background(), "acme")
}

// ── 装配期校验 ──────────────────────────────────────────────────────────

func TestNewRejectsUnusableOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		opts    Options
		wantSub string
	}{
		{
			name:    "空列名",
			opts:    Options{Models: testModels()},
			wantSub: "column is required",
		},
		{
			name:    "无模型",
			opts:    Options{Column: "tenant_id"},
			wantSub: "models are required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := New(tc.opts)
			if err == nil {
				t.Fatal("New() = nil error, want failure")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("New() error = %q, want it to contain %q", err, tc.wantSub)
			}
		})
	}
}

// TestInitializeRejectsUnusableConfiguration 覆盖依赖命名策略的那批校验。
//
// 列名写错是本能力最坏的失效模式：LookUpField 永不命中 → 零张表受控 →
// 隔离静默消失而一切看起来正常。它必须在装配期就失败。
func TestInitializeRejectsUnusableConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*Options)
		wantSub string
	}{
		{
			name:    "列名写错导致零张表受控",
			mutate:  func(o *Options) { o.Column = "tenant_i" },
			wantSub: "no managed model declares column",
		},
		{
			name:    "豁免清单指向不存在的表",
			mutate:  func(o *Options) { o.IgnoreTables = []string{"no_such_table"} },
			wantSub: "ignore_tables lists table",
		},
		{
			name:    "强制清单指向不存在的表",
			mutate:  func(o *Options) { o.ForceTables = []string{"no_such_table"} },
			wantSub: "tables lists table",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opts := defaultOptions()
			tc.mutate(&opts)

			plugin, err := New(opts)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			err = openMockDB(t).Use(plugin)
			if err == nil {
				t.Fatal("db.Use() = nil error, want failure")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("db.Use() error = %q, want it to contain %q", err, tc.wantSub)
			}
		})
	}
}

// ── 注入 ────────────────────────────────────────────────────────────────

func TestQueryInjectsTenantCondition(t *testing.T) {
	t.Parallel()

	db := newDB(t, defaultOptions())
	var rows []scopedRow
	stmt := dryRun(db, acme()).Where("name = ?", "n").Find(&rows).Statement

	sql := stmt.SQL.String()
	if !strings.Contains(sql, "`tenant_id` = ?") {
		t.Fatalf("SQL did not carry the tenant condition: %s", sql)
	}
	if !containsVar(stmt.Vars, "acme") {
		t.Fatalf("tenant value did not reach the bind vars: %#v", stmt.Vars)
	}
	// 值只走参数占位：SQL 文本里不得出现字面量。
	if strings.Contains(sql, "acme") {
		t.Fatalf("tenant value leaked into the SQL text: %s", sql)
	}
}

// TestTopLevelOrDoesNotEscapeTenant 是本包最重要的一条判据。
//
// 朴素地把租户条件追加到 WHERE 尾部，遇到调用方写了 .Or() 就会生成
// `A OR B AND tenant_id = ?`——按 SQL 优先级即 `A OR (B AND tenant_id = ?)`，
// 另一个租户的行只要满足 A 就被返回。这是静默越权。
//
// 反证：移除 injectCondition 里的 group 包装，本用例必须失败。
func TestTopLevelOrDoesNotEscapeTenant(t *testing.T) {
	t.Parallel()

	db := newDB(t, defaultOptions())
	var rows []scopedRow
	stmt := dryRun(db, acme()).
		Where("name = ?", "a").Or("name = ?", "b").
		Find(&rows).Statement

	sql := stmt.SQL.String()
	where := whereFragment(t, sql)

	// 调用方的条件必须整体被括起来，租户条件在括号之外以 AND 并列。
	if !strings.HasPrefix(where, "(") {
		t.Fatalf("user conditions were not grouped; a top-level OR escapes the tenant filter.\nWHERE %s", where)
	}
	closing := strings.Index(where, ")")
	if closing < 0 {
		t.Fatalf("grouped conditions are not closed.\nWHERE %s", where)
	}
	inside, outside := where[:closing+1], where[closing+1:]
	if !strings.Contains(inside, " OR ") {
		t.Fatalf("the OR did not land inside the group.\nWHERE %s", where)
	}
	if !strings.Contains(outside, "AND") || !strings.Contains(outside, "`tenant_id` = ?") {
		t.Fatalf("the tenant condition is not ANDed outside the group.\nWHERE %s", where)
	}
}

// TestNewlineOrIsAlsoGrouped 覆盖 GORM 自带括号启发式的盲区。
//
// GORM 只在表达式 SQL 文本含 " OR " 时补括号，而这里 OR 前面是换行符——
// 子串匹配不到。若本包依赖那个启发式，本用例就是越权。
func TestNewlineOrIsAlsoGrouped(t *testing.T) {
	t.Parallel()

	db := newDB(t, defaultOptions())
	var rows []scopedRow
	stmt := dryRun(db, acme()).
		Where("name = ?\nOR name = ?", "a", "b").
		Find(&rows).Statement

	where := whereFragment(t, stmt.SQL.String())
	if !strings.HasPrefix(where, "(") {
		t.Fatalf("a newline-separated OR was not grouped.\nWHERE %s", where)
	}
}

func TestCreateStampsTenant(t *testing.T) {
	t.Parallel()

	db := newDB(t, defaultOptions())
	row := scopedRow{Name: "n"}
	stmt := dryRun(db, acme()).Create(&row).Statement

	if row.TenantID != "acme" {
		t.Fatalf("row.TenantID = %q, want %q", row.TenantID, "acme")
	}
	if !containsVar(stmt.Vars, "acme") {
		t.Fatalf("tenant value did not reach the insert vars: %#v", stmt.Vars)
	}
}

func TestCreateStampsEveryRowOfABatch(t *testing.T) {
	t.Parallel()

	db := newDB(t, defaultOptions())
	rows := []scopedRow{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	dryRun(db, acme()).Create(&rows)

	for i, r := range rows {
		if r.TenantID != "acme" {
			t.Fatalf("rows[%d].TenantID = %q, want %q", i, r.TenantID, "acme")
		}
	}
}

// TestCreateAcceptsAMatchingExplicitTenant 显式写了同一个租户不算冲突。
func TestCreateAcceptsAMatchingExplicitTenant(t *testing.T) {
	t.Parallel()

	db := newDB(t, defaultOptions())
	row := scopedRow{Name: "n", TenantID: "acme"}
	if err := dryRun(db, acme()).Create(&row).Error; err != nil {
		t.Fatalf("a matching explicit tenant was rejected: %v", err)
	}
}

// TestCreateRejectsAForeignExplicitTenant 钉住跨租户写入被拒。
//
// 静默覆盖能防住越权，但会把调用方明确写下的值悄悄丢掉——那要么是 bug、
// 要么是越权尝试，两者都不该无声无息。
func TestCreateRejectsAForeignExplicitTenant(t *testing.T) {
	t.Parallel()

	db := newDB(t, defaultOptions())
	row := scopedRow{Name: "n", TenantID: "globex"}
	err := dryRun(db, acme()).Create(&row).Error

	if err == nil {
		t.Fatal("writing into another tenant was accepted")
	}
	for _, want := range []string{"globex", "acme", "bypass"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

// TestCreateRejectsAForeignTenantAnywhereInABatch 批量里任何一行越界都要被拒。
//
// 只查第一行是很自然的写法，而它会让「批量插入」成为绕过这道检查的通道。
func TestCreateRejectsAForeignTenantAnywhereInABatch(t *testing.T) {
	t.Parallel()

	db := newDB(t, defaultOptions())
	rows := []scopedRow{{Name: "a"}, {Name: "b", TenantID: "globex"}, {Name: "c"}}
	err := dryRun(db, acme()).Create(&rows).Error

	if err == nil {
		t.Fatal("a batch carrying a foreign tenant was accepted")
	}
	if !strings.Contains(err.Error(), "row 1") {
		t.Fatalf("error %q does not point at the offending row", err)
	}
}

// TestForcedTableIsControlledWithoutTheColumn 强制纳入优先于元数据。
func TestForcedTableIsControlledWithoutTheColumn(t *testing.T) {
	t.Parallel()

	opts := defaultOptions()
	opts.ForceTables = []string{"global_rows"}
	db := newDB(t, opts)

	var rows []globalRow
	// 强制纳入后，没有身份就该失败——即便这张表没有判别列。
	err := dryRun(db, context.Background()).Find(&rows).Error
	if !errors.Is(err, errNoIdentity) {
		t.Fatalf("error = %v, want errNoIdentity", err)
	}
}

// TestIgnoreWinsOverForce 钉住判定顺序：豁免优先于强制。
//
// 两份清单同时命中同一张表时必须有确定的赢家，否则同一份配置在不同版本上
// 可能给出不同的隔离行为，而配置本身看不出任何问题。
func TestIgnoreWinsOverForce(t *testing.T) {
	t.Parallel()

	opts := defaultOptions()
	opts.IgnoreTables = []string{"scoped_rows"}
	opts.ForceTables = []string{"scoped_rows"}
	db := newDB(t, opts)

	var rows []scopedRow
	stmt := dryRun(db, context.Background()).Find(&rows).Statement
	if stmt.Error != nil {
		t.Fatalf("ignore did not win over force: %v", stmt.Error)
	}
}

// TestFailClosedMessagesNameTheirWayOut 三种 fail-closed 成因各给出不同的出路。
//
// 合并成一句话会让最容易卡住的那种无从排查：三者的下一步完全不同——
// 补身份、给语句一个 Model、开安全闸。
func TestFailClosedMessagesNameTheirWayOut(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		run      func(*gorm.DB) error
		allow    bool
		wantSubs []string
	}{
		{
			name: "无身份",
			run: func(db *gorm.DB) error {
				var rows []scopedRow
				return dryRun(db, context.Background()).Find(&rows).Error
			},
			wantSubs: []string{"tenant-scoped", "authenticated"},
		},
		{
			name: "无法判定受控性",
			run: func(db *gorm.DB) error {
				var out []map[string]any
				return dryRun(db, acme()).Table("mystery_rows").Find(&out).Error
			},
			wantSubs: []string{"no model metadata", "Model"},
		},
		{
			name: "绕过被安全闸拦下",
			run: func(db *gorm.DB) error {
				var rows []scopedRow
				return dryRun(db, tenant.WithBypass(context.Background())).Find(&rows).Error
			},
			wantSubs: []string{"safety gate", "allow-bypass"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opts := defaultOptions()
			opts.AllowBypass = tc.allow
			err := tc.run(newDB(t, opts))
			if err == nil {
				t.Fatal("statement succeeded, want a fail-closed error")
			}
			for _, want := range tc.wantSubs {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

// ── 无条件写保护 ────────────────────────────────────────────────────────

// TestUnconditionalUpdateIsRefused 钉住租户条件不得成为唯一谓词。
//
// GORM 自带的保护只检查 WHERE 是否存在，且在 gorm:update 回调内部执行——
// 晚于本插件。不自行复现的话，一条今天会被拒绝的全表更新会变成
// 「静默把该租户整张表改掉」。
//
// 反证：移除 hook 里的 hasUserCondition 判定，本用例必须失败。
func TestUnconditionalUpdateIsRefused(t *testing.T) {
	t.Parallel()

	db := newDB(t, defaultOptions())
	err := dryRun(db, acme()).Model(&scopedRow{}).Update("name", "x").Error

	if !errors.Is(err, gorm.ErrMissingWhereClause) {
		t.Fatalf("error = %v, want it to wrap gorm.ErrMissingWhereClause", err)
	}
}

func TestUnconditionalDeleteIsRefused(t *testing.T) {
	t.Parallel()

	db := newDB(t, defaultOptions())
	err := dryRun(db, acme()).Delete(&scopedRow{}).Error

	if !errors.Is(err, gorm.ErrMissingWhereClause) {
		t.Fatalf("error = %v, want it to wrap gorm.ErrMissingWhereClause", err)
	}
}

// TestSoftDeleteConditionIsNotAUserCondition 覆盖软删除模型。
//
// GORM 的软删除条件在 SQL 构建阶段才加入，晚于本插件的回调；若判定实现
// 误把它算作调用方条件，这道保护对所有软删除模型就都失效了——
// 而本仓的 BaseTimeField 默认带软删除，那等于对绝大多数表失效。
func TestSoftDeleteConditionIsNotAUserCondition(t *testing.T) {
	t.Parallel()

	db := newDB(t, defaultOptions())
	err := dryRun(db, acme()).Model(&softRow{}).Update("tenant_id", "x").Error

	if !errors.Is(err, gorm.ErrMissingWhereClause) {
		t.Fatalf("error = %v, want it to wrap gorm.ErrMissingWhereClause", err)
	}
}

func TestConditionalUpdateInjectsTenant(t *testing.T) {
	t.Parallel()

	db := newDB(t, defaultOptions())
	stmt := dryRun(db, acme()).Model(&scopedRow{}).
		Where("id = ?", 1).Update("name", "x").Statement

	sql := stmt.SQL.String()
	if !strings.Contains(sql, "`tenant_id` = ?") {
		t.Fatalf("UPDATE did not carry the tenant condition: %s", sql)
	}
	// 只注入 WHERE，不碰 SET。
	setPart, _, _ := strings.Cut(sql, " WHERE ")
	if strings.Contains(setPart, "tenant_id") {
		t.Fatalf("the tenant column leaked into the SET clause: %s", sql)
	}
}

// ── fail-closed ─────────────────────────────────────────────────────────

func TestMissingIdentityFailsClosed(t *testing.T) {
	t.Parallel()

	db := newDB(t, defaultOptions())
	var rows []scopedRow
	stmt := dryRun(db, context.Background()).Find(&rows).Statement

	if !errors.Is(stmt.Error, errNoIdentity) {
		t.Fatalf("error = %v, want errNoIdentity", stmt.Error)
	}
	if sql := stmt.SQL.String(); strings.Contains(sql, "SELECT") {
		t.Fatalf("a statement without a tenant condition was still built: %s", sql)
	}
}

func TestUnknownTableFailsClosed(t *testing.T) {
	t.Parallel()

	db := newDB(t, defaultOptions())
	var out []map[string]any
	err := dryRun(db, acme()).Table("mystery_rows").Find(&out).Error

	if !errors.Is(err, errUnknownTable) {
		t.Fatalf("error = %v, want errUnknownTable", err)
	}
}

// TestTablelessStatementIsUntouched 覆盖 stmt.Table 为空这个边界。
//
// `SELECT @@session.time_zone`（连接语义校验）与 `SELECT 1`（健康检查）都没有
// 目标表，因此不可能泄漏任何租户的行。把它们 fail-closed 掉只会在排障时
// 打断最需要能跑的两件事。
//
// 这条与 TestUnknownTableFailsClosed 是一对：那边是"指定了一张表但认不出来"，
// 这边是"没有表"，两个不同的输入必须有两种不同的处置。
func TestTablelessStatementIsUntouched(t *testing.T) {
	t.Parallel()

	// 真实执行而非 DryRun：Raw + Scan 不支持 dry run（GORM 直接报
	// "dry run mode unsupported"），而这条用例要验的恰恰是语句能跑完。
	db, mock := newDBWithMock(t, defaultOptions())
	mock.ExpectQuery("SELECT @@session.time_zone").
		WillReturnRows(sqlmock.NewRows([]string{"time_zone"}).AddRow("+00:00"))

	var probe struct {
		TimeZone string
	}
	// 无身份、无表：不该失败。
	err := db.WithContext(context.Background()).
		Raw("SELECT @@session.time_zone AS time_zone").Scan(&probe).Error

	if err != nil {
		t.Fatalf("a table-less statement failed: %v", err)
	}
	if probe.TimeZone != "+00:00" {
		t.Fatalf("time_zone = %q, want %q", probe.TimeZone, "+00:00")
	}
	if mockErr := mock.ExpectationsWereMet(); mockErr != nil {
		t.Fatalf("the statement did not reach the database unchanged: %v", mockErr)
	}
}

func TestUncontrolledTableIsUntouched(t *testing.T) {
	t.Parallel()

	db := newDB(t, defaultOptions())
	var rows []globalRow
	// 没有租户身份也不该失败：这张表不受控。
	stmt := dryRun(db, context.Background()).Find(&rows).Statement

	if stmt.Error != nil {
		t.Fatalf("an uncontrolled table failed: %v", stmt.Error)
	}
	if strings.Contains(stmt.SQL.String(), "tenant_id") {
		t.Fatalf("an uncontrolled table got a tenant condition: %s", stmt.SQL.String())
	}
}

func TestIgnoredTableIsNotControlled(t *testing.T) {
	t.Parallel()

	opts := defaultOptions()
	opts.IgnoreTables = []string{"scoped_rows"}
	db := newDB(t, opts)

	var rows []scopedRow
	stmt := dryRun(db, context.Background()).Find(&rows).Statement

	if stmt.Error != nil {
		t.Fatalf("an ignored table failed: %v", stmt.Error)
	}
	if strings.Contains(stmt.SQL.String(), "tenant_id` = ?") {
		t.Fatalf("an ignored table got a tenant condition: %s", stmt.SQL.String())
	}
}

// ── 逃逸口与安全闸 ──────────────────────────────────────────────────────

func TestBypassIsIgnoredWhileTheGateIsClosed(t *testing.T) {
	t.Parallel()

	db := newDB(t, defaultOptions())
	var rows []scopedRow
	ctx := tenant.WithBypass(context.Background())
	err := dryRun(db, ctx).Find(&rows).Error

	if !errors.Is(err, errNoIdentity) {
		t.Fatalf("error = %v, want errNoIdentity", err)
	}
	// 错误必须点名安全闸：否则写了绕过的人只会收到「没有身份」，
	// 无从知道是闸门拦下的。
	if !strings.Contains(err.Error(), "safety gate") {
		t.Fatalf("error %q does not name the safety gate", err)
	}
}

func TestBypassSkipsIsolationWhenAllowed(t *testing.T) {
	t.Parallel()

	opts := defaultOptions()
	opts.AllowBypass = true
	db := newDB(t, opts)

	var rows []scopedRow
	stmt := dryRun(db, tenant.WithBypass(context.Background())).Find(&rows).Statement

	if stmt.Error != nil {
		t.Fatalf("an allowed bypass failed: %v", stmt.Error)
	}
	if strings.Contains(stmt.SQL.String(), "tenant_id") {
		t.Fatalf("a bypassed query still carried a tenant condition: %s", stmt.SQL.String())
	}
}

// TestBypassAffectsOnlyOneCall 钉住绕过不会渗到下一次调用。
//
// 请求级绕过一旦成立，一次误用会让整条链路脱离隔离，而它在代码里只表现为
// 多了一行。这条判据钉住「单次调用粒度」这个契约。
func TestBypassAffectsOnlyOneCall(t *testing.T) {
	t.Parallel()

	opts := defaultOptions()
	opts.AllowBypass = true
	db := newDB(t, opts)

	base := acme()
	var rows []scopedRow

	// 第一次：显式绕过。
	first := dryRun(db, tenant.WithBypass(base)).Find(&rows).Statement
	if strings.Contains(first.SQL.String(), "tenant_id") {
		t.Fatalf("the bypassed call carried a tenant condition: %s", first.SQL.String())
	}

	// 第二次：用同一个基础 ctx，不带绕过标记。
	second := dryRun(db, base).Find(&rows).Statement
	if !strings.Contains(second.SQL.String(), "`tenant_id` = ?") {
		t.Fatalf("the tenant condition did not come back on the next call: %s", second.SQL.String())
	}
}

func TestIdentityCanBeSwitchedForOneCall(t *testing.T) {
	t.Parallel()

	db := newDB(t, defaultOptions())
	var rows []scopedRow
	stmt := dryRun(db, tenant.With(acme(), "globex")).Find(&rows).Statement

	if !containsVar(stmt.Vars, "globex") {
		t.Fatalf("the switched identity did not reach the bind vars: %#v", stmt.Vars)
	}
	if containsVar(stmt.Vars, "acme") {
		t.Fatalf("the original identity is still in the bind vars: %#v", stmt.Vars)
	}
}

// ── 扩展点 ──────────────────────────────────────────────────────────────

type recordingPolicy struct{ applied *int }

func (recordingPolicy) Name() string { return "recording" }

func (p recordingPolicy) Apply(Operation, *gorm.Statement, tenant.ID) error {
	*p.applied++
	return nil
}

func TestRegisteredPolicyParticipates(t *testing.T) {
	t.Parallel()

	db := newDB(t, defaultOptions())
	// 取回插件以注册策略：db.Use 之后插件实例仍由测试持有更直接，
	// 这里重新构造一份并单独装配。
	plugin, err := New(defaultOptions())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	applied := 0
	plugin.Register(recordingPolicy{applied: &applied})

	own := openMockDB(t)
	if useErr := own.Use(plugin); useErr != nil {
		t.Fatalf("db.Use: %v", useErr)
	}

	var rows []scopedRow
	dryRun(own, acme()).Find(&rows)
	if applied != 1 {
		t.Fatalf("registered policy applied %d times, want 1", applied)
	}

	// 未注册该策略的句柄不受影响。
	dryRun(db, acme()).Find(&rows)
	if applied != 1 {
		t.Fatalf("the policy ran on a handle it was not registered on (count=%d)", applied)
	}
}

// TestOnlyTenantPolicyIsBuiltIn 守住「不内置行/列规则」。
//
// 行与列的可见性规则必然业务特定。母版内置一套猜测的规则会被派生项目
// 当成可用默认值，而它保护的不是它们真实的组织结构。
func TestOnlyTenantPolicyIsBuiltIn(t *testing.T) {
	t.Parallel()

	plugin, err := New(defaultOptions())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(plugin.policies) != 1 {
		t.Fatalf("built-in policies = %d, want exactly 1 (tenant)", len(plugin.policies))
	}
	if name := plugin.policies[0].Name(); name != "tenant" {
		t.Fatalf("built-in policy = %q, want %q", name, "tenant")
	}
}

// ── 派生会话 ────────────────────────────────────────────────────────────

// TestIsolationSurvivesTransactionDerivedSessions 钉住回调随事务句柄生效。
//
// 这是整套方案的前提：仓储统一经 mdbCtx(ctx) 访问数据库，而它有两条派生路径——
// 无事务时 `fallback.WithContext(ctx)`，事务内是 `tx.WithContext(ctx)`。
// 依据是 Config.callbacks 为指针字段而 Session() 对 Config 做浅拷贝，
// 但那是读源码得出的结论；若哪天上游改成深拷贝，事务内的写就会**完全脱离隔离**，
// 而单元测试里非事务路径照样全绿。因此这一条必须有判据，不能只靠注释。
//
// WithContext 那条路径已被其余用例覆盖：dryRun 本身就是 Session + WithContext。
func TestIsolationSurvivesTransactionDerivedSessions(t *testing.T) {
	t.Parallel()

	db, mock := newDBWithMock(t, defaultOptions())
	mock.ExpectBegin()
	mock.ExpectCommit()

	var got string
	err := db.WithContext(acme()).Transaction(func(tx *gorm.DB) error {
		var rows []scopedRow
		stmt := tx.Session(&gorm.Session{DryRun: true}).Find(&rows).Statement
		got = stmt.SQL.String()
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}

	if !strings.Contains(got, "`tenant_id` = ?") {
		t.Fatalf("a statement inside a transaction lost the tenant condition: %s", got)
	}
}

// TestIsolationCoexistsWithAnotherPlugin 钉住与第二个 GORM 插件共存。
//
// 本仓的连接上已经有 otelgorm（它也在 Before("gorm:query") 挂点）。回调按名注册，
// 同名会互相覆盖——那正是本包给回调名加 `tenant:` 前缀的理由。这里用一个
// 同样挂在 gorm:query 前的探针插件代替 otelgorm：不引入观测依赖，
// 但验的是同一件事（两个插件的回调都执行，谁也没被顶掉）。
func TestIsolationCoexistsWithAnotherPlugin(t *testing.T) {
	t.Parallel()

	db := newDB(t, defaultOptions())
	probeRan := 0
	if err := db.Callback().Query().Before("gorm:query").
		Register("probe:before:select", func(*gorm.DB) { probeRan++ }); err != nil {
		t.Fatalf("register probe callback: %v", err)
	}

	var rows []scopedRow
	stmt := dryRun(db, acme()).Find(&rows).Statement

	if probeRan != 1 {
		t.Fatalf("the other plugin's callback ran %d times, want 1", probeRan)
	}
	if !strings.Contains(stmt.SQL.String(), "`tenant_id` = ?") {
		t.Fatalf("the tenant condition was lost when another callback shared the hook point: %s",
			stmt.SQL.String())
	}
}

// ── helpers ─────────────────────────────────────────────────────────────

func containsVar(vars []any, want string) bool {
	for _, v := range vars {
		if s, ok := v.(string); ok && s == want {
			return true
		}
	}
	return false
}

// whereFragment 取出 SQL 里 WHERE 之后的部分。
func whereFragment(t *testing.T, sql string) string {
	t.Helper()
	_, after, found := strings.Cut(sql, " WHERE ")
	if !found {
		t.Fatalf("SQL has no WHERE clause: %s", sql)
	}
	return after
}
