package scaffold

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// accessChoice 是生成模块时必须做出的访问策略选择。
type accessChoice struct {
	// Expr 是写进生成代码的 AccessPolicy 字面量。
	Expr string
	// Note 出现在生成结果的提示里，说明该选择的后果与后续动作。
	Note string
}

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
			Expr: "routespec.AccessPolicy{Mode: routespec.AccessPublic}",
			Note: "模块声明为公开（--public）：所有生成的路由无需身份即可访问。",
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
			Expr: "routespec.AccessPolicy{Mode: routespec.AccessAuthenticated}",
			Note: "模块要求已认证身份。",
		}, nil
	case AccessOptionOptional:
		return accessChoice{
			Expr: "routespec.AccessPolicy{Mode: routespec.AccessOptionalAuthenticated}",
			Note: "模块允许匿名访问；一旦提供 token 则必须合法。" +
				"注意：user scope 的限流不能挂在可选认证路由上——匿名请求没有 user 维度。",
		}, nil
	case AccessOptionAdmin:
		return accessChoice{
			Expr: `routespec.AccessPolicy{Mode: routespec.AccessAuthorized, AuthorizerID: "role.admin"}`,
			Note: "模块要求 role.admin 授权。**当前没有权威角色源**，" +
				"role.admin 是恒拒绝的占位实现，所有请求都会 403。" +
				"接入数据库/IAM 等实时角色源后替换该 adapter，RouteSpec 无需改动。" +
				"绝不要改成仅凭 JWT 的 role claim 放行——那是一次签发当成持续有效的高权限。",
		}, nil
	case AccessOptionCustom:
		id := customAuthorizerID(moduleName)
		return accessChoice{
			Expr: fmt.Sprintf("routespec.AccessPolicy{Mode: routespec.AccessAuthorized, AuthorizerID: %q}", id),
			Note: fmt.Sprintf(
				"模块要求自定义授权 %s。已在 runtime 层生成一个恒拒绝的 adapter："+
					"可编译、可启动，但所有请求都被拒绝，不留 TODO、不默许放行。"+
					"替换该 adapter 时不需要改 RouteSpec。", id),
		}, nil
	}

	return accessChoice{}, errors.New("unreachable access choice")
}

// customAuthorizerID 由模块名派生确定性的 authorizer ID。
//
// 确定性很重要：ID 同时出现在 RouteSpec、registry 注册点与 metrics label 上，
// 随机或可变的 ID 会让三者对不上。
func customAuthorizerID(moduleName string) string {
	return "module." + moduleName + ".custom"
}
