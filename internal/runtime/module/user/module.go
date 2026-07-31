package user

import (
	userapp "github.com/gtkit/gin-api/internal/module/user/application"
	"github.com/gtkit/gin-api/internal/repository/dbtx"
	userrepo "github.com/gtkit/gin-api/internal/repository/user"
	userdeactivation "github.com/gtkit/gin-api/internal/repository/user_deactivation"
	logger "github.com/gtkit/logger/v2"

	"gorm.io/gorm"
)

// NewService 在 runtime 层装配 user application service：
// db→repo（UserRepository + DeactivationRecorder）+ dbtx.Manager（TxManager），
// 使 application 不依赖 gorm/repository，注销事务以接口注入。
func NewService(db *gorm.DB) *userapp.Service {
	return userapp.NewServiceWithTx(
		userrepo.New(db),
		userdeactivation.New(db),
		dbtx.NewManager(db),
		userapp.WithAuditLogger(logAuditor{}),
	)
}

// logAuditor 把 application 的审计日志 port 适配到进程级 logger。
type logAuditor struct{}

func (logAuditor) Auditf(format string, args ...any) { logger.Infof(format, args...) }
