package bootstrap

import (
	"context"

	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
)

// vipAuthorizer 把 VIP 资格判定适配成 Authorizer。
//
// 它是**共享实现**：多个模块经 RouteSpec 引用同一个 ID，而不是各自复制一份。
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
