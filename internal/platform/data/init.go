package dao

import (
	"context"
	"fmt"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"

	logger "github.com/gtkit/logger/v2"
	"github.com/gtkit/ormx"
	"github.com/gtkit/ormx/zlogger"
	"github.com/gtkit/redisx"
	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// NewWithConfig 使用 typed config 初始化数据层。
func NewWithConfig(cfg *config.Config) {
	daoDB = &dao{
		rdb: initRedisWithConfig(cfg),
		mdb: initMysqlWithConfig(cfg),
	}
}

// gormLoggerOptions 构造 GORM zlogger 选项。
// WithParameterizedQueries 无条件开启：zlogger 默认级别为 Warn，即使 log.sql=0，
// 错误与慢查询日志也会输出 SQL，参数化保证任何级别都不落真实绑定参数
// （手机号、邮箱等敏感值）。log.sql 只控制是否逐条输出 SQL（Info 级）。
func gormLoggerOptions(cfg *config.Config) []zlogger.Option {
	opts := []zlogger.Option{
		zlogger.WithLogger(logger.Default().Zap()),
		// SQL 日志携带与访问/业务日志同源的 requestId（字段名 trace_id），串联单请求全链路。
		zlogger.WithTraceIDExtractor(logger.RequestIDFromContext),
		// record not found 属正常业务分支（如查无待支付订单），不应打成 ERROR。
		zlogger.WithIgnoreRecordNotFoundError(true),
		zlogger.WithParameterizedQueries(true),
	}
	if cfg.Log.SQL != 0 {
		// ormx/zlogger 移除了 WithSqlLog，Info 级别等价于旧版逐条输出 SQL。
		opts = append(opts, zlogger.WithLogLevel(gormlogger.Info))
	}

	return opts
}

func initMysqlWithConfig(cfg *config.Config) *gorm.DB {
	loggeropt := gormLoggerOptions(cfg)

	// MustOpen 与旧版 orm.NewMysql 一致：启动连不上数据库直接 panic 快速退出。
	client := ormx.MustOpen(context.Background(),
		ormx.WithHost(cfg.Database.Host),
		ormx.WithPort(cfg.Database.Port),
		ormx.WithDatabase(cfg.Database.Name),
		ormx.WithUser(cfg.Database.Username),
		ormx.WithPassword(cfg.Database.Password),
		ormx.WithPrepareStmt(true),
		ormx.WithSkipDefaultTransaction(true),
		ormx.WithGormLogger(zlogger.New(loggeropt...)),
		ormx.WithMaxOpenConns(cfg.Database.MaxOpenConn),
		ormx.WithMaxIdleConns(cfg.Database.MaxIdleConn),
		ormx.WithConnMaxLifetime(time.Duration(cfg.Database.MaxLifeSeconds)*time.Second),
	)

	logger.Info("mysql connect success",
		zap.String("host", cfg.Database.Host),
		zap.String("database", cfg.Database.Name))
	return client.DB()
}

// initRedisWithConfig 初始化多 DB Redis 客户端。
// 不传 WithKeyPrefix，保持 key 原样直通，与旧版 gtkit/redis 行为一致。
// 与 MySQL 的 MustOpen 一致：启动连不上 Redis 直接 panic 快速退出。
func initRedisWithConfig(cfg *config.Config) *redisx.Client {
	client, err := redisx.NewClient(
		redisx.WithAddr(cfg.Redis.Addr),
		redisx.WithPassword(cfg.Redis.Password),
		redisx.WithInitDBs(cfg.Redis.DBs...),
	)
	if err != nil {
		panic(fmt.Errorf("init redis client: %w", err))
	}

	logger.Info("redis connect success", zap.Ints("dbs", cfg.Redis.DBs))
	return client
}
