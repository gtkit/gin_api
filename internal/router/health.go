package router

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	logger "github.com/gtkit/logger/v2"
	"go.uber.org/zap"
)

// readyDepTimeout 是每个依赖检查各自的超时预算。
// 每个依赖独立计时，慢依赖不会吃掉其它依赖的预算。
const readyDepTimeout = 2 * time.Second

// 探针的请求级超时预算。
//
// 刻意比业务路由短得多：探针的意义是快速回答"我还行吗"，
// 一个要等好几秒才失败的探针会让编排器的判定同样迟钝，摘流也就跟着变慢。
const (
	healthProbeDeadline   = 3 * time.Second
	healthProbeWriteGrace = 2 * time.Second
)

// 对外只暴露固定状态标签，绝不回传底层错误串。
const (
	statusOK          = "ok"
	statusUnavailable = "unavailable"
)

// HealthChecker 定义健康探针需要的外部依赖检查能力。
// bootstrap 层负责把 DB / Redis 的 Ping 注入进来。
type HealthChecker interface {
	PingDB(ctx context.Context) error
	PingRedis(ctx context.Context) error
}

func healthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
	})
}

func readyHandler(checker HealthChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		// fail-closed：未配置 checker 属装配错误，报"未就绪"而非"就绪"，
		// 避免错误装配的实例仍被纳入流量。
		if checker == nil {
			logger.Warn("readiness checker not configured; reporting not ready")
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": statusUnavailable})
			return
		}

		parent := c.Request.Context()
		checks := gin.H{
			"database": checkDependency(parent, "database", checker.PingDB),
			"redis":    checkDependency(parent, "redis", checker.PingRedis),
		}

		status := http.StatusOK
		result := statusOK
		if checks["database"] != statusOK || checks["redis"] != statusOK {
			status = http.StatusServiceUnavailable
			result = statusUnavailable
		}

		c.JSON(status, gin.H{
			"status": result,
			"checks": checks,
		})
	}
}

// checkDependency 用独立超时执行单个依赖检查。
// 失败时把根因记进服务端日志，对外只返回固定标签，绝不泄漏底层错误串。
func checkDependency(parent context.Context, name string, ping func(context.Context) error) string {
	ctx, cancel := context.WithTimeout(parent, readyDepTimeout)
	defer cancel()

	if err := ping(ctx); err != nil {
		logger.Error("readiness dependency check failed",
			zap.String("dependency", name), zap.Error(err))
		return statusUnavailable
	}
	return statusOK
}
