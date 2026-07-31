package application

import (
	"context"

	"github.com/gtkit/gin-api/internal/models"
)

// UserRepository 定义 user 模块依赖的仓储能力。
type UserRepository interface {
	// ExistsByID 判断指定主键的可用（status=1）用户是否存在；err 仅在库访问失败时返回。
	ExistsByID(ctx context.Context, id int64) (bool, error)
	// Get 按主键查询用户；found=false 表示不存在（非错误），err 仅在库访问失败时返回。
	Get(ctx context.Context, idstr string) (user models.User, found bool, err error)
	Update(ctx context.Context, userID int64, fields map[string]any) error

	// 注销流程所需（在事务内执行，由 TxManager 经 ctx 传递事务）。
	LockByID(ctx context.Context, id int64) (*models.User, bool, error)
	AnonymizeForDeactivation(ctx context.Context, id int64, anonymizedPhone, anonymizedNickname string) error
	SoftDeleteProfile(ctx context.Context, userID int64) error
}

// DeactivationRecorder 写入用户注销审计记录。
type DeactivationRecorder interface {
	Create(ctx context.Context, record *models.UserDeactivation) error
}

// TxManager 把一组 repo 调用包进同一数据库事务；fn 内拿到的 ctx 已携带事务句柄，
// repo 经各自 mdbCtx 自动参与。事务边界只在 application 编排，*gorm.DB 不进 application。
type TxManager interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// AuditLogger 记录不可逆业务操作（如注销）的审计日志。
// application 不依赖具体日志库，由 runtime 注入实现；未注入时审计日志静默跳过。
type AuditLogger interface {
	Auditf(format string, args ...any)
}
