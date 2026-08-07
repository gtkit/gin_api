package bootstrap

import (
	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
)

// resourceRequirements 由**已启用的能力集合**推导本进程对可选资源的必需性。
//
// 为什么不用一个独立的 `redis.required` 配置项：那个开关必然与真实依赖漂移。
// 有人打开了一个依赖 Redis 的能力却忘了同步打开开关，于是 Redis 挂掉时
// 进程照常报告 ready，而那个能力已经不工作了；反过来把开关一直开着，
// 又会让不用 Redis 的部署因为一个用不到的依赖而起不来。
//
// 判据只能来自"谁真的要用它"。新增依赖 Redis 的能力时，在下面的表里加一行——
// 这也是唯一需要改的地方。
func resourceRequirements(cfg *config.Config) data.Requirements {
	return data.Requirements{
		Redis: redisRequired(cfg),
	}
}

// redisRequired 报告当前配置下是否存在必须依赖 Redis 的已启用能力。
//
// 判据只能来自"谁真的要用它"：下面的能力表每行一个依赖 Redis 的已启用能力，
// 派生项目引入新依赖时在表里加一行，它就自动成为 required，
// 无需改动 Resources 或启动流程。
func redisRequired(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}

	// 本地认证：刷新令牌存储、访问令牌撤销表与清理任务的单实例租约
	// 全部以 Redis 为载体，Redis 不可用即认证不可用——此时实例不该被
	// 判为 ready，否则 readiness 在对认证故障撒谎。
	if cfg.Auth.LocalEnabled() {
		return true
	}

	// 限流：只有 `store: redis` **且** `failure_mode: fail_closed` 的策略
	// 才让 Redis 成为 required——那种组合下 Redis 挂掉时该策略只能拒绝请求，
	// 实例不该被判为 ready。其余组合本来就有降级路径，把 Redis 抬成 required
	// 会让一次可降级的故障升级成实例被摘流。
	if redisRequiredForRateLimit(cfg) {
		return true
	}

	// 新增其他依赖 Redis 的能力时在此追加，形如：
	//   if cfg.Cron.Enabled { return true }
	return false
}
