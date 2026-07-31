package user

import (
	"context"
	"errors"
	"fmt"

	"github.com/gtkit/gin-api/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Get 按主键查询用户。
// found=false 表示用户不存在（非错误，由调用方决定缺省策略）；
// err 仅在数据库访问真正失败时返回，调用方据此区分「没找到」与「库故障」。
func (r *Repository) Get(ctx context.Context, idstr string) (user models.User, found bool, err error) {
	err = r.mdbCtx(ctx).Where("id", idstr).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.User{}, false, nil
	}
	if err != nil {
		return models.User{}, false, fmt.Errorf("get user %s: %w", idstr, err)
	}
	return user, true, nil
}

// Update 按字段更新用户资料（partial update）。
// 只更新 fields 中显式给出的列：调用方（application）据请求指针是否为 nil 决定纳入哪些字段。
// 用 map 而非 struct，既能精确限定更新范围，又能写入零值（如 gender=0）——
// struct + Updates 会忽略零值字段，且 Select 必须在 Updates(finisher) 之前才生效。
func (r *Repository) Update(ctx context.Context, userID int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.mdbCtx(ctx).Model(&models.User{}).Where("id = ?", userID).Updates(fields).Error
}

// ExistsByID 判断指定主键的可用（status=1）用户是否存在。
// 用强类型主键代替过去的动态字段名 IsExist(field, value)，无需字段白名单即天然免于 SQL 注入。
func (r *Repository) ExistsByID(ctx context.Context, id int64) (bool, error) {
	var count int64
	if err := r.mdbCtx(ctx).Model(models.User{}).
		Where("id = ?", id).Where("status = 1").
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("check user exists by id %d: %w", id, err)
	}
	return count > 0, nil
}

// CreateOrUpdate 创建或更新.
func (r *Repository) CreateOrUpdate(ctx context.Context, user *models.User, primarykey string, updatekey []string) error {
	return r.mdbCtx(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: primarykey}},
		DoUpdates: clause.AssignmentColumns(updatekey),
	}).Create(&user).Error
}

// UpdateVipStatus 更新 用户会员状态。
func (r *Repository) UpdateVipStatus(ctx context.Context, userId int64, status models.VipStatus) error {
	return r.mdbCtx(ctx).Model(models.User{}).Where("id = ?", userId).Update("vip_status", status).Error
}

// GetVIPInfo 查询用户会员信息.
func (r *Repository) GetVIPInfo(ctx context.Context, userID int64) (*models.User, error) {
	var user models.User
	err := r.mdbCtx(ctx).
		Select("id", "vip_status", "vip_level", "vip_expire_at").
		Where("id = ? AND deleted_at IS NULL", userID).
		First(&user).Error
	if err != nil {
		return nil, fmt.Errorf("get vip info: %w", err)
	}
	return &user, nil
}
