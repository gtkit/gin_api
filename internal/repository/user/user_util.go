package user

import (
	"context"
	"errors"
	"fmt"

	"github.com/gtkit/gin-api/internal/models"

	"gorm.io/gorm"
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
	if err := r.mdbCtx(ctx).Model(&models.User{}).Where("id = ?", userID).Updates(fields).Error; err != nil {
		// 带上操作与主键：裸 GORM 错误只说"Error 1062: Duplicate entry"，
		// 而一次请求里可能有多处写入，光看错误无从判断是哪一处。
		return fmt.Errorf("update user %d: %w", userID, err)
	}
	return nil
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
