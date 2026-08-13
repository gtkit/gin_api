package middleware

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/gtkit/goerr"
	jwtclaims "github.com/gtkit/gojwt/claims"

	"github.com/gtkit/gin-api/internal/pkg/errx"
	"github.com/gtkit/gin-api/internal/pkg/ginc"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
)

// token 只经请求头承载，`Authorization: Bearer` 为主要形态（标准形态，
// 网关/代理/工具链的默认支持面最大），App-Token 为存量客户端兼容保留。
//
// 刻意不接受 query 参数：query 会进访问日志、Referer 与浏览器历史，是长期泄漏面。
// 此前为 WebSocket 升级请求开的 query 旁路已删除——仓库里没有任何 WebSocket 端点，
// 那是一个零消费者的安全敞口。将来引入 WS 时须单独设计（如一次性票据换取连接），
// 不复活 query token。
const (
	tokenHeaderName         = "App-Token"
	authorizationHeaderName = "Authorization"
	bearerScheme            = "Bearer"
)

// maxTokenBytes 是 token 的长度上限。
//
// 上限在**解析与缓存之前**生效：没有上限时，攻击者可以用超长字符串迫使
// 解析器与缓存做无谓的工作，也会把巨大的字段写进日志。
const maxTokenBytes = 8 << 10 // 8 KiB

// TokenParser 抽象 token 验证能力。
//
// 带 ctx 是有意的：验签链路未来会接黑名单（一次 Redis 查询），
// 没有 ctx 就无法透传取消与超时——一个挂住的黑名单查询会把
// 每个请求都拖到超时，而那时再改签名会波及全部调用点。
type TokenParser interface {
	ParseToken(ctx context.Context, token string, opts ...gojwt.ParserOption) (*jwtclaims.Claims, error)
}

// RevocationChecker 报告访问令牌是否已被撤销。
//
// 它填上了 TokenParser 那个 ctx 参数当初预留的位置：撤销检查是一次 Redis
// 查询，需要透传取消与超时——一个挂住的查询会把每个请求都拖到超时。
type RevocationChecker interface {
	IsRevoked(ctx context.Context, tokenID string) (bool, error)
}

// AuthOptions 是认证中间件的依赖。
type AuthOptions struct {
	Verifier sharedauth.UserVerifier
	Parser   TokenParser
	// Revocation 检查令牌是否已被撤销；为 nil 时不做检查。
	//
	// 允许为 nil 是给**外部身份模式**留的：那种部署里令牌由外部签发，
	// 撤销也归外部，本服务没有撤销表可查。本地认证模式必须注入——
	// 缺了它登出不会产生任何效果，而配置校验已经在启动期拦下这种组合。
	Revocation RevocationChecker
	// AllowMissingToken 对应"可选认证"：缺失 token 时放行；
	// 但**一旦提供了 token，非法就必须拒绝**——否则攻击者只要发一个坏 token
	// 就能绕过校验以匿名身份继续。
	AllowMissingToken bool
}

// Auth 构造认证中间件，用法与任何 gin 中间件一致：
//
//	g := r.Group("/user", middleware.Auth(opts))
//
// 依赖缺失直接 panic：那是装配错误，发生在启动期而非请求期，
// 与 gin 生态的惯例一致（gin-contrib 的中间件同样在配置非法时 panic）。
//
// 本中间件**只做认证**（你是谁），不做任何业务角色判定（你能做什么）。
// 此前它硬编码校验 ClientRole，后果有二：任何不含该角色的 token 在认证阶段就被拒绝，
// 于是 admin/custom 之类的授权模式根本没有机会执行；而且"角色不符"被表达为 401，
// 客户端据此重新登录也无济于事——它实际是 403。
func Auth(opts AuthOptions) gin.HandlerFunc {
	if opts.Verifier == nil {
		panic("middleware: Auth requires a user verifier")
	}
	if opts.Parser == nil {
		panic("middleware: Auth requires a token parser")
	}

	return func(c *gin.Context) {
		token, err := tokenFromHeader(c)
		if err != nil {
			// 只有"完全没带 token"才允许匿名继续；带了但不合法一律拒绝。
			if opts.AllowMissingToken && errors.Is(err, errTokenAbsent) {
				c.Next()
				return
			}
			abortWith(c, goerr.New(err, goerr.StatusAuthHeader()))
			return
		}

		claims, err := opts.Parser.ParseToken(c.Request.Context(), token)
		if err != nil {
			// 纯 ctx 错误交终态中间件：解析器可能查了远端密钥集，
			// 请求预算到点或客户端断开都不是"凭据无效"。把它写成 401
			// 会让一次超时看起来像一次认证失败，客户端据此清掉本地令牌。
			if abortOnContextError(c, err) {
				return
			}
			abortWith(c, goerr.New(err, goerr.StatusAuth(), "token parse failed"))
			return
		}
		if claims == nil || claims.UserID <= 0 {
			abortWith(c, goerr.New(nil, goerr.StatusAuth(), "token subject is invalid"))
			return
		}

		ok, err := opts.Verifier.ValidateUser(c.Request.Context(), claims.UserID)
		if err != nil {
			// 请求预算到点 / 客户端断开交终态中间件裁决（504 / 不回包）：
			// 它们不是依赖不可用，写成 503 会让一次自身超时被记成下游故障。
			if abortOnContextError(c, err) {
				return
			}
			// 基础设施故障（如 DB 不可用）不能伪装成鉴权失败：返回 503，
			// 让客户端/网关按"服务暂时不可用"重试，而不是误判为"用户无效"(401)。
			abortWith(c, goerr.New(err, goerr.StatusDependencyUnavailable(), "user verification unavailable"))
			return
		}
		if !ok {
			abortWith(c, goerr.New(nil, goerr.StatusAuth(), "user is unavailable"))
			return
		}

		// 撤销检查放在最后：它是三项检查里唯一需要网络往返的，
		// 排在签名与用户有效性之后，非法请求不会白白消耗一次 Redis 查询。
		if opts.Revocation != nil {
			revoked, err := opts.Revocation.IsRevoked(c.Request.Context(), claims.TokenID)
			if err != nil && abortOnContextError(c, err) {
				return
			}
			if err != nil {
				// **故障必须拒绝，不能放行**。
				//
				// 把查询失败当作"未撤销"，会让一次 Redis 抖动变成"所有已登出的
				// 令牌重新生效"——恰恰在最需要它生效的时候失效。回 503 而不是
				// 401：这是依赖不可用，不是凭据无效，客户端据此重试才是对的。
				abortWith(c, goerr.New(err, goerr.StatusDependencyUnavailable(),
					"token revocation check unavailable"))
				return
			}
			if revoked {
				abortWith(c, goerr.New(nil, goerr.StatusAuth(), "token has been revoked"))
				return
			}
		}

		ginc.SetUserID(c, claims.UserID)
		ginc.SetPrincipal(c, sharedauth.Principal{UserID: claims.UserID, Roles: claims.Roles})
		// 记录令牌标识供登出撤销使用。ExpiresAt 决定撤销记录该活多久——
		// 缺了它就只能给撤销记录一个猜出来的存活时间。
		ginc.SetTokenIdentity(c, ginc.TokenIdentity{
			TokenID:   claims.TokenID,
			ExpiresAt: claimsExpiry(claims),
		})
		c.Next()
	}
}

// OptionalAuth 是"带了 token 就必须合法、没带则放行"的认证中间件。
func OptionalAuth(opts AuthOptions) gin.HandlerFunc {
	opts.AllowMissingToken = true
	return Auth(opts)
}

// Authorize 构造授权中间件，判定已认证主体能否执行本次请求。
//
// 它只消费**已经认证过**的 principal，不重复解析 token——
// 重复解析既浪费，也可能让两个阶段的判定不一致。
func Authorize(authorizer sharedauth.Authorizer, authorizerID string) gin.HandlerFunc {
	if authorizer == nil {
		panic(fmt.Sprintf("middleware: Authorize requires an authorizer for %q", authorizerID))
	}

	return func(c *gin.Context) {
		principal, ok := ginc.PrincipalFromContext(c)
		if !ok {
			// 授权必须排在认证之后。走到这里说明装配顺序错了，
			// 而"没有身份"绝不能被解释成"允许"。
			abortWith(c, goerr.New(nil, goerr.StatusAuth(), "authorization requires an authenticated principal"))
			return
		}

		allowed, err := authorizer.Authorize(c.Request.Context(), principal, authorizationRequest(c))
		if err != nil {
			// 依赖故障不是拒绝：把"角色数据源挂了"映射成 403，
			// 会让一次基础设施故障表现为全体用户突然失去权限。
			abortWith(c, goerr.New(err, goerr.StatusDependencyUnavailable(), "authorization unavailable"))
			return
		}
		if !allowed {
			abortWith(c, goerr.New(nil, goerr.StatusPermission(), "permission denied"))
			return
		}
		c.Next()
	}
}

// authorizationRequest 把 Gin 请求翻译成中立的授权输入。
//
// 只取路由模式与路径参数：用模式而非实际路径，才能作为低基数 metrics label；
// 不传 *gin.Context，避免授权实现读 body 或写响应。
func authorizationRequest(c *gin.Context) sharedauth.AuthorizationRequest {
	params := make(map[string]string, len(c.Params))
	for _, p := range c.Params {
		params[p.Key] = p.Value
	}
	return sharedauth.AuthorizationRequest{
		Method:       c.Request.Method,
		RoutePattern: c.FullPath(),
		PathParams:   params,
	}
}

func abortWith(c *gin.Context, err error) {
	resp.Error(c, err)
	c.Abort()
}

// abortOnContextError 把纯 ctx 错误交给终态中间件，返回是否已接管。
//
// 认证链上的每一步都可能做 I/O（远端密钥集、用户校验、撤销查询），而请求
// 预算到点与客户端断开是**请求级事实**，不是这一步的语义：写成 401 会让一次
// 超时看起来像凭据无效，写成 503 会让它看起来像下游故障。两者都让客户端与
// 网关按错误的方向处置。判定用纯错误树——混入真实故障的错误仍按各自语义走。
func abortOnContextError(c *gin.Context, err error) bool {
	if !errx.IsPureAbort(err) {
		return false
	}
	_ = c.Error(err)
	c.Abort()
	return true
}

// errTokenAbsent 区分"完全没带 token"与"带了但不合法"。
//
// 这个区分是可选认证模式的正确性前提：只有前者可以匿名放行，
// 后者若也放行，攻击者发一个坏 token 就能绕过校验。
var errTokenAbsent = errors.New("token is absent")

// tokenFromHeader 提取认证令牌（local-authentication spec）：
// Bearer 为主、App-Token 兼容；两头同时出现且值不一致即拒绝——
// 静默取其一会让代理注入的头悄悄改变认证身份。
// **同名头出现多个不一致取值同样拒绝**：Header.Get 只取第一个，而不同的
// 代理/框架取第一个还是最后一个并无共识——攻击者追加一个同名头，身份就
// 随下游的取值策略漂移。一致的重复容忍：有些代理会原样复制既有头。
func tokenFromHeader(c *gin.Context) (string, error) {
	bearer, err := bearerToken(c)
	if err != nil {
		return "", err
	}
	legacy, err := singleHeaderValue(c, tokenHeaderName)
	if err != nil {
		return "", err
	}

	switch {
	case bearer != "" && legacy != "" && bearer != legacy:
		return "", fmt.Errorf("authorization bearer token and %s header disagree", tokenHeaderName)
	case bearer != "":
		return validateRawToken(bearer, "authorization bearer token")
	case legacy != "":
		return validateRawToken(legacy, tokenHeaderName+" header")
	default:
		return "", fmt.Errorf("%w: neither an authorization bearer token nor the %s header is present",
			errTokenAbsent, tokenHeaderName)
	}
}

// singleHeaderValue 返回同名头的唯一取值；多个不一致取值即拒绝。
func singleHeaderValue(c *gin.Context, name string) (string, error) {
	values := c.Request.Header.Values(name)
	if len(values) == 0 {
		return "", nil
	}
	first := values[0]
	for _, v := range values[1:] {
		if v != first {
			return "", fmt.Errorf("request carries %d conflicting %s headers", len(values), name)
		}
	}
	return first, nil
}

// bearerToken 从 Authorization 头提取 Bearer 令牌。
//
// 头缺失或使用其他 scheme（如 Basic）→ 空串无错误：那不是本中间件的载体。
// scheme 匹配（大小写不敏感，RFC 7235）但令牌为空 → 报错：形态非法不是缺席。
// 同名头多个不一致取值 → 拒绝（见 tokenFromHeader 的说明）。
func bearerToken(c *gin.Context) (string, error) {
	raw, err := singleHeaderValue(c, authorizationHeaderName)
	if err != nil {
		return "", err
	}
	if raw == "" {
		return "", nil
	}
	scheme, token, found := strings.Cut(raw, " ")
	if !found || !strings.EqualFold(scheme, bearerScheme) {
		return "", nil
	}
	if token == "" {
		return "", errors.New("authorization header carries an empty bearer token")
	}
	return token, nil
}

// validateRawToken 对提取出的令牌做长度与空白校验，source 用于错误指认载体。
func validateRawToken(raw, source string) (string, error) {
	// 长度检查放在最前：先于任何解析与缓存，避免超长输入迫使下游做无谓的工作。
	if len(raw) > maxTokenBytes {
		return "", fmt.Errorf("%s exceeds %d bytes", source, maxTokenBytes)
	}
	// 首尾空白一律拒绝而不是修剪：修剪会让 " abc" 与 "abc" 成为同一个 token，
	// 从而在缓存键、审计与限流键上产生歧义。
	if strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, " \t\r\n") {
		return "", fmt.Errorf("%s contains whitespace", source)
	}
	return raw, nil
}

// claimsExpiry 取令牌的过期时刻。
//
// 缺失时返回零值：调用方（登出）据此判断这个令牌无法被撤销。
// 编一个"当前时间 + 默认 TTL"更糟——那会写出一条与真实寿命对不上的撤销记录，
// 短了则令牌提前复活，长了则撤销表白白占着空间。
func claimsExpiry(claims *jwtclaims.Claims) time.Time {
	if claims == nil || claims.ExpiresAt == nil {
		return time.Time{}
	}
	return claims.ExpiresAt.Time
}
