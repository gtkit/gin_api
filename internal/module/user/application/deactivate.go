package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/gtkit/gin-api/internal/models"
)

var (
	// ErrUserNotFound 表示用户不存在或已被软删除。
	ErrUserNotFound = errors.New("user not found")
	// ErrAlreadyDeactivated 表示用户已是注销态，幂等返回。
	ErrAlreadyDeactivated = errors.New("user already deactivated")
	// ErrDeactivateUnavailable 表示当前 Service 未注入事务能力，无法执行注销。
	// 仅在 NewWithDeps（仅 repo）构造的实例上出现；生产经 runtime 注入 TxManager。
	ErrDeactivateUnavailable = errors.New("deactivate: tx not configured")
)

// deactivatedNickname 注销后对客展示的占位昵称（业务文案，由 application 决定）。
const deactivatedNickname = "已注销用户"

// DeactivateInput 注销动作的全部上下文。
//
//   - UserID    必填，待注销用户。
//   - Reason    可选，用户填写的注销原因。
//   - Channel   必填，请求渠道（app/h5/admin 等），用于审计与分渠道统计。
//   - OperatorIP 来源 IP，由 handler 从请求里取。
type DeactivateInput struct {
	UserID     int64
	Reason     string
	Channel    string
	OperatorIP string
}

// Deactivate 执行用户注销（方案 A：脱敏改写 + 写注销记录 + 软删除）。
//
// 事务边界在此编排（经 TxManager.Do），事务内步骤：
//  1. LockByID 以 SELECT ... FOR UPDATE 锁定用户行，规避并发重复注销；
//  2. 检查未被注销（status != Deleted），否则幂等返回 ErrAlreadyDeactivated；
//  3. 写 user_deactivations 记录（保留 phone/email/nickname 原值，供审计/申诉）；
//  4. AnonymizeForDeactivation 脱敏改写 + 软删 users 行（脱敏后的 phone/昵称由本层决定）；
//  5. SoftDeleteProfile 软删 user_profiles 关联行。
//
// 任一步失败整事务回滚。SQL 细节下沉到 repository，本层只做业务编排。
func (s *Service) Deactivate(ctx context.Context, in DeactivateInput) error {
	if s.tx == nil || s.recorder == nil {
		return ErrDeactivateUnavailable
	}
	if in.UserID <= 0 {
		return ErrUserNotFound
	}

	return s.tx.Do(ctx, func(ctx context.Context) error {
		user, found, err := s.repo.LockByID(ctx, in.UserID)
		if err != nil {
			return err
		}
		if !found {
			return ErrUserNotFound
		}
		if user.Status == models.UserStatusDeleted {
			return ErrAlreadyDeactivated
		}

		record := &models.UserDeactivation{
			UserID:           user.ID,
			OriginalPhone:    user.Phone,
			OriginalEmail:    user.Email,
			OriginalNickname: user.Nickname,
			Reason:           in.Reason,
			Channel:          in.Channel,
			OperatorIP:       in.OperatorIP,
		}
		if err := s.recorder.Create(ctx, record); err != nil {
			return fmt.Errorf("create deactivation record: %w", err)
		}

		// 脱敏 phone 释放原手机号唯一索引并保留可追溯前缀（业务策略）。
		anonymizedPhone := fmt.Sprintf("DEL_%d_%d", user.ID, s.currentTime().Unix())
		if err := s.repo.AnonymizeForDeactivation(ctx, user.ID, anonymizedPhone, deactivatedNickname); err != nil {
			return err
		}
		if err := s.repo.SoftDeleteProfile(ctx, user.ID); err != nil {
			return err
		}

		s.auditf("user %d deactivated via channel=%s reason_len=%d", user.ID, in.Channel, len(in.Reason))
		return nil
	})
}
