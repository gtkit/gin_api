package dao

import (
	"context"
	"errors"
	"time"

	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/platform/config"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// NewGormLogger uses the selected project logger. Parameters are always omitted
// at every SQL log level, including errors and slow queries.
func NewGormLogger(cfg *config.Config) gormlogger.Interface {
	level := gormlogger.Warn
	threshold := (config.LogConfig{}).EffectiveSQLSlowThreshold()
	if cfg != nil && cfg.Log.SQL != 0 {
		level = gormlogger.Info
	}
	if cfg != nil {
		threshold = cfg.Log.EffectiveSQLSlowThreshold()
	}
	return &gormLogger{logLevel: level, slowThreshold: threshold}
}

type gormLogger struct {
	logLevel      gormlogger.LogLevel
	slowThreshold time.Duration
}

var _ gormlogger.Interface = (*gormLogger)(nil)

// LogMode 返回一个使用指定日志级别的副本，原 logger 不受影响。
func (l *gormLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	clone := *l
	clone.logLevel = level
	return &clone
}

// Info 在日志级别不低于 Info 时按 Printf 风格输出 Info 日志，附带 source 调用位置。
func (l *gormLogger) Info(ctx context.Context, msg string, data ...any) {
	if l.logLevel < gormlogger.Info {
		return
	}
	l.emit(ctx, applog.LevelInfo, msg, data)
}

// Warn 在日志级别不低于 Warn 时按 Printf 风格输出 Warn 日志，附带 source 调用位置。
func (l *gormLogger) Warn(ctx context.Context, msg string, data ...any) {
	if l.logLevel < gormlogger.Warn {
		return
	}
	l.emit(ctx, applog.LevelWarn, msg, data)
}

// Error 在日志级别不低于 Error 时按 Printf 风格输出 Error 日志，附带 source 调用位置。
func (l *gormLogger) Error(ctx context.Context, msg string, data ...any) {
	if l.logLevel < gormlogger.Error {
		return
	}
	l.emit(ctx, applog.LevelError, msg, data)
}

// Trace 记录一次 SQL 执行：出错时（除被忽略的 gorm.ErrRecordNotFound 外）输出 Error 日志；
// 慢查询阈值非 0 且耗时超过阈值时输出 Warn 慢查询日志；级别为 Info 时输出普通查询日志；
// 级别为 Silent 时不输出。日志字段包含调用位置、耗时、SQL、影响行数及可选的 trace_id。
// 无需输出日志时不会调用 fc，避免正常快查询产生 SQL 格式化开销。
func (l *gormLogger) Trace(
	ctx context.Context, begin time.Time,
	fc func() (sql string, rowsAffected int64), err error,
) {
	if l.logLevel <= gormlogger.Silent {
		return
	}

	elapsed := time.Since(begin)
	recordNotFoundIgnored := errors.Is(err, gorm.ErrRecordNotFound)

	switch {
	case err != nil && l.logLevel >= gormlogger.Error && !recordNotFoundIgnored:
		if !applog.Enabled(applog.LevelError, applog.MainStream) {
			return
		}
		sql, rows := fc()
		fields := l.traceFields(ctx, elapsed, sql, rows)
		applog.Log(ctx, applog.LogEntry{Level: applog.LevelError, Message: "gorm query error", Fields: append(fields, applog.Err(err)), CallerSkip: 1})
	case l.slowThreshold > 0 && elapsed > l.slowThreshold && l.logLevel >= gormlogger.Warn:
		if !applog.Enabled(applog.LevelWarn, applog.MainStream) {
			return
		}
		sql, rows := fc()
		fields := l.traceFields(ctx, elapsed, sql, rows)
		applog.Log(ctx, applog.LogEntry{Level: applog.LevelWarn, Message: "gorm slow query", Fields: append(fields, applog.Duration("slow_threshold", l.slowThreshold)), CallerSkip: 1})
	case l.logLevel == gormlogger.Info:
		if !applog.Enabled(applog.LevelInfo, applog.MainStream) {
			return
		}
		sql, rows := fc()
		fields := l.traceFields(ctx, elapsed, sql, rows)
		applog.Log(ctx, applog.LogEntry{Level: applog.LevelInfo, Message: "gorm query", Fields: fields, CallerSkip: 1})
	}
}

// ParamsFilter prevents real bound values from entering SQL diagnostics.
func (*gormLogger) ParamsFilter(_ context.Context, sql string, _ ...any) (string, []any) {
	return sql, nil
}

func (l *gormLogger) emit(ctx context.Context, level applog.Level, message string, args []any) {
	if !applog.Enabled(level, applog.MainStream) {
		return
	}
	fields := []applog.Field{applog.String("source", callerSource())}
	if id := applog.RequestIDFromContext(ctx); id != "" {
		fields = append(fields, applog.String("trace_id", id))
	}
	applog.Log(ctx, applog.LogEntry{Level: level, Message: message, Format: true, Args: args, Fields: fields, CallerSkip: 2})
}

// traceFields 组装 Trace 日志的公共字段。
func (l *gormLogger) traceFields(ctx context.Context, elapsed time.Duration, sql string, rows int64) []applog.Field {
	const maxTraceFields = 6
	fields := make([]applog.Field, 0, maxTraceFields)
	fields = append(fields,
		applog.String("source", callerSource()),
		applog.Duration("elapsed", elapsed),
		applog.String("sql", sql),
	)
	if rows >= 0 {
		fields = append(fields, applog.Int64("rows", rows))
	}
	if traceID := applog.RequestIDFromContext(ctx); traceID != "" {
		fields = append(fields, applog.String("trace_id", traceID))
	}
	return fields
}
