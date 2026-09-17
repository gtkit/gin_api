// Package useraccount 是账号状态的数据访问，服务于**鉴权链路**。
//
// 它与 repository/authcredential 分开：那个包只碰凭据列（登录时读密码哈希），
// 本包回答的是"这个已经拿到令牌的用户现在还能不能用"。两件事的调用时机、
// 频次与故障影响面都不同——凭据只在登录时读一次，账号状态则是**每个带令牌的
// 请求**都要读，把它们混在一个仓储里，将来给其中一条加缓存会连带影响另一条。
//
// 为什么直接实现 shared/auth 的 verifier 接口而不经 application service：
// 这两项判定没有任何业务规则可言（一次带条件的存在性查询），中间加一层
// service 只会多一个纯转发。与 repository/userrole 的接线方式一致。
package useraccount

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/repository/dbtx"
)

// Repository 是 users 表中账号状态列的数据访问。
type Repository struct {
	db *gorm.DB

	// now 供测试注入固定时钟；为 nil 时取 time.Now。
	//
	// 会员到期判定与当前时刻有关，而"到期边界"正是最该被测到的一条分支——
	// 没有注入点的话，那条分支只能靠 sleep 或改系统时间去碰。
	now func() time.Time
}

// New 构造仓储。
func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// NewWithClock 构造带固定时钟的仓储，供测试使用。
func NewWithClock(db *gorm.DB, now func() time.Time) *Repository {
	return &Repository{db: db, now: now}
}

// mdbCtx 是本仓储所有数据访问的唯一收口：经 dbtx.DB 在事务内自动用事务句柄，否则用底座 db。
func (r *Repository) mdbCtx(ctx context.Context) *gorm.DB {
	return dbtx.DB(ctx, r.db)
}

func (r *Repository) currentTime() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// ValidateUser 报告用户存在且处于可用状态（status = 正常）。
//
// 返回 (false, err) 表示库访问失败（基础设施故障），调用方据此返回 503；
// (false, nil) 才表示用户确实不存在或已被禁用。两者绝不能折叠成一个 false——
// 那样一次数据库抖动会表现为全体用户令牌失效（401），而客户端对 401 的
// 标准反应是清掉登录态重新登录，等于把一次可恢复的故障变成一次全站登出。
//
// 只 Count 不取行：判定只需要"存不存在"，把整行搬进内存没有用处，
// 而这是每个带令牌的请求都会走的路径。
func (r *Repository) ValidateUser(ctx context.Context, userID int64) (bool, error) {
	if userID <= 0 {
		// 非法入参按"用户不可用"处理而不是报错：令牌里带着一个非法 ID 说明
		// 签发侧有问题，但那不是数据源故障——报 error 会被映射成 503，
		// 掩盖真正的原因。
		return false, nil
	}

	var exists int64
	err := r.mdbCtx(ctx).
		Model(&models.User{}).
		Where("id = ?", userID).
		Where("status = ?", models.UserStatusNormal).
		Limit(1).
		Count(&exists).Error
	if err != nil {
		return false, fmt.Errorf("validate user %d: %w", userID, err)
	}
	return exists > 0, nil
}

// ValidateVIP 报告用户具备有效会员资格：账号可用、vip_status 为 active，
// 且 vip_expire_at 为空或**晚于**当前时刻。
//
// **示例实现，可按需删除**（会员是特定业务领域的概念，删除清单见 SCAFFOLD.md）。
//
// 契约与 ValidateUser 同款：仓储故障是 error 而不是 false。裸 bool 会把
// "数据库挂了"折叠成"不是会员"，于是一次基础设施故障表现为全体会员突然
// 失去权益，而客户端看到的是一个正常的业务拒绝，既不会重试也不会触发告警。
//
// 到期判定用 !After 而不是 Before：到期时刻本身算已过期，用 Before 会让
// 恰好等于到期时间的那一瞬间仍然放行。
func (r *Repository) ValidateVIP(ctx context.Context, userID int64) (bool, error) {
	if userID <= 0 {
		return false, nil
	}

	var user models.User
	err := r.mdbCtx(ctx).
		Select("id", "status", "vip_status", "vip_expire_at").
		Where("id = ?", userID).
		Where("status = ?", models.UserStatusNormal).
		Limit(1).
		Find(&user).Error
	if err != nil {
		return false, fmt.Errorf("validate vip %d: %w", userID, err)
	}
	// Find 在无匹配行时不报 ErrRecordNotFound，而是留下零值——用主键判空。
	if user.ID == 0 {
		return false, nil
	}
	if user.VipStatus != models.VipStatusActive {
		return false, nil
	}
	if user.VipExpireAt != nil && !user.VipExpireAt.After(r.currentTime()) {
		return false, nil
	}
	return true, nil
}
