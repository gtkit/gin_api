package bootstrap

import (
	"context"

	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
)

// vipAuthorizer 把 VIP 资格判定适配成 Authorizer。
//
// 它是**共享实现**：多个模块路由引用同一个 ID，而不是各自复制一份。
// 复制会让"会员判定"出现多份可能不一致的实现，且改动时极易漏掉其中之一。
type vipAuthorizer struct {
	verifier sharedauth.VIPVerifier
}

func newVIPAuthorizer(v sharedauth.VIPVerifier) sharedauth.Authorizer {
	return vipAuthorizer{verifier: v}
}

func (a vipAuthorizer) Authorize(
	ctx context.Context, principal sharedauth.Principal, _ sharedauth.AuthorizationRequest,
) (bool, error) {
	if a.verifier == nil {
		// 未注入依赖时拒绝而非放行：装配缺失绝不能表现为"允许"。
		return false, nil
	}
	// error 原样上抛：授权中间件会把它映射成 503 而不是 403——
	// 仓储故障不是"你没有权限"。
	return a.verifier.ValidateVIP(ctx, principal.UserID)
}

// adminRoleName 是 role.admin 判定在**权威角色源**里查找的角色标识。
//
// 它与授权注册表里的 ID（"role.admin"）不是同一个东西：前者是 user_roles 表里的值，
// 后者是路由声明引用的标识。两者碰巧相近，但将来可能不再对应——
// 例如把 role.admin 改为检查 superuser。定义成常量而不是散落字面量，
// 是为了让那种改动只有一个落点；散落时漏改的方向通常是"权限判定悄悄失效"。
const adminRoleName = "admin"

// roleAuthorizer 把角色判定适配成 Authorizer。
//
// **示例实现，可按需删除**（删除清单见 SCAFFOLD.md）。它替换了此前的恒拒绝占位，
// 给出接线示范：查权威角色源，而不是采信令牌里的 role claim。
//
// 它刻意不做三件事，需要时由使用者按自己的要求补：
//   - 不引入策略引擎：权威角色源的形态差异极大（角色表 / IAM / IdP claims / 多租户），
//     预置一种会让其余几种先删再重写；
//   - 不做缓存：失效策略取决于"刚撤销的权限多久生效"这个业务要求。
//     需要时在 RoleVerifier 的实现侧加，本 adapter 不用改；
//   - 不做角色层级与通配：只回答"这个用户有没有这个角色"。
type roleAuthorizer struct {
	verifier sharedauth.RoleVerifier
	role     string
}

func newRoleAuthorizer(v sharedauth.RoleVerifier, role string) sharedauth.Authorizer {
	return roleAuthorizer{verifier: v, role: role}
}

func (a roleAuthorizer) Authorize(
	ctx context.Context, principal sharedauth.Principal, _ sharedauth.AuthorizationRequest,
) (bool, error) {
	if a.verifier == nil {
		// 未注入依赖时拒绝而非放行：装配缺失绝不能表现为"允许"。
		return false, nil
	}
	// 刻意**不看** principal.Roles：那是令牌签发时刻的事实，而令牌寿命可达数十天。
	// 权威源里没有记录就是没有权限——这也保证了替换恒拒绝占位后，
	// 未接入角色数据的部署行为不变。
	//
	// error 原样上抛：角色源故障映射 503 而不是 403。
	return a.verifier.HasRole(ctx, principal.UserID, a.role)
}
