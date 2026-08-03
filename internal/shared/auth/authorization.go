package auth

import "context"

// Principal 是已完成**认证**的请求主体。
//
// 它只承载认证阶段确定的事实（我是谁），不承载授权判定（我能做什么）。
// 两者必须分开：把角色判定混进认证，会让"token 合法但角色不符"表现为 401，
// 而它实际是 403——客户端据此重新登录也没有用。
type Principal struct {
	// UserID 是已认证用户的主键。
	UserID int64
	// Roles 是 token 携带的角色声明。
	//
	// **它不是权威权限事实**：token 默认 TTL 30 天，其中的角色可能早已过期。
	// 授权实现必须查询权威角色源，只有在明确接受"最长 30 天延迟"时才可直接采信。
	Roles []string
}

// AuthorizationRequest 描述一次授权判定的输入。
//
// 刻意只含稳定的路由维度，**不含 HTTP 上下文对象、请求体或响应写入器**：
// 授权判定是纯策略函数，拿到 *gin.Context 就迟早会有人在里面读 body、写响应，
// 或者做需要数据库的资源所有权判断——那属于 application service，不属于 HTTP 授权层。
type AuthorizationRequest struct {
	// Method 是大写 HTTP 方法。
	Method string
	// RoutePattern 是注册时的路由模式（含 :param 占位），不是实际请求路径。
	// 用模式而非实际路径，才能作为低基数的 metrics label。
	RoutePattern string
	// PathParams 是路径参数的副本。
	PathParams map[string]string
}

// Authorizer 判定已认证主体是否被允许执行某次请求。
//
// 契约：
//   - (true, nil)  允许；
//   - (false, nil) 明确拒绝 → 映射 403；
//   - (_, non-nil) 依赖故障 → 映射 503，**不得**当作拒绝。
//
// 最后一条是关键：把"角色数据源挂了"映射成 403，会让一次基础设施故障
// 表现为全体用户突然失去权限，而客户端看到 403 只会以为自己真的没权限。
type Authorizer interface {
	Authorize(ctx context.Context, principal Principal, request AuthorizationRequest) (bool, error)
}

// AuthorizerFunc 让普通函数满足 Authorizer。
type AuthorizerFunc func(ctx context.Context, principal Principal, request AuthorizationRequest) (bool, error)

func (f AuthorizerFunc) Authorize(
	ctx context.Context, principal Principal, request AuthorizationRequest,
) (bool, error) {
	return f(ctx, principal, request)
}

// DenyAll 是一个恒拒绝的 authorizer。
//
// 用作尚未接入权威数据源的占位实现：生成的模块因此可编译、可启动，
// 但所有请求都被拒绝——不留 TODO、不默许放行。
// "先放行、以后再补" 的占位会在补上之前一直是一个敞开的授权缺口。
func DenyAll(reason string) Authorizer {
	return AuthorizerFunc(func(context.Context, Principal, AuthorizationRequest) (bool, error) {
		return false, nil
	})
}
