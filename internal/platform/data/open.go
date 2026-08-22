package dao

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gtkit/ormx"
	gormlogger "gorm.io/gorm/logger"

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
//  3. **GORM logger 经参数注入**：日志配置不属于数据库配置，本函数不从整份配置里取。
//     传 nil 表示不挂 logger（迁移等一次性场景）。
//  4. **返回成功之前本函数仍然拥有该连接**：任何 post-open 校验失败都必须在函数内部关闭
//     底层连接后再返回，否则连接池泄漏——调用方拿到的是 nil，没有可关闭的句柄。
func OpenDB(
	ctx context.Context,
	dbCfg config.DatabaseConfig,
	creds config.DBCredentials,
	gormLogger gormlogger.Interface,
) (*ormx.Client, error) {
	return openAndVerify(ctx, dbCfg, serverOpenOptions(dbCfg, creds), gormLogger)
}

// OpenMigrationDB 为**迁移**建立连接：除驱动级 I/O deadline 外与 OpenDB 完全一致。
//
// 单独具名而不是给 OpenDB 加参数，是为了让"本次用的是哪种超时语义"在调用点可读。
// 此前两条路径共用一个入口，迁移因此静默继承了 server 的 30s 上限，而调用点
// 完全看不出这件事——那正是本入口要消除的形态。
//
// **迁移路径不施加驱动级 I/O deadline**，理由是失效形态不对称：驱动的读 deadline
// 作用于"发出语句后等待响应包"，对 DDL 等价于给单条语句的执行时长封顶；而 MySQL
// 的 DDL 非事务性、**不随客户端断连回滚**。于是一次超时留下的是"schema 已变更、
// 迁移版本表未记录"——迁移命令报失败，数据库侧却已完成，且空库上的迁移验证
// 永远复现不出来。相比之下迁移是一次性前台命令，墙钟上界由操作者与 CI 承担：
// 一次可见的挂起优于一次静默的 schema 分叉。
//
// 去掉 deadline **不会**让连接建立阶段失去上界：Ping 与两次会话校验都经 ctx 执行，
// 调用方给的启动预算仍然生效。失去上界的只有本函数返回之后由迁移库执行的语句。
//
// 不要在服务进程路径上调用它：那里需要"单次网络操作上界"来保护请求 goroutine。
func OpenMigrationDB(
	ctx context.Context,
	dbCfg config.DatabaseConfig,
	creds config.DBCredentials,
	gormLogger gormlogger.Interface,
) (*ormx.Client, error) {
	return openAndVerify(ctx, dbCfg, migrationOpenOptions(dbCfg, creds), gormLogger)
}

// openAndVerify 是两个入口共用的建立与启动校验流程。
//
// 两个入口的差异只在传入的 opts 上，其余步骤共用同一份代码——否则"除超时外
// 完全一致"就得靠人逐行比对两份拷贝来维持。
func openAndVerify(
	ctx context.Context,
	dbCfg config.DatabaseConfig,
	opts []ormx.Option,
	gormLogger gormlogger.Interface,
) (*ormx.Client, error) {
	minVersion, err := parseMinVersion(dbCfg.EffectiveMinVersion())
	if err != nil {
		return nil, fmt.Errorf("invalid database.min_version: %w", err)
	}

	if gormLogger != nil {
		// GORM logger 由调用方注入而不是在本函数内构造：它需要 log 配置，
		// 而本函数只接收数据库配置——把整份 *Config 传进来就等于让底层去猜用途，
		// 那正是凭据选择要避免的形态。
		opts = append(opts, ormx.WithGormLogger(gormLogger))
	}

	client, err := ormx.Open(ctx, opts...)
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

// serverIOTimeoutCap 是服务进程路径的驱动 I/O deadline 上限。
const serverIOTimeoutCap = 30 * time.Second

// migrationIOTimeout 表示不施加驱动级 I/O deadline。
//
// 0 是这件事在驱动层的表达而不是"回落默认值"：ormx 的 option 覆盖 DefaultConfig
// 的 30s，go-sql-driver 在 readTimeout <= 0 时不设读 deadline。
const migrationIOTimeout time.Duration = 0

// serverIOTimeout 是服务进程路径的驱动 I/O deadline。
//
// 取连接寿命与上限中的较小值：读写超时是"单次网络操作"的上界，不该跟连接寿命
// 同量级；而寿命本身更短时，用比寿命更长的 deadline 没有意义。
func serverIOTimeout(dbCfg config.DatabaseConfig) time.Duration {
	return min(time.Duration(dbCfg.EffectiveMaxLifeSeconds())*time.Second, serverIOTimeoutCap)
}

// serverOpenOptions 是服务进程路径的连接选项。
func serverOpenOptions(dbCfg config.DatabaseConfig, creds config.DBCredentials) []ormx.Option {
	return buildOpenOptions(dbCfg, creds, serverIOTimeout(dbCfg))
}

// migrationOpenOptions 是迁移路径的连接选项。
func migrationOpenOptions(dbCfg config.DatabaseConfig, creds config.DBCredentials) []ormx.Option {
	return buildOpenOptions(dbCfg, creds, migrationIOTimeout)
}

// buildOpenOptions 组装连接选项。
//
// ioTimeout 由调用方按用途给出，本函数不再替所有用途算一个值：server 与迁移
// 对"语句被客户端掐断"的容忍度相反，而此前的单一公式让调用点看不出这件事。
func buildOpenOptions(
	dbCfg config.DatabaseConfig,
	creds config.DBCredentials,
	ioTimeout time.Duration,
) []ormx.Option {
	return []ormx.Option{
		ormx.WithHost(dbCfg.Host),
		ormx.WithPort(dbCfg.Port),
		ormx.WithDatabase(dbCfg.Name),
		ormx.WithUser(creds.Username),
		ormx.WithPassword(creds.Password),
		ormx.WithPrepareStmt(true),
		ormx.WithSkipDefaultTransaction(true),

		// 把驱动错误翻译成 GORM 的统一错误类型（gorm.ErrDuplicatedKey 等）。
		//
		// 没有它，仓储只能靠匹配驱动的错误码文本来区分"唯一键冲突"与"数据库
		// 出错"，而那个文本随驱动与数据库版本变化。这条判断决定的是回 409
		// 还是回 500——判错会把一次正常的重复提交报成服务端故障。
		ormx.WithTranslateError(true),

		// 跳过 GORM 自己的版本探测（那次往返不受 ctx 约束），改由本函数显式校验。
		ormx.WithSkipInitializeWithVersion(true),
		ormx.WithServerVersion(dbCfg.EffectiveMinVersion()),
		// 读写超时兜住其余不受 ctx 约束的路径；取值由调用方按用途决定，
		// 迁移路径传 0 表示不施加（见 OpenMigrationDB）。
		ormx.WithReadTimeout(ioTimeout),
		ormx.WithWriteTimeout(ioTimeout),

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
