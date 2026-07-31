package dao

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gtkit/ormx"

	"github.com/gtkit/gin-api/internal/platform/config"
)

// sessionTimeZone 是连接会话的固定时区。
//
// 固定 UTC 而不是跟随数据库默认值：同一份代码在不同 time_zone 的实例上，
// 时间列的读写会各自偏移，且这种偏移只在跨时区排查时才被发现。
const sessionTimeZone = "+00:00"

// strictSQLModes 是可接受的严格模式之一。
//
// 非严格模式下 MySQL 会静默截断超长值、把非法值隐式纠正为默认值——
// 于是"写入成功"不再等于"写入正确"，而应用层拿不到任何错误。
var strictSQLModes = []string{"STRICT_TRANS_TABLES", "STRICT_ALL_TABLES"}

// OpenDB 建立一个数据库连接并完成启动期校验。
//
// 它是数据库连接的唯一建立入口，资源生命周期批次的 NewResources 直接复用它，不再另写一份。
//
// 三个设计要点：
//
//  1. **凭据由调用方解析后传入**，本函数不从整份配置里猜用途——同一份配置内同时存在
//     运行凭据与迁移凭据，底层无从判断本次该用哪个。
//  2. **跳过 GORM 的版本探测，同时自己查一次真实版本**。ormx 文档明确警告：ctx 只约束
//     启动 Ping，GORM 初始化的 SELECT VERSION() 由驱动以内部 background context 执行，
//     不受 ctx 约束。跳过它消除这次不可控往返；但 WithServerVersion 只是告诉 GORM 按该
//     版本配置方言、**不验证数据库真是该版本**，所以必须自己补一次受 ctx 约束的校验。
//     只做前一半会把"版本不符"从启动报错变成运行期怪异 SQL 行为——比不跳过更糟。
//  3. **返回成功之前本函数仍然拥有该连接**：任何 post-open 校验失败都必须在函数内部关闭
//     底层连接后再返回，否则连接池泄漏——调用方拿到的是 nil，没有可关闭的句柄。
func OpenDB(ctx context.Context, dbCfg config.DatabaseConfig, creds config.DBCredentials) (*ormx.Client, error) {
	minVersion, err := parseMinVersion(dbCfg.EffectiveMinVersion())
	if err != nil {
		return nil, fmt.Errorf("invalid database.min_version: %w", err)
	}

	client, err := ormx.Open(ctx, buildOpenOptions(dbCfg, creds)...)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// 自此起本函数拥有 client，任何失败路径都必须先关闭它。
	if err := verifyServerVersion(ctx, client, minVersion); err != nil {
		return nil, closeAndWrap(client.Close, err)
	}
	if err := verifySessionSemantics(ctx, client, dbCfg); err != nil {
		return nil, closeAndWrap(client.Close, err)
	}
	return client, nil
}

func buildOpenOptions(dbCfg config.DatabaseConfig, creds config.DBCredentials) []ormx.Option {
	// 读写超时是"单次网络操作"的上界，不该跟连接寿命同量级。
	readWriteTimeout := min(time.Duration(dbCfg.EffectiveMaxLifeSeconds())*time.Second, 30*time.Second)

	return []ormx.Option{
		ormx.WithHost(dbCfg.Host),
		ormx.WithPort(dbCfg.Port),
		ormx.WithDatabase(dbCfg.Name),
		ormx.WithUser(creds.Username),
		ormx.WithPassword(creds.Password),
		ormx.WithPrepareStmt(true),
		ormx.WithSkipDefaultTransaction(true),

		// 跳过 GORM 自己的版本探测（那次往返不受 ctx 约束），改由本函数显式校验。
		ormx.WithSkipInitializeWithVersion(true),
		ormx.WithServerVersion(dbCfg.EffectiveMinVersion()),
		// 读写超时兜住其余不受 ctx 约束的路径。
		ormx.WithReadTimeout(readWriteTimeout),
		ormx.WithWriteTimeout(readWriteTimeout),

		// 会话语义固定，不依赖数据库环境默认值。
		ormx.WithParseTime(true),
		ormx.WithLocation(time.UTC),
		ormx.WithCharset(dbCfg.EffectiveCharset()),
		ormx.WithCollation(dbCfg.EffectiveCollation()),
		ormx.WithSystemVariable("time_zone", "'"+sessionTimeZone+"'"),

		ormx.WithMaxOpenConns(dbCfg.EffectiveMaxOpenConn()),
		ormx.WithMaxIdleConns(dbCfg.EffectiveMaxIdleConn()),
		ormx.WithConnMaxLifetime(time.Duration(dbCfg.EffectiveMaxLifeSeconds()) * time.Second),
		ormx.WithConnMaxIdleTime(time.Duration(dbCfg.EffectiveMaxIdleTimeSeconds()) * time.Second),
	}
}

// verifyServerVersion 实测数据库的 vendor 与版本。
func verifyServerVersion(ctx context.Context, client *ormx.Client, minVersion serverVersion) error {
	var row struct {
		Version string
		Comment string
	}
	err := client.DB().WithContext(ctx).
		Raw("SELECT VERSION() AS version, @@version_comment AS comment").
		Scan(&row).Error
	if err != nil {
		return fmt.Errorf("query server version: %w", err)
	}

	actual, err := parseServerVersion(row.Version, row.Comment)
	if err != nil {
		return err
	}
	// vendor 先判：MariaDB 的 10.x 数值上大于 MySQL 的 8.0，只比数字会误放行。
	if !actual.vendor.supported() {
		return fmt.Errorf(
			"unsupported database vendor: got %s; only Oracle MySQL and Percona Server are supported "+
				"(enabling another vendor requires a dedicated compatibility change with its own minimum version line)",
			actual)
	}
	if !actual.atLeast(minVersion) {
		return fmt.Errorf("database version %s is below the minimum supported version %s", actual, minVersion.raw)
	}
	return nil
}

// verifySessionSemantics 校验实际生效的会话语义。
//
// 这些值即使在 DSN 里声明过也必须回读确认：服务端可能因权限、代理层或版本差异
// 未按声明生效，而不匹配的后果（时间偏移、排序差异、静默截断）都是静默的。
func verifySessionSemantics(ctx context.Context, client *ormx.Client, dbCfg config.DatabaseConfig) error {
	var row struct {
		TimeZone  string
		Charset   string
		Collation string
		SQLMode   string
	}
	err := client.DB().WithContext(ctx).Raw(
		"SELECT @@session.time_zone AS time_zone, " +
			"@@session.character_set_connection AS charset, " +
			"@@session.collation_connection AS collation, " +
			"@@session.sql_mode AS sql_mode").
		Scan(&row).Error
	if err != nil {
		return fmt.Errorf("query session semantics: %w", err)
	}

	var errs []error
	if row.TimeZone != sessionTimeZone {
		errs = append(errs, fmt.Errorf(
			"session time_zone is %q, want %q", row.TimeZone, sessionTimeZone))
	}
	if want := dbCfg.EffectiveCharset(); row.Charset != want {
		errs = append(errs, fmt.Errorf("connection charset is %q, want %q", row.Charset, want))
	}
	if want := dbCfg.EffectiveCollation(); row.Collation != want {
		errs = append(errs, fmt.Errorf("connection collation is %q, want %q", row.Collation, want))
	}
	if !hasStrictSQLMode(row.SQLMode) {
		errs = append(errs, fmt.Errorf(
			"sql_mode %q contains no strict mode (need one of %s): "+
				"without it MySQL silently truncates oversized values and coerces invalid ones, "+
				"so a successful write no longer means a correct write",
			row.SQLMode, strings.Join(strictSQLModes, " or ")))
	}
	return errors.Join(errs...)
}

func hasStrictSQLMode(sqlMode string) bool {
	for mode := range strings.SplitSeq(sqlMode, ",") {
		if slicesContainsFold(strictSQLModes, strings.TrimSpace(mode)) {
			return true
		}
	}
	return false
}

func slicesContainsFold(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.EqualFold(s, needle) {
			return true
		}
	}
	return false
}

// closeAndWrap 关闭连接并把关闭错误附加到原始错误上。
//
// 取 closer 函数而非 *ormx.Client：该类型字段不导出、无法在测试里构造，
// 而"失败路径必须恰好关闭一次且不掩盖根因"正是这里最需要被验证的契约。
//
// 原始错误用 %w 保留可 errors.Is/As，关闭错误只作附加信息——
// 反过来会让调用方拿不到真正的失败原因。
func closeAndWrap(closer func() error, cause error) error {
	if closeErr := closer(); closeErr != nil {
		return fmt.Errorf("%w (additionally, closing the half-open connection failed: %v)", cause, closeErr)
	}
	return cause
}
