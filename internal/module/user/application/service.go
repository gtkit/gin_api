package application

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
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

// Deps 是 Service 的**必需**依赖。
//
// 用一个结构体承载而不是多个构造函数：此前 NewWithDeps 只注入 repo，
// recorder/tx 留空，于是"依赖是否齐全"变成运行期才知道的事——
// 调用 Deactivate 会返回一个"未配置事务能力"的错误。
//
// 那个错误码是**装配缺陷的运行期表现**，不是业务语义。调用方拿到它无从处理：
// 既不能重试，也不能提示用户，只能说明部署装错了。现在缺失在装配期即失败，
// 问题从"上线后某个接口返回怪错误"前移到"进程起不来"。
type Deps struct {
	Repo     UserRepository
	Recorder DeactivationRecorder
	Tx       TxManager
}

// ErrIncompleteDeps 表示装配缺少必需依赖。
//
// 它只可能在进程启动的装配路径上出现，永远不会到达请求处理链——
// 因此不需要（也不应该）有对应的对客错误码。
var ErrIncompleteDeps = errors.New("user application: incomplete dependencies")

// NewService 注入必需依赖并应用可选策略。
//
// 返回 error 而不是 panic：装配失败要沿 bootstrap 的错误链上抛，
// 由启动流程统一报告并退出，与本仓库其它装配点（路由注册、授权注册表）一致。
// panic 会绕过既有的关停编排，把一个可诊断的配置问题变成一段栈回溯。
//
// 也不接受"缺了就降级为 nil 依赖"：那正是本次要消灭的形态——
// 装配缺陷推迟到第一个真实请求才暴露。
//
// opts 只用于**真正可选**的策略（如审计日志实现）。必需依赖走 option
// 就等于把"是否齐全"重新变成运行期才知道的事。
func NewService(deps Deps, opts ...ServiceOption) (*Service, error) {
	var missing []string
	if deps.Repo == nil {
		missing = append(missing, "Repo")
	}
	if deps.Recorder == nil {
		missing = append(missing, "Recorder")
	}
	if deps.Tx == nil {
		missing = append(missing, "Tx")
	}
	if len(missing) > 0 {
		// 一次报全部缺失字段，而不是遇到第一个就返回：装配问题通常成串出现，
		// 逐个暴露意味着修一次重启一次。
		return nil, fmt.Errorf("%w: missing %s", ErrIncompleteDeps, strings.Join(missing, ", "))
	}

	s := &Service{repo: deps.Repo, recorder: deps.Recorder, tx: deps.Tx, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
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

// ValidateVIP 报告用户是否具备有效会员资格：用户存在、vip_status 为 active、
// 且 vip_expire_at 为空或晚于当前时刻。
//
// 返回 (bool, error) 而不是裸 bool：仓储故障与"确实不是会员"是两件不同的事。
// 裸 bool 契约会把"数据库挂了"折叠成"不是会员"，于是一次基础设施故障
// 表现为全体会员突然失去权益，而客户端看到的是一个正常的业务拒绝，不会重试也不会告警。
//
// 调用方应把 error 映射为 503，与 ValidateUser 的契约一致。
func (s *Service) ValidateVIP(ctx context.Context, userID int64) (bool, error) {
	user, found, err := s.repo.Get(ctx, strconv.FormatInt(userID, 10))
	if err != nil {
		return false, fmt.Errorf("validate vip: %w", err)
	}
	if !found {
		return false, nil
	}
	if user.VipStatus != models.VipStatusActive {
		return false, nil
	}
	if user.VipExpireAt != nil && !user.VipExpireAt.After(s.currentTime()) {
		return false, nil
	}

	return true, nil
}
