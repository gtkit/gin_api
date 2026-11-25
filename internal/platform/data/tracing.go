package data

import (
	"errors"
	"fmt"

	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	"github.com/uptrace/opentelemetry-go-extra/otelgorm"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
)

// InstrumentTracing installs data tracing during assembly, after telemetry initializes.
func (r *Resources) InstrumentTracing(dbName string) error {
	if r == nil || r.Mdb() == nil {
		return errors.New("instrument tracing: mysql connection is not initialized")
	}
	if err := InstrumentDatabaseTracing(r.Mdb(), dbName); err != nil {
		return err
	}
	return InstrumentRedisTracing(r.redis)
}

// InstrumentDatabaseTracing attaches the existing safe GORM plugin during assembly.
func InstrumentDatabaseTracing(db *gorm.DB, dbName string) error {
	if db == nil {
		return errors.New("instrument gorm tracing: database is not initialized")
	}
	if err := instrumentGorm(db, dbName, nil); err != nil && !errors.Is(err, gorm.ErrRegistered) {
		return fmt.Errorf("instrument gorm tracing: %w", err)
	}
	return nil
}

// InstrumentRedisTracing instruments an owned Redis connection set once.
func InstrumentRedisTracing(handle RedisHandle) error {
	if handle == nil {
		// Redis 未配置或没有可恢复的句柄：没有可挂载的客户端。
		// 这不是错误——可降级能力的缺席不该阻断启动（resource-lifecycle spec）。
		return nil
	}
	if owner, ok := handle.(interface{ ConfigureTracing() error }); ok {
		return owner.ConfigureTracing()
	}

	// DB 集合经 PoolStats 的键枚举：那是 redisx 实际初始化的集合。
	// 不从配置的 DBs 推导——列表为空时 redisx 落到它自己的默认 DB，
	// 在本层复制这条默认值会与上游漂移（与 buildRedisOptions 不复制
	// 池默认值是同一条理由）。
	for db := range handle.PoolStats() {
		client, ok := handle.GetClient(db)
		if !ok {
			return fmt.Errorf("instrument redis tracing: db %d reported by pool stats but has no client", db)
		}
		if err := InstrumentRedisClient(client); err != nil {
			return fmt.Errorf("instrument redis tracing (db %d): %w", db, err)
		}
	}
	return nil
}

// InstrumentRedisClient installs tracing with the same fixed safe options used
// during startup. Owners must call it once, before publishing each client.
func InstrumentRedisClient(client *redis.Client) error {
	if client == nil {
		return errors.New("instrument redis tracing: nil client")
	}
	return instrumentRedis(client, nil)
}

// instrumentGorm 挂 otelgorm 插件。
//
// 选项写死在这里而不是交由调用方拼装，因为其中两项是不可让渡的：
//
//   - WithoutQueryVariables 是安全属性，与 SQL 日志的参数化脱敏同源——手机号、
//     邮箱等敏感值经绑定参数流动，span 与日志一样是采集面，任何载体都不落真实参数；
//   - WithoutMetrics 保住连接池指标的单一属主（metrics.Recorder）：otelgorm 默认
//     会在全局 MeterProvider 上再注册一份 DBStats，同一池子双报会让看板出现
//     两组数值不一致的同义指标。
//
// tp 仅供测试注入采集用 provider；nil 走全局（生产路径）。测试与生产共用这份
// 选项构造代码，反证测试才对生产路径有效。
func instrumentGorm(db *gorm.DB, dbName string, tp trace.TracerProvider) error {
	opts := []otelgorm.Option{
		otelgorm.WithoutQueryVariables(),
		otelgorm.WithoutMetrics(),
		otelgorm.WithDBName(dbName),
	}
	if tp != nil {
		opts = append(opts, otelgorm.WithTracerProvider(tp))
	}
	return db.Use(otelgorm.NewPlugin(opts...))
}

// instrumentRedis 给单个 DB 的客户端挂 redisotel 追踪 hook。
//
// WithDBStatement(false) 是安全属性：令牌撤销、验证码等敏感值以 Redis 命令参数
// 流动，语句进 span 即泄漏。redisotel 不导出 hook 类型，无法经 redisx 的 AddHook
// 一次挂全，故按 DB 逐客户端挂载——副产品是每个 span 携带所属 DB 的连接属性。
func instrumentRedis(client *redis.Client, tp trace.TracerProvider) error {
	opts := []redisotel.TracingOption{
		redisotel.WithDBStatement(false),
	}
	if tp != nil {
		opts = append(opts, redisotel.WithTracerProvider(tp))
	}
	return redisotel.InstrumentTracing(client, opts...)
}
