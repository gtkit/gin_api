package middleware

import (
	"context"
	"errors"
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/goerr"

	"github.com/gtkit/gin-api/internal/pkg/ginc"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
	"github.com/gtkit/gin-api/internal/shared/tenant"
)

// TenantResolver 从已认证主体解析出它所属的租户。
//
// 母版不内置实现：租户从什么派生是业务决定（令牌声明、用户归属表、组织同步结果
// 都可能），母版内置一种会被派生项目当成默认答案。
//
// 契约：
//   - 返回 error 表示**依赖故障**（如归属表查不到），映射 503——不得用它表达
//     "这个用户没有租户"，那会让一次基础设施故障看起来像业务拒绝；
//   - 返回空 ID 且无 error 视为契约违反，同样映射 503。真正"该用户不属于任何
//     租户"的场景应由业务决定：给一个专门的租户，或不把本中间件挂到那条路由上。
//
// 实现应自带缓存：本中间件按请求调用它，每请求查一次库会把一次登录的成本
// 加到每个接口上。
type TenantResolver func(ctx context.Context, p sharedauth.Principal) (tenant.ID, error)

var (
	errTenantResolverMissing = errors.New("tenant isolation requires a resolver")
	errTenantResolverEmpty   = errors.New("tenant resolver returned an empty tenant for an authenticated principal")
)

// TenantIsolation 把租户身份写入**请求上下文**，供数据访问层的隔离判定消费。
//
// 必须写进 c.Request.Context() 而不是 gin 的 Keys：handler 往下传的是
// c.Request.Context()，而 GORM 回调拿到的正是那条 context。写进 gin Keys 的东西
// 在数据访问层根本取不到（已认证主体就存在 Keys 里，因此它不能直接复用）。
//
// 装配顺序：必须排在认证中间件**之后**。租户只从已认证身份派生——这一条不是
// 洁癖，而是响应缓存的安全前提：响应缓存靠"请求是否携带身份头"决定能否缓存，
// 且缓存键里没有身份。若租户可以从 X-Tenant-ID 这类不算身份头的位置派生，
// 一个不带认证头的 GET 会被判定为可缓存，其响应随后被发给另一个租户的同一
// URL 请求——数据库侧隔离做得再对也没用，泄漏发生在缓存层。
//
// 未认证的请求不写入任何租户，也不中止：那条路由可能压根不碰受控表。
// 若它确实碰了，数据访问层会 fail-closed——安全由那一层兜底，不靠本中间件猜。
func TenantIsolation(resolve TenantResolver) gin.HandlerFunc {
	return func(c *gin.Context) {
		if resolve == nil {
			// 装配错误。不 panic 而是 503：一次装配失误不该让整个进程挂掉，
			// 但也绝不能静默放行——静默放行等于隔离没装而没人知道。
			abortWith(c, goerr.New(errTenantResolverMissing, goerr.StatusDependencyUnavailable()))
			return
		}

		principal, ok := ginc.PrincipalFromContext(c)
		if !ok {
			c.Next()
			return
		}

		id, err := resolve(c.Request.Context(), principal)
		if err != nil {
			abortWith(c, goerr.New(
				fmt.Errorf("tenant resolution unavailable: %w", err),
				goerr.StatusDependencyUnavailable()))
			return
		}
		if id == "" {
			abortWith(c, goerr.New(errTenantResolverEmpty, goerr.StatusDependencyUnavailable()))
			return
		}

		c.Request = c.Request.WithContext(tenant.With(c.Request.Context(), id))
		c.Next()
	}
}
