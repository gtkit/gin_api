//go:build scaffold

package scaffold

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// accessChoice 是生成模块时必须做出的访问策略选择。
//
// 它同时承载该策略**所需的接线信息**：需要哪些中间件依赖、适用哪些限流作用域、
// 是否要生成模块专属的授权实现。此前只有 Expr，于是"表达式用了 deps.Authorize"
// 与"装配层要不要注入 Authorize"是两处独立信息，接线漏了也没有任何信号——
// 生成的模块能编译、启动即空指针 panic。
type accessChoice struct {
	// Expr 是写进生成代码的访问策略语句。
	Expr string
	// Note 出现在生成结果的提示里，说明该选择的后果与后续动作。
	Note string
	// AuthorizerID 非空时表示该策略用到 deps.Authorize(AuthorizerID)。
	AuthorizerID string
	// OwnsAuthorizer 为真时，该 AuthorizerID 是模块专属的，需要在 runtime 层
	// 生成恒拒绝实现并注册进注册表；为假表示复用母版已注册的实现。
	OwnsAuthorizer bool
	// RateLimitPolicies 是写进生成代码的限流策略标识列表（已带引号）。
	//
	// 随访问策略变化：没有身份的路由不能引用身份作用域的策略——那类策略取不到
	// 主体，中间件把它判为装配错误并拒绝，于是该路由的每个请求都是 429。
	RateLimitPolicies string
}

// 限流策略集合按"有无身份"分两档。
const (
	rateLimitPoliciesSourceOnly = `"default.ip"`
	rateLimitPoliciesWithUser   = `"default.ip", "default.user"`
)

// 可选的 --access 取值。
const (
	AccessOptionAuthenticated = "authenticated"
	AccessOptionOptional      = "optional"
	AccessOptionAdmin         = "admin"
	AccessOptionCustom        = "custom"
)

var accessOptions = []string{
	AccessOptionAuthenticated, AccessOptionOptional, AccessOptionAdmin, AccessOptionCustom,
}

// ResolveAccessChoice 把 --access / --public 翻译成生成用的访问策略。
//
// 二者**严格二选一**：两个都给报冲突，两个都不给也报错。刻意不设默认值——
// 访问策略是安全决策，让它有默认值就等于允许"没想过"这个状态存在，
// 而最可能的默认（公开）恰好是最危险的那个。
//
// `--public` 不是"未指定"，它是**显式的公开声明**，与其它模式同级。
func ResolveAccessChoice(access string, public bool, moduleName string) (accessChoice, error) {
	access = strings.TrimSpace(access)

	switch {
	case access != "" && public:
		return accessChoice{}, fmt.Errorf(
			"--access=%s conflicts with --public: choose exactly one", access)
	case access == "" && !public:
		return accessChoice{}, fmt.Errorf(
			"an access policy is required: pass --public for a deliberately public module, "+
				"or --access=%s", strings.Join(accessOptions, "|"))
	case public:
		return accessChoice{
			Expr: "",
			Note: "模块声明为公开（--public）：所有生成的路由无需身份即可访问。" +
				"限流只按来源：公开路由没有身份维度。",
			RateLimitPolicies: rateLimitPoliciesSourceOnly,
		}, nil
	}

	if !slices.Contains(accessOptions, access) {
		return accessChoice{}, fmt.Errorf(
			"unknown --access=%q; valid values are %s (or use --public)",
			access, strings.Join(accessOptions, "|"))
	}

	switch access {
	case AccessOptionAuthenticated:
		return accessChoice{
			Expr:              "g.Use(deps.Auth)",
			Note:              "模块要求已认证身份：生成的 Register 里已挂 deps.Auth。",
			RateLimitPolicies: rateLimitPoliciesWithUser,
		}, nil
	case AccessOptionOptional:
		return accessChoice{
			Expr: "g.Use(deps.OptionalAuth)",
			Note: "模块允许匿名访问；一旦提供 token 则必须合法。" +
				"限流只按来源：匿名请求没有 user 维度，身份作用域的策略在这里取不到主体。",
			RateLimitPolicies: rateLimitPoliciesSourceOnly,
		}, nil
	case AccessOptionAdmin:
		return accessChoice{
			Expr:              `g.Use(deps.Auth, deps.Authorize("role.admin"))`,
			AuthorizerID:      "role.admin",
			RateLimitPolicies: rateLimitPoliciesWithUser,
			Note: "模块要求 role.admin 授权。该标识**已接真实判定**：查 user_roles 表这一" +
				"权威角色源，表里有 admin 角色行的账号即通行。" +
				"未建表或表为空时一律拒绝，因此空库上的表现仍是 403——" +
				"但不要据此以为这条路由是关着的。" +
				"换成 IAM / IdP 同步表时只改 internal/runtime/module/account/module.go 里的角色源构造，路由代码无需改动。" +
				"绝不要改成仅凭 JWT 的 role claim 放行——那是一次签发当成持续有效的高权限。",
		}, nil
	case AccessOptionCustom:
		id := customAuthorizerID(moduleName)
		return accessChoice{
			Expr:              fmt.Sprintf("g.Use(deps.Auth, deps.Authorize(%q))", id),
			AuthorizerID:      id,
			OwnsAuthorizer:    true,
			RateLimitPolicies: rateLimitPoliciesWithUser,
			Note: fmt.Sprintf(
				"模块要求自定义授权 %s。该标识**尚未注册**，装配层会兜底为恒拒绝并打一条"+
					"错误日志：可编译、可启动，所有请求都被拒绝，不留 TODO、不默许放行。"+
					"接入真实实现的方式是在 providers.go 的 scaffold:module-authorizers 标记处"+
					"注册它，路由代码无需改动。", id),
		}, nil
	}

	return accessChoice{}, errors.New("unreachable access choice")
}

// customAuthorizerID 由模块名派生确定性的 authorizer ID。
//
// 确定性很重要：ID 同时出现在模块路由、registry 注册点与 metrics label 上，
// 随机或可变的 ID 会让三者对不上。
func customAuthorizerID(moduleName string) string {
	return "module." + moduleName + ".custom"
}
