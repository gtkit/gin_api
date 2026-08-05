package user

import (
	"fmt"
	"time"

	userworker "github.com/gtkit/gin-api/internal/module/user/worker"
	"github.com/gtkit/gin-api/internal/platform/config"
	userdeactivation "github.com/gtkit/gin-api/internal/repository/user_deactivation"

	"gorm.io/gorm"

	logger "github.com/gtkit/logger/v2"
)

// NewDeactivationCleanup 在 runtime 层装配注销档案清理任务。
//
// **模块内部装配归这里**：把 db + config 翻译成 repository 与业务对象，
// 是模块装配层的职责；编排层不该知道 user 模块用了哪个仓储。
//
// 返回的是**纯业务对象**而不是编排契约的实现：那个契约的类型属于 bootstrap，
// 在这里实现它会形成 bootstrap ↔ runtime 的循环依赖。编排语义（Kind、
// Lifecycle、Required）由 bootstrap 侧的薄适配器补齐。
//
// **未配置保留期时返回 (nil, nil)**：禁用应当表现为根本不注册这个任务，
// 而不是注册一个什么都不做的——后者会在关停顺序与就绪信息里留下一个名字，
// 让人以为清理正在进行。
func NewDeactivationCleanup(
	db *gorm.DB, cfg config.UserModuleConfig,
) (*userworker.DeactivationCleanup, error) {
	if !cfg.RetentionEnabled() {
		return nil, nil
	}

	retention := time.Duration(cfg.DeactivationRetentionDays) * 24 * time.Hour
	cleanup, err := userworker.NewDeactivationCleanup(
		userdeactivation.New(db), cleanupAuditor{},
		retention, cfg.EffectiveCleanupInterval(), cfg.EffectiveCleanupBatch(),
	)
	if err != nil {
		return nil, fmt.Errorf("assemble deactivation cleanup: %w", err)
	}
	return cleanup, nil
}

// cleanupAuditor 把清理结果接到进程日志栈。
type cleanupAuditor struct{}

func (cleanupAuditor) Auditf(format string, args ...any) { logger.Infof(format, args...) }

// Failf 走 Warn：清理失败意味着该被清除的个人信息在超期留存，
// 日志告警规则按级别匹配，不该被迫去解析 Info 消息的文本。
func (cleanupAuditor) Failf(format string, args ...any) { logger.Warnf(format, args...) }
