package bootstrap

import (
	"context"
	"errors"

	"github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/gin-api/internal/router"
)

// runtimeHealthChecker 实现 router.HealthChecker，基于进程级 Resources 探活。
//
// 探活直接走 Resources 而不是各自持有的连接句柄：句柄一旦被复制出去，
// "谁负责关闭"就不再有唯一答案，而探活恰恰是最容易在关停期仍持有旧句柄的地方。
type runtimeHealthChecker struct {
	resources *data.Resources
}

// 编译时接口实现检查。
var _ router.HealthChecker = (*runtimeHealthChecker)(nil)

func newHealthChecker(resources *data.Resources) router.HealthChecker {
	return &runtimeHealthChecker{resources: resources}
}

func (h *runtimeHealthChecker) PingDB(ctx context.Context) error {
	if h.resources == nil {
		return errors.New("database not initialized")
	}
	return h.resources.PingDB(ctx)
}

// RedisConfigured 区分未启用 Redis 与连接故障，供 readiness 跳过未配置项。
func (h *runtimeHealthChecker) RedisConfigured() bool {
	return h.resources != nil && h.resources.RedisConfigured()
}

// PingRedis 探测 Redis 并把结果计入可用性状态机。
//
// 结果计入状态机而不是只返回：readiness 探针是本进程对 Redis 最规律的一次调用，
// 丢掉它的观测结果，状态机就只能靠零星的业务调用来感知恢复。
func (h *runtimeHealthChecker) PingRedis(ctx context.Context) error {
	if h.resources == nil {
		return errors.New("redis not initialized")
	}
	return h.resources.PingRedis(ctx)
}
