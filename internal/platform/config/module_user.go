package config

import (
	"errors"
	"fmt"
	"time"
)

// UserModuleConfig 是 user 模块的配置子树。
type UserModuleConfig struct {
	// DeactivationRetentionDays 是注销档案的保留天数。
	//
	// **没有默认值，必须显式配置**。这不是疏忽——各行业与各司法辖区对个人信息
	// 的最长保留期、删除权响应时限、以及"注销后仍需留存多久"的要求并不一致，
	// 脚手架替使用者选一个数字只会让它在某些辖区是错的，而且是**静默**错的。
	//
	// 0 表示不自动清理（由外部流程负责），此时启动期会提示一次——
	// 一份无人清理、越积越多的个人信息档案，出问题时才被发现的代价最高。
	DeactivationRetentionDays int `mapstructure:"deactivation_retention_days"`
	// DeactivationCleanupInterval 是清理任务的执行间隔；留空按一天。
	DeactivationCleanupInterval time.Duration `mapstructure:"deactivation_cleanup_interval"`
	// DeactivationCleanupBatch 是单次清理的最大行数；<=0 按 500。
	//
	// 分批而不是一次删完：注销档案在长期运行的系统里可能积累到百万级，
	// 一条 DELETE 会长时间持有锁并撑大 binlog，而这个任务本身并不紧急。
	DeactivationCleanupBatch int `mapstructure:"deactivation_cleanup_batch"`
}

// RetentionEnabled 报告是否启用自动清理。
func (c UserModuleConfig) RetentionEnabled() bool {
	return c.DeactivationRetentionDays > 0
}

// EffectiveCleanupInterval 返回生效的执行间隔。
func (c UserModuleConfig) EffectiveCleanupInterval() time.Duration {
	if c.DeactivationCleanupInterval > 0 {
		return c.DeactivationCleanupInterval
	}
	return 24 * time.Hour
}

// EffectiveCleanupBatch 返回生效的单批行数。
func (c UserModuleConfig) EffectiveCleanupBatch() int {
	if c.DeactivationCleanupBatch > 0 {
		return c.DeactivationCleanupBatch
	}
	return 500
}

// Validate 校验 user 模块配置自洽。
func (c UserModuleConfig) Validate() []error {
	var errs []error

	if c.DeactivationRetentionDays < 0 {
		errs = append(errs, errors.New(
			"module.user.deactivation_retention_days must be >= 0 (0 disables automatic cleanup)"))
	}
	// 上界防手误：写成 36500 天（100 年）等于永久保留，而那多半不是本意。
	const maxRetentionDays = 3650
	if c.DeactivationRetentionDays > maxRetentionDays {
		errs = append(errs, fmt.Errorf(
			"module.user.deactivation_retention_days %d exceeds %d days (10 years): "+
				"such a value is effectively permanent retention, which is rarely intended",
			c.DeactivationRetentionDays, maxRetentionDays))
	}
	// 下界：毫秒级间隔会让清理任务持续扫库，而它本身并不紧急。
	const minCleanupInterval = time.Minute
	if c.DeactivationCleanupInterval < 0 {
		errs = append(errs, errors.New("module.user.deactivation_cleanup_interval must be >= 0"))
	} else if c.DeactivationCleanupInterval > 0 && c.DeactivationCleanupInterval < minCleanupInterval {
		errs = append(errs, fmt.Errorf(
			"module.user.deactivation_cleanup_interval %s is below the %s minimum: "+
				"a tighter loop keeps scanning the table without making the task any more timely",
			c.DeactivationCleanupInterval, minCleanupInterval))
	}

	// 上界：单批过大会形成长事务并撑大 binlog，而分批的意义就是避免它。
	const maxCleanupBatch = 10000
	if c.DeactivationCleanupBatch < 0 {
		errs = append(errs, errors.New("module.user.deactivation_cleanup_batch must be >= 0"))
	} else if c.DeactivationCleanupBatch > maxCleanupBatch {
		errs = append(errs, fmt.Errorf(
			"module.user.deactivation_cleanup_batch %d exceeds %d: a batch that large holds locks "+
				"long enough to defeat the point of batching",
			c.DeactivationCleanupBatch, maxCleanupBatch))
	}

	return errs
}

// RetentionWarnings 返回未设置保留期的个人信息表清单。
//
// 只提示不阻断：保留期是使用方按司法辖区决定的事实，母版无从判断，
// 也不该因为它没决定而拒绝启动。一份无人清理、越积越多的个人信息档案，
// 出问题时才被发现的代价最高，而它恰恰是最容易被忽略的。
func (c *Config) RetentionWarnings() []string {
	if c == nil {
		return nil
	}

	var warnings []string
	if !c.Modules.User.RetentionEnabled() {
		warnings = append(warnings,
			"modules.user.deactivation_retention_days is 0: deactivation records keep the "+
				"user's phone, email, nickname and source IP indefinitely, and no cleanup task "+
				"is registered. Set a retention window that matches your jurisdiction, or make "+
				"sure an external process purges the table.")
	}
	return warnings
}
