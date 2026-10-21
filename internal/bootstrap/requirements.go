package bootstrap

import (
	"github.com/gtkit/gin-api/internal/platform/config"
)

// RedisRequired 报告当前配置下是否存在必须依赖 Redis 的已启用能力。
//
// **导出是为了让诊断层复用同一份判据**：doctor 需要知道 Redis 是否必需才能给出
// 正确的退出码档位，而它自己再写一份必然与这张表分叉——分叉的两份里诊断那份
// 永远不会跟上。判据由命令层注入（它同时看得到装配层与诊断层），因此诊断层
// 不必依赖本包。
//
// 判据只能来自"谁真的要用它"：下面的能力表每行一个依赖 Redis 的已启用能力，
// 派生项目引入新依赖时在表里加一行，它就自动成为 required，
// 无需改动 Resources 或启动流程。
func RedisRequired(cfg *config.Config) bool {
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

	// 异步任务队列：入队端与调度器都以 Redis 为载体，两者在拿不到客户端时
	// **无法装配**（tasks.NewProducer 与 scheduler 都对 nil client 返回错误）。
	// 一行覆盖两者：调度器由 tasks.scheduler.enabled 门控，而那以 tasks.enabled 为前提。
	//
	// 漏掉这一行的后果不是"Redis 挂了任务不跑"，而是**进程起不来且错误指错方向**：
	// 判为 optional 之后，Redis 已配置但启动期不可达时资源层保留 nil 客户端，
	// 装配走到 tasks.NewProducer 才失败，报出的是"producer requires a redis client"——
	// 那读起来像配置缺失，而真实原因（Redis 不可达）不在错误里任何位置。
	//
	// 也就是说：这不是"要不要让它成为必需"的权衡，进程两种情况下都起不来；
	// 加进表里只是让失败发生在正确的位置、带上正确的原因。
	if cfg.Tasks.Enabled {
		return true
	}

	// 新增其他依赖 Redis 的能力时在此追加，形如：
	//   if cfg.Cron.Enabled { return true }
	//
	// 判据（人工审查，非自动门禁）：被推导为 optional 的资源，其消费方都必须
	// 能在空句柄下完成装配。任何消费方在空句柄下返回错误，就说明这张表少了一行。
	return false
}
