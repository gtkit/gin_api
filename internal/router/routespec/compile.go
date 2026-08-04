package routespec

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

// operationIDPattern 约束 operationId 的字符集。
//
// 必须是合法的标识符形状：它会成为生成客户端里的方法名，
// 带空格或斜杠的值在多数生成器里要么报错、要么被静默改写。
var operationIDPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)

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
//
// 本包只校验描述符里的**引用**长什么样。策略本身的定义、取值枚举与自洽校验
// 在 pkg/ratelimit——协议描述包不认识令牌桶，也不该认识。
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
		// operations 保证 operationId 全局唯一。
		operations = make(map[string]string) // operationId -> source
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

			if prev, dup := operations[spec.OperationID]; dup {
				errs = append(errs, fmt.Errorf(
					"%s: duplicate operation id %q (already used by %s)", where, spec.OperationID, prev))
				continue
			}
			operations[spec.OperationID] = c.Source

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
	if err := validateTimeoutMethodConstraint(spec.Method, spec.TimeoutPolicy); err != nil {
		errs = append(errs, err)
	}
	if !operationIDPattern.MatchString(spec.OperationID) {
		errs = append(errs, fmt.Errorf(
			"operation id %q must match %s: it names the generated client method, so it has to be "+
				"a stable identifier", spec.OperationID, operationIDPattern.String()))
	}
	errs = append(errs, validateRateLimitPolicyIDs(spec.RateLimitPolicyIDs)...)
	if err := validateBodyDeclaration(spec); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// methodsWithRequestBody 是必须声明请求体的方法。
//
// DELETE 不在其中：它的请求体语义在 HTTP 规范里未定义，代理与中间件对它的
// 处理并不一致，因此本仓不通过 DELETE 传递请求体。
var methodsWithRequestBody = []string{http.MethodPost, http.MethodPut, http.MethodPatch}

// validateBodyDeclaration 要求请求体与响应体**显式**声明。
//
// 沉默不能当作"没有"：漏声明与确实没有在代码里长得一模一样，而漏声明的
// 后果是契约里缺掉这个接口最重要的部分——客户端生成器会产出一个不接受
// 参数、或不返回数据的方法，而调用它的人要到联调时才发现。
func validateBodyDeclaration(spec RouteSpec) error {
	var errs []error

	errs = append(errs, validateRequestBodyPolicy(spec)...)

	switch spec.ResponseShape {
	case ResponseShapeEnveloped, ResponseShapeRaw:
		if spec.ResponseBody == nil {
			errs = append(errs, fmt.Errorf(
				"ResponseShape %s requires ResponseBody (a sample value such as dto.Xxx{})",
				spec.ResponseShape))
		}
	case ResponseShapeEmpty:
		if spec.ResponseBody != nil {
			errs = append(errs, errors.New(
				"ResponseShape empty must not carry a ResponseBody"))
		}
	case ResponseShapeUnspecified:
		errs = append(errs, errors.New(
			"ResponseShape must be declared (enveloped, raw or empty)"))
	default:
		errs = append(errs, fmt.Errorf("unknown ResponseShape %d", spec.ResponseShape))
	}

	return errors.Join(errs...)
}

// maxRouteBodyBytes 是路由级上限的绝对天花板。
//
// 需要一个天花板：路由级上限的意义是**收紧**，而一个写错的巨大值会静默
// 变成"这条路由没有上限"，且它比全局配置更难被注意到。
const maxRouteBodyBytes = 1 << 30 // 1 GiB

// validateRequestBodyPolicy 校验请求体契约。
//
// 读方法的零值合法且视为"不接受请求体"：GET/HEAD/DELETE 的请求体语义在 HTTP
// 规范里未定义，本仓不通过它们传递请求体，因此那里不存在"漏写导致危险默认"。
// 写方法则不同——JSON、multipart 与"没有请求体"的处置完全不同，零值必须被拒。
func validateRequestBodyPolicy(spec RouteSpec) []error {
	var errs []error
	policy := spec.RequestBodyPolicy
	writeMethod := slices.Contains(methodsWithRequestBody, spec.Method)

	switch {
	case writeMethod && policy.Mode == BodyModeUnspecified:
		errs = append(errs, fmt.Errorf(
			"%s must declare RequestBodyPolicy.Mode (none, json, form or multipart)", spec.Method))
	case !writeMethod && policy.Mode != BodyModeUnspecified && policy.Mode != BodyModeNone:
		errs = append(errs, fmt.Errorf(
			"%s must not accept a request body, got mode %s", spec.Method, policy.Mode))
	}

	if policy.MaxBytes < 0 || policy.MaxBytes > maxRouteBodyBytes {
		errs = append(errs, fmt.Errorf(
			"RequestBodyPolicy.MaxBytes %d is out of range (0 means inherit the global limit, max %d)",
			policy.MaxBytes, maxRouteBodyBytes))
	}
	if policy.Mode == BodyModeNone && policy.MaxBytes > 0 {
		errs = append(errs, errors.New(
			"RequestBodyPolicy.MaxBytes is meaningless when the route accepts no body"))
	}

	// 契约声明与运行时治理必须同源：两处各写各的会出现"中间件按 JSON 放行、
	// 契约却说没有请求体"这种分叉，而分叉只在客户端接入时才暴露。
	structured := policy.Mode == BodyModeJSON || policy.Mode == BodyModeForm ||
		policy.Mode == BodyModeMultipart
	switch {
	case structured && spec.RequestBody == nil:
		errs = append(errs, fmt.Errorf(
			"RequestBodyPolicy.Mode %s requires RequestBody (a sample value such as dto.Xxx{})",
			policy.Mode))
	case !structured && spec.RequestBody != nil:
		errs = append(errs, fmt.Errorf(
			"RequestBody must be nil when the mode is %s", policy.Mode))
	}

	return errs
}

// maxRateLimitPoliciesPerRoute 是单条路由可引用的策略数上限。
//
// 每条策略都是一次后端往返。挂十几条会让该路由的延迟随策略数线性增长，
// 而这种劣化只在高负载下显现——那时再回头数策略条数已经很晚了。
// 四条足以表达"来源 + 身份"各配一档短窗口与一档长窗口。
const maxRateLimitPoliciesPerRoute = 4

// validateRateLimitPolicyIDs 校验策略引用。
func validateRateLimitPolicyIDs(ids []string) []error {
	if len(ids) == 0 {
		return nil
	}

	var errs []error
	if len(ids) > maxRateLimitPoliciesPerRoute {
		errs = append(errs, fmt.Errorf(
			"a route may reference at most %d rate limit policies, got %d: each one costs a backend "+
				"round trip, so the route latency grows with the count",
			maxRateLimitPoliciesPerRoute, len(ids)))
	}

	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !rateLimitPolicyIDPattern.MatchString(id) {
			errs = append(errs, fmt.Errorf(
				"rate limit policy id %q must match %s", id, rateLimitPolicyIDPattern.String()))
			continue
		}
		if seen[id] {
			// 重复不会让限流更严：同一个桶在一次请求里被扣两次，实际配额
			// 因此是配置值的一半，而配置上完全看不出来。
			errs = append(errs, fmt.Errorf(
				"rate limit policy %q is referenced twice by the same route: the bucket would be "+
					"charged twice per request, halving the effective quota", id))
			continue
		}
		seen[id] = true
	}
	return errs
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

// 超时上界。
//
// 存在的理由是"用超大 duration 等价关闭保护"这条真实路径：没有上界时，
// 想绕过保护的人不必删掉策略——写一个 24h 就够了，而代码审查里
// 一个填了值的字段看起来完全合规。
//
// 更长的任务应当改成流式或异步，而不是把请求挂在那里。
const (
	// maxRouteDeadline 是业务预算上界。
	maxRouteDeadline = 5 * time.Minute
	// maxWriteGrace 是 socket 写宽限上界。它只覆盖"把响应刷给慢客户端"，
	// 不覆盖业务本身，因此远小于业务预算。
	maxWriteGrace = 30 * time.Second
	// maxIdleWrite 是流式写空闲超时上界。
	maxIdleWrite = 5 * time.Minute
)

// validateTimeoutMethodConstraint 校验超时模式与 HTTP 方法的组合。
//
// ClientVisible 在本阶段只允许 GET/HEAD。原因是它的语义：到点先给客户端写 504，
// 但 handler goroutine **仍在继续跑**。对写方法而言，客户端已经收到失败、
// 服务端却把写操作做完了——客户端据此重试就会产生重复副作用。
//
// 放开写方法的前提是有显式的幂等策略元数据（批次 6），在那之前一律拒绝。
func validateTimeoutMethodConstraint(method string, p TimeoutPolicy) error {
	if p.Mode != TimeoutClientVisible {
		return nil
	}
	if method == http.MethodGet || method == http.MethodHead {
		return nil
	}
	return fmt.Errorf(
		"timeout mode %s is not allowed on %s: it returns 504 to the client while the handler "+
			"keeps running, so a client retry would duplicate the side effect; "+
			"use %s until explicit idempotency metadata exists",
		p.Mode, method, TimeoutCooperative)
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
		if p.Deadline > maxRouteDeadline {
			errs = append(errs, fmt.Errorf(
				"timeout mode %s deadline %s exceeds the %s ceiling: a longer task belongs in a "+
					"streaming route or an async job, not in a request with the protection turned off",
				p.Mode, p.Deadline, maxRouteDeadline))
		}
		if p.WriteGrace <= 0 {
			errs = append(errs, fmt.Errorf("timeout mode %s requires a positive write grace", p.Mode))
		}
		if p.WriteGrace > maxWriteGrace {
			errs = append(errs, fmt.Errorf(
				"timeout mode %s write grace %s exceeds the %s ceiling: the grace only covers "+
					"flushing a response to a slow client, not the business work itself",
				p.Mode, p.WriteGrace, maxWriteGrace))
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
		if p.IdleWrite > maxIdleWrite {
			errs = append(errs, fmt.Errorf(
				"streaming idle write timeout %s exceeds the %s ceiling: an oversized duration is "+
					"just the protection switched off under another name", p.IdleWrite, maxIdleWrite))
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
