// Package userrole 是角色关联表的数据访问。
//
// **示例实现，可按需删除**（删除清单见 SCAFFOLD.md）。
package userrole

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/repository/dbtx"
)

// Repository 是 user_roles 表的数据访问。
type Repository struct {
	db *gorm.DB
}

// New 构造仓储。
func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// mdbCtx 是本仓储所有数据访问的唯一收口：经 dbtx.DB 在事务内自动用事务句柄，否则用底座 db。
func (r *Repository) mdbCtx(ctx context.Context) *gorm.DB {
	return dbtx.DB(ctx, r.db)
}

// HasRole 报告用户是否具备指定角色。
//
// 返回 (bool, error) 而不是裸 bool：查询失败与"确实没有这个角色"是两件不同的事。
// 裸 bool 会把"数据库挂了"折叠成"没有权限"，于是一次基础设施故障表现为
// 全体管理员突然失去权限，而客户端收到的是一个正常的业务拒绝——
// 既不会重试，也不会触发任何告警。
//
// 查询只取 1 行：判定只需要"存不存在"，取全部记录会让一个角色很多的账号
// 白白搬运整个集合。
func (r *Repository) HasRole(ctx context.Context, userID int64, role string) (bool, error) {
	if userID <= 0 || role == "" {
		// 非法入参按"没有角色"处理而不是报错：调用方传空角色名说明它自己有 bug，
		// 但那不是数据源故障——报 error 会让它被映射成 503，掩盖真正的编码问题。
		return false, nil
	}

	var exists int64
	err := r.mdbCtx(ctx).
		Model(&models.UserRole{}).
		Where("user_id = ? AND role = ?", userID, role).
		Limit(1).
		Count(&exists).Error
	if err != nil {
		return false, fmt.Errorf("query user role: %w", err)
	}
	return exists > 0, nil
}
