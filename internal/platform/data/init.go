package dao

import (
	"github.com/gtkit/gin-api/internal/platform/config"

	logger "github.com/gtkit/logger/v2"
	"github.com/gtkit/ormx/zlogger"
	gormlogger "gorm.io/gorm/logger"
)

// NewGormLogger 构造进程共用的 GORM 日志实现。
//
// 导出它而不是让各调用点自己拼 zlogger 选项：参数化查询是一条安全属性
// （日志里绝不出现手机号、邮箱等真实绑定值），复制一份选项就意味着
// 迟早有一份忘了开。server 与 migrate 两条路径共用同一个构造。
func NewGormLogger(cfg *config.Config) gormlogger.Interface {
	return zlogger.New(gormLoggerOptions(cfg)...)
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
