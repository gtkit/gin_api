package router

import (
	"errors"
	"fmt"

	"github.com/gtkit/gin-api/internal/router/routespec"
	"github.com/gtkit/gin-api/pkg/ratelimit"
)

// validateRateLimitReferences 校验路由引用的策略 ID 都已登记，且 scope 与访问策略相容。
//
// 留在路由层而不是限流包里：这条规则的判据是**访问策略**——它属于路由契约，
// 而限流包不认识 HTTP，也不该认识"什么叫已认证"。
//
// 在 compile 之后、接触生产 engine 之前执行。引用一个不存在的策略 ID
// 若不拦住，那条路由就会**完全没有限流**——而配置里明明写了一个 ID，
// 看起来一切正常。
func validateRateLimitReferences(registry *ratelimit.Registry, routes []routespec.CompiledRoute) error {
	var errs []error

	for _, route := range routes {
		for _, id := range route.Spec.RateLimitPolicyIDs {
			limiter, ok := registry.Lookup(id)
			if !ok {
				errs = append(errs, fmt.Errorf(
					"route %s %s references rate limit policy %q, which is not registered: "+
						"that policy would silently not apply to the route",
					route.Spec.Method, route.FullPath, id))
				continue
			}

			// ScopeUser 需要已认证身份。挂在公开路由上时拿不到 userID，
			// 该策略会退化成一个所有匿名请求共享的桶——一个用户就能打满它。
			if limiter.Policy().Scope == ratelimit.ScopeUser &&
				!requiresAuthentication(route.Spec.AccessPolicy) {
				errs = append(errs, fmt.Errorf(
					"route %s %s uses user-scoped rate limit policy %q but does not require authentication: "+
						"without an identity every anonymous request would share one bucket",
					route.Spec.Method, route.FullPath, id))
			}
		}
	}

	return errors.Join(errs...)
}

// requiresAuthentication 报告访问策略是否保证 handler 能拿到已认证身份。
//
// OptionalAuthenticated 不算：它允许匿名请求通过，因此不保证有身份。
func requiresAuthentication(p routespec.AccessPolicy) bool {
	return p.Mode == routespec.AccessAuthenticated || p.Mode == routespec.AccessAuthorized
}
