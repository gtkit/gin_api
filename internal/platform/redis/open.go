package redis

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/redisx"
)

// ProbeRedis 在调用方预算内复用运行期建连校验，并在检查完成后关闭连接。
func ProbeRedis(ctx context.Context, cfg config.RedisConfig) error {
	client, err := OpenRedis(ctx, cfg)
	if err != nil {
		return err
	}
	if err := client.Close(); err != nil {
		return fmt.Errorf("close redis probe: %w", err)
	}
	return nil
}

// OpenRedis 建立多 DB Redis 客户端并在 ctx 预算内完成一次连通性校验。
//
// 与旧实现的两处关键差异：
//
//  1. **不 panic**。旧实现在 NewClient 失败时 panic，于是一个 Redis 地址写错
//     就会跳过所有已建立资源的清理路径。初始化失败是配置/环境问题，
//     必须作为 error 沿装配链上抛。
//  2. **受 ctx 约束**。用 NewClientContext 而不是 NewClient：后者的建连没有
//     调用方可控的上限，Redis 地址可路由但无响应时，启动会挂在那里，
//     而编排器的启动探针已经超时并重启容器——形成重启循环。
//
// 建连成功后再显式 HealthCheck 一次：连接池是惰性的，"NewClient 返回成功"
// 不等于"这个地址上真的有一个可用的 Redis"。
//
// **拿不到可重连的客户端是库的约束**：redisx.NewClientContext 在构造期对每个 DB
// 执行 Ping，且 DefaultDB 必须成功、否则它回收全部连接返回 nil。因此服务端整体
// 不可达时本函数无法交出一个"稍后能自愈"的客户端，探测失败即关闭并返回 nil。
// 消费方要区分"未配置"与"已配置但不可达"，用 Resources.RedisConfigured()
// 而不是客户端指针的非空性——见那里的注释。
func OpenRedis(ctx context.Context, cfg config.RedisConfig) (*redisx.Client, error) {
	client, err := redisx.NewClientContext(ctx, buildRedisOptions(cfg)...)
	if err != nil {
		return nil, fmt.Errorf("init redis client: %w", err)
	}

	if err := client.HealthCheck(ctx); err != nil {
		// 本函数在返回成功之前仍然拥有该客户端：校验失败必须在这里关闭，
		// 否则连接池泄漏——调用方拿到的是 nil，没有可关闭的句柄。
		return nil, closeAndWrap(client.Close, fmt.Errorf("redis health check: %w", err))
	}

	return client, nil
}

// buildRedisOptions 把配置翻译成 redisx 选项。
//
// 池与超时项只在**显式配置**时才传递：留空表示采用 redisx 自身的默认值，
// 在本层复制一份默认值会与上游漂移，而漂移的那一刻没有任何地方会报错。
//
// 不传 WithKeyPrefix：key 原样直通，与既有部署的数据保持一致。
func buildRedisOptions(cfg config.RedisConfig) []redisx.Option {
	opts := []redisx.Option{
		redisx.WithAddr(cfg.Addr),
		redisx.WithPassword(cfg.Password),
		redisx.WithInitDBs(cfg.DBs...),
	}

	if cfg.PoolSize > 0 {
		opts = append(opts, redisx.WithPoolSize(cfg.PoolSize))
	}
	if cfg.MinIdleConns > 0 {
		opts = append(opts, redisx.WithMinIdleConns(cfg.MinIdleConns))
	}
	if cfg.DialTimeoutMS > 0 {
		opts = append(opts, redisx.WithDialTimeout(time.Duration(cfg.DialTimeoutMS)*time.Millisecond))
	}
	if cfg.ReadTimeoutMS > 0 {
		opts = append(opts, redisx.WithReadTimeout(time.Duration(cfg.ReadTimeoutMS)*time.Millisecond))
	}
	if cfg.WriteTimeoutMS > 0 {
		opts = append(opts, redisx.WithWriteTimeout(time.Duration(cfg.WriteTimeoutMS)*time.Millisecond))
	}

	return opts
}

func closeAndWrap(closer func() error, cause error) error {
	if err := closer(); err != nil {
		return errors.Join(cause, fmt.Errorf("close half-open connection: %w", err))
	}
	return cause
}
