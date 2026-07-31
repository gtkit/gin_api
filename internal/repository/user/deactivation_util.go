package user

import (
	"context"
	"errors"
	"fmt"

	"github.com/gtkit/gin-api/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LockByID 以 SELECT ... FOR UPDATE 锁定未软删的用户行，规避并发重复注销。
// found=false 表示用户不存在或已软删除（非错误），由上层决定语义。
func (r *Repository) LockByID(ctx context.Context, id int64) (*models.User, bool, error) {
	var user models.User
	err := r.mdbCtx(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND deleted_at IS NULL", id).
		First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("lock user %d: %w", id, err)
	}
	return &user, true, nil
}

// AnonymizeForDeactivation 执行注销脱敏改写并软删用户行（固定的注销机制）。
// 变量值由上层按业务策略生成：anonymizedPhone 释放原手机号唯一索引并保留可追溯前缀，
// anonymizedNickname 为对客展示文案。其余字段（email/avatar/password 清空、status、deleted_at）
// 是注销的固定语义。
func (r *Repository) AnonymizeForDeactivation(ctx context.Context, id int64, anonymizedPhone, anonymizedNickname string) error {
	updates := map[string]any{
		"phone":      anonymizedPhone,
		"email":      gorm.Expr("NULL"),
		"nickname":   anonymizedNickname,
		"avatar":     "",
		"password":   "",
		"status":     models.UserStatusDeleted,
		"deleted_at": gorm.Expr("NOW()"),
	}
	if err := r.mdbCtx(ctx).Model(&models.User{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return fmt.Errorf("anonymize user %d: %w", id, err)
	}
	return nil
}

// SoftDeleteProfile 软删用户的 user_profiles 关联行。
// 用 Where + Update 直接写 deleted_at；user_id 上有 uniqueIndex，最多影响一行，
// 用户从未登录过则 0 行，均属正常。
func (r *Repository) SoftDeleteProfile(ctx context.Context, userID int64) error {
	if err := r.mdbCtx(ctx).
		Model(&models.UserProfile{}).
		Where("user_id = ? AND deleted_at IS NULL", userID).
		Update("deleted_at", gorm.Expr("NOW()")).Error; err != nil {
		return fmt.Errorf("soft delete user_profile of %d: %w", userID, err)
	}
	return nil
}
