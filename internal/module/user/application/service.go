package application

import (
	"context"
	"strconv"
	"time"

	"github.com/gtkit/gin-api/internal/models"
)

// Service 提供 v2 用户模块的应用层能力。
type Service struct {
	repo     UserRepository
	recorder DeactivationRecorder
	tx       TxManager
	audit    AuditLogger
	// now 是可注入的时钟，默认 time.Now；测试注入固定时刻以确定性覆盖过期边界。
	now func() time.Time
}

// ServiceOption 配置 Service 的可选依赖。
type ServiceOption func(*Service)

// WithAuditLogger 注入审计日志实现（runtime 装配用）。
func WithAuditLogger(audit AuditLogger) ServiceOption {
	return func(s *Service) { s.audit = audit }
}

// NewWithDeps 仅注入 UserRepository，用于不涉及注销的单元测试；
// 此构造下 recorder/tx 为 nil，调用 Deactivate 返回 ErrDeactivateUnavailable。
func NewWithDeps(repo UserRepository) *Service {
	return &Service{repo: repo, now: time.Now}
}

// NewServiceWithTx 注入完整依赖（生产装配与注销单测用）。
func NewServiceWithTx(repo UserRepository, recorder DeactivationRecorder, tx TxManager, opts ...ServiceOption) *Service {
	s := &Service{repo: repo, recorder: recorder, tx: tx, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// auditf 写审计日志；未注入 AuditLogger 时静默跳过（审计缺失不影响业务事务）。
func (s *Service) auditf(format string, args ...any) {
	if s.audit != nil {
		s.audit.Auditf(format, args...)
	}
}

// currentTime 返回当前时刻，经可注入时钟取值。
// 零值 Service（未经构造函数）也安全：now 为 nil 时回退到 time.Now。
func (s *Service) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// ValidateUser 校验用户是否存在且可用。
// 返回 (false, err) 表示库访问失败（基础设施故障）——调用方据此返回 503，
// 不能像旧实现那样把 DB 故障静默降级成"用户不可用"(401)，否则库抖动会被误判为鉴权失败。
// (false, nil) 才表示用户确实不存在/不可用。
func (s *Service) ValidateUser(ctx context.Context, userID int64) (bool, error) {
	exists, err := s.repo.ExistsByID(ctx, userID)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// ValidateVIP 校验用户是否具备有效会员资格。
// 判定规则与当前业务口径保持一致：
// 1. 用户必须存在；
// 2. vip_status 必须为 active；
// 3. 若存在 vip_expire_at，则过期时间必须晚于当前时刻。
func (s *Service) ValidateVIP(ctx context.Context, userID int64) bool {
	// 查询失败或用户不存在都视为不具备有效会员资格：ValidateVIP 契约为 bool，
	// 无法上抛 error，但仓储层已不再吞错，这里是有意为之的降级判定。
	user, found, err := s.repo.Get(ctx, strconv.FormatInt(userID, 10))
	if err != nil || !found {
		return false
	}
	if user.VipStatus != models.VipStatusActive {
		return false
	}
	if user.VipExpireAt != nil && !user.VipExpireAt.After(s.currentTime()) {
		return false
	}

	return true
}
