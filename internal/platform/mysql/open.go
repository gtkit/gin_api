package mysql

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/ormx"
	gormlogger "gorm.io/gorm/logger"
)

// sessionTimeZone 是连接会话的固定时区。
//
// 固定 UTC 而不是跟随数据库默认值：同一份代码在不同 time_zone 的实例上，
// 时间列的读写会各自偏移，且这种偏移只在跨时区排查时才被发现。
const sessionTimeZone = "+00:00"

// OpenDB 建立一个数据库连接并完成启动期校验。
//
// 它是数据库连接的唯一建立入口，MySQL Owner、迁移与诊断复用它，不再另写一份。
//
// 凭据和日志由调用方传入；ormx 负责以启动 ctx 查询真实版本并初始化方言。
func OpenDB(
	ctx context.Context,
	dbCfg config.DatabaseConfig,
	creds config.DBCredentials,
	gormLogger gormlogger.Interface,
) (*ormx.Client, error) {
	return openDB(ctx, serverOpenOptions(dbCfg, creds), gormLogger)
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
// 去掉 deadline **不会**让连接建立阶段失去上界：Ping 与驱动版本初始化都经 ctx 执行，
// 调用方给的启动预算仍然生效。失去上界的只有本函数返回之后由迁移库执行的语句。
//
// 不要在服务进程路径上调用它：那里需要"单次网络操作上界"来保护请求 goroutine。
func OpenMigrationDB(
	ctx context.Context,
	dbCfg config.DatabaseConfig,
	creds config.DBCredentials,
	gormLogger gormlogger.Interface,
) (*ormx.Client, error) {
	return openDB(ctx, migrationOpenOptions(dbCfg, creds), gormLogger)
}

func openDB(ctx context.Context, opts []ormx.Option, gormLogger gormlogger.Interface) (*ormx.Client, error) {
	if gormLogger != nil {
		opts = append(opts, ormx.WithGormLogger(gormLogger))
	}
	client, err := ormx.Open(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := ctx.Err(); err != nil {
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
		ormx.WithPrepareStmtCache(
			dbCfg.EffectivePrepareStmtMaxSize(),
			time.Duration(dbCfg.EffectivePrepareStmtTTLSeconds())*time.Second),
		ormx.WithSkipDefaultTransaction(true),

		// 把驱动错误翻译成 GORM 的统一错误类型（gorm.ErrDuplicatedKey 等）。
		//
		// 没有它，仓储只能靠匹配驱动的错误码文本来区分"唯一键冲突"与"数据库
		// 出错"，而那个文本随驱动与数据库版本变化。这条判断决定的是回 409
		// 还是回 500——判错会把一次正常的重复提交报成服务端故障。
		ormx.WithTranslateError(true),

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

// closeAndWrap 关闭连接并把关闭错误附加到原始错误上。
//
// 取 closer 函数而非 *ormx.Client：该类型字段不导出、无法在测试里构造，
// 而"失败路径必须恰好关闭一次且不掩盖根因"正是这里最需要被验证的契约。
//
// 原始错误用 %w 保留可 errors.Is/As，关闭错误只作附加信息——
// 反过来会让调用方拿不到真正的失败原因。
func closeAndWrap(closer func() error, cause error) error {
	if closeErr := closer(); closeErr != nil {
		return errors.Join(cause, fmt.Errorf("close half-open connection: %w", closeErr))
	}
	return cause
}
