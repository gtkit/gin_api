// Package account 装配鉴权链路的账号、会员与角色校验源，独立于可删除的 auth 业务模块。
package account

import (
	"errors"

	"github.com/gtkit/gin-api/internal/repository/useraccount"
	"github.com/gtkit/gin-api/internal/repository/userrole"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"

	"gorm.io/gorm"
)

// Assembly 提供账号状态与具名授权需要的校验能力。
type Assembly struct {
	Users sharedauth.UserVerifier
	VIP   sharedauth.VIPVerifier
	Roles sharedauth.RoleVerifier
}

// New 只装配校验源，不查询数据库；Users 与 VIP 共享一个账号仓储。
func New(db *gorm.DB) (Assembly, error) {
	if db == nil {
		return Assembly{}, errors.New("account: database is required")
	}
	accounts := useraccount.New(db)
	return Assembly{Users: accounts, VIP: accounts, Roles: userrole.New(db)}, nil
}
