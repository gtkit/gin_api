package routespec

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

// allowedMethods 是模块可声明的 HTTP 方法白名单。
//
// 刻意不含 OPTIONS：预检响应必须由 CORS 策略统一派生，模块各写各的会产生两套预检来源。
// 也不含 TRACE / CONNECT：前者会回显请求内容（跨站追踪风险），后者是代理语义，业务路由用不到。
var allowedMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost,
	http.MethodPut, http.MethodPatch, http.MethodDelete,
}

// authorizerIDPattern 限定 authorizer 标识的字符集。
//
// 该 ID 会进日志与 metrics label，必须是低基数、稳定、不含用户输入的字面量。
var authorizerIDPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)

// rateLimitPolicyIDPattern 与 authorizer ID 同源：同样进 key 与 metrics label。
var rateLimitPolicyIDPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)

// CompiledRoute 是通过全部校验、可安全交给 engine 的一条路由。
type CompiledRoute struct {
	// FullPath 是加上版本前缀后的最终路径。
	FullPath string
	Spec     RouteSpec
}

// Contribution 是某个来源（一个业务模块，或系统路由）贡献的一组路由。
//
// Source 只用于错误信息定位——出问题时必须能一眼看出是哪个模块写错了，
// 而不是只给出一个孤零零的路径。
type Contribution struct {
	Source string
	Prefix string
	Routes []RouteSpec
}

// Compile 校验并合并全部贡献，返回可注册的路由集合。
//
// 关键约束：**必须在触碰生产 engine 之前完成**。Gin 的注册在冲突时会 panic，
// 若边注册边校验，进程会以 panic 形式崩溃，且此时 engine 已被写入一半。
// 先全量 compile、全部通过后再注册，失败就只是一个启动错误。
func Compile(contributions []Contribution) ([]CompiledRoute, error) {
	var (
		compiled []CompiledRoute
		errs     []error
		seen     = make(map[string]string) // "METHOD FULLPATH" -> source
	)

	for _, c := range contributions {
		for i, spec := range c.Routes {
			where := fmt.Sprintf("%s[%d] %s %s", c.Source, i, spec.Method, spec.Path)

			if err := validateSpec(spec); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", where, err))
				continue
			}

			full := joinPath(c.Prefix, spec.Path)
			key := spec.Method + " " + full
			if prev, dup := seen[key]; dup {
				errs = append(errs, fmt.Errorf(
					"%s: duplicate route %s (already contributed by %s)", where, key, prev))
				continue
			}
			seen[key] = c.Source

			compiled = append(compiled, CompiledRoute{FullPath: full, Spec: spec})
		}
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	// 路径参数树冲突（如 /user/:id 与 /user/:name）只有 Gin 自己知道，
	// 用一次性的 throwaway engine 探测，把它的 panic 转成启动错误。
	if err := detectPathTreeConflicts(compiled); err != nil {
		return nil, err
	}
	return compiled, nil
}

func validateSpec(spec RouteSpec) error {
	var errs []error

	if !slices.Contains(allowedMethods, spec.Method) {
		errs = append(errs, fmt.Errorf(
			"method %q is not allowed (allowed: %s; OPTIONS is derived from CORS, TRACE/CONNECT are rejected)",
			spec.Method, strings.Join(allowedMethods, ", ")))
	}
	if err := validatePath(spec.Path); err != nil {
		errs = append(errs, err)
	}
	if spec.Handler == nil {
		errs = append(errs, errors.New("handler must not be nil"))
	}
	if err := validateAccessPolicy(spec.AccessPolicy); err != nil {
		errs = append(errs, err)
	}
	if err := validateTimeoutPolicy(spec.TimeoutPolicy); err != nil {
		errs = append(errs, err)
	}
	if spec.RateLimitPolicyID != "" && !rateLimitPolicyIDPattern.MatchString(spec.RateLimitPolicyID) {
		errs = append(errs, fmt.Errorf(
			"rate limit policy id %q must match %s", spec.RateLimitPolicyID, rateLimitPolicyIDPattern.String()))
	}

	return errors.Join(errs...)
}

// validatePath 校验模块声明的相对路径。
func validatePath(p string) error {
	switch {
	case p == "":
		return errors.New("path must not be empty")
	case !strings.HasPrefix(p, "/"):
		return fmt.Errorf("path %q must start with /", p)
	case strings.Contains(p, "://"):
		return fmt.Errorf("path %q must not contain a scheme", p)
	case strings.ContainsAny(p, "?#"):
		return fmt.Errorf("path %q must not contain a query or fragment", p)
	case strings.Contains(p, "//"):
		return fmt.Errorf("path %q must not contain repeated slashes", p)
	}
	// `..` 在路由里没有合法用途，但很容易从字符串拼接里漏进来。
	for seg := range strings.SplitSeq(strings.TrimPrefix(p, "/"), "/") {
		if seg == ".." || seg == "." {
			return fmt.Errorf("path %q must not contain relative segments", p)
		}
	}
	// 非根路径的尾斜杠会与 RedirectTrailingSlash=false 组合成"注册了但访问不到"。
	if len(p) > 1 && strings.HasSuffix(p, "/") {
		return fmt.Errorf("path %q must not end with a trailing slash", p)
	}
	return nil
}

func validateAccessPolicy(p AccessPolicy) error {
	switch p.Mode {
	case AccessUnspecified:
		return errors.New(
			"access policy is unspecified: every route must declare one explicitly " +
				"(public endpoints must state AccessPublic on purpose)")
	case AccessPublic, AccessOptionalAuthenticated, AccessAuthenticated:
		if p.AuthorizerID != "" {
			return fmt.Errorf(
				"authorizer id must be empty for access mode %s, got %q", p.Mode, p.AuthorizerID)
		}
	case AccessAuthorized:
		if p.AuthorizerID == "" {
			return errors.New("access mode authorized requires a non-empty authorizer id")
		}
		if !authorizerIDPattern.MatchString(p.AuthorizerID) {
			return fmt.Errorf(
				"authorizer id %q must match %s", p.AuthorizerID, authorizerIDPattern.String())
		}
	default:
		return fmt.Errorf("unknown access mode %d", p.Mode)
	}
	return nil
}

func validateTimeoutPolicy(p TimeoutPolicy) error {
	switch p.Mode {
	case TimeoutUnspecified:
		return errors.New(
			"timeout policy is unspecified: every route must declare one explicitly " +
				"(a route without a budget can occupy a connection and a goroutine indefinitely)")
	case TimeoutCooperative, TimeoutClientVisible:
		var errs []error
		if p.Deadline <= 0 {
			errs = append(errs, fmt.Errorf("timeout mode %s requires a positive deadline", p.Mode))
		}
		if p.WriteGrace <= 0 {
			errs = append(errs, fmt.Errorf("timeout mode %s requires a positive write grace", p.Mode))
		}
		// 流式字段出现在非流式策略上，说明作者搞混了模式，而不是"多写了没关系"。
		if p.IdleWrite != 0 || p.HeartbeatInterval != 0 {
			errs = append(errs, fmt.Errorf(
				"timeout mode %s must not set streaming fields (idle write / heartbeat)", p.Mode))
		}
		return errors.Join(errs...)
	case TimeoutStreaming:
		var errs []error
		if p.IdleWrite <= 0 {
			errs = append(errs, errors.New("streaming timeout requires a positive idle write timeout"))
		}
		if p.HeartbeatInterval <= 0 {
			errs = append(errs, errors.New("streaming timeout requires a positive heartbeat interval"))
		}
		// 严格小于：write deadline 过期后再设置无法延长它，
		// 心跳不早于空闲超时就永远续不上，只能断连。
		if p.IdleWrite > 0 && p.HeartbeatInterval >= p.IdleWrite {
			errs = append(errs, fmt.Errorf(
				"heartbeat interval (%s) must be strictly shorter than the idle write timeout (%s): "+
					"a write deadline cannot be extended once it has expired",
				p.HeartbeatInterval, p.IdleWrite))
		}
		if p.Deadline != 0 || p.WriteGrace != 0 {
			errs = append(errs, errors.New(
				"streaming timeout must not set a total deadline or write grace"))
		}
		return errors.Join(errs...)
	default:
		return fmt.Errorf("unknown timeout mode %d", p.Mode)
	}
}

// joinPath 拼接版本前缀与模块相对路径。
func joinPath(prefix, p string) string {
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix == "" {
		return p
	}
	// 模块路径 "/" 表示"就是前缀本身"（如 /api/v1/user 的 GET）。
	if p == "/" {
		return prefix
	}
	return prefix + p
}
