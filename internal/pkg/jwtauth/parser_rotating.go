package jwtauth

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	jwtv5 "github.com/golang-jwt/jwt/v5"
	"github.com/gtkit/gojwt/claims"
)

// Parser 实现 RotatingTokenParser。
type Parser struct {
	keys  *KeySet
	opts  ParserOptions
	cache *claimsCache
	now   func() time.Time
}

// NewParser 构造验签器。
//
// cacheMaxEntries 为 0 表示禁用缓存；负值即配置错误（一个"负容量"的缓存
// 没有任何合理解释，静默当成 0 会掩盖配置里的笔误）。
func NewParser(keys *KeySet, opts ParserOptions, cacheMaxEntries int) (*Parser, error) {
	if keys == nil {
		return nil, errors.New("jwt parser requires a key set")
	}

	var errs []error
	if opts.Issuer == "" {
		errs = append(errs, errors.New("jwt.issuer is required for verification"))
	}
	if len(opts.Audiences) == 0 {
		errs = append(errs, errors.New("jwt.audiences must contain at least one entry for verification"))
	}
	if opts.ClockSkew < 0 {
		errs = append(errs, errors.New("jwt clock skew must not be negative"))
	}
	if opts.ClockSkew > maxClockSkew {
		errs = append(errs, fmt.Errorf(
			"jwt clock skew %s exceeds the %s ceiling: a wide skew window silently relaxes "+
				"expiry checking, and a value like 24h effectively disables it",
			opts.ClockSkew, maxClockSkew))
	}
	if cacheMaxEntries < 0 {
		errs = append(errs, fmt.Errorf("jwt cache max entries must not be negative, got %d", cacheMaxEntries))
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	return &Parser{keys: keys, opts: opts, cache: newClaimsCache(cacheMaxEntries), now: time.Now}, nil
}

// ParseToken 验签并校验全部标准 claims。
//
// 传入任意 ParserOption 时**绕过缓存**：那些选项会改变单次解析的判定条件，
// 而缓存里的结果是按默认条件验出来的。命中缓存等于用宽松条件的结果
// 冒充严格条件的结论。
func (p *Parser) ParseToken(
	ctx context.Context, token string, opts ...jwtv5.ParserOption,
) (*claims.Claims, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	useCache := len(opts) == 0 && p.cache != nil
	if useCache {
		if cached, ok := p.cache.get(token, p.now()); ok {
			// 缓存命中后仍然要走后续检查点（当前只有 ctx；黑名单接线后
			// 检查位置在这里）——撤销绝不能被缓存绕过。
			return cached, nil
		}
	}

	parsed, err := p.parseUncached(token, opts...)
	if err != nil {
		return nil, err
	}

	if useCache {
		p.cache.put(token, parsed)
	}
	// 返回深拷贝：调用方修改返回值不能影响缓存里的条目。
	return cloneClaims(parsed), nil
}

// parseUncached 执行真正的验签。
func (p *Parser) parseUncached(token string, opts ...jwtv5.ParserOption) (*claims.Claims, error) {
	// 安全选项**最后**应用：jwtv5 后设选项覆盖先设的，调用方传入的
	// WithValidMethods / WithoutClaimsValidation 之类若排在后面，就能把
	// 算法白名单与 exp 必需一起关掉——一个本意是"补充"的入口变成了绕过口。
	parserOpts := append(append([]jwtv5.ParserOption{}, opts...), p.baseParserOptions()...)

	parsed, err := jwtv5.ParseWithClaims(token, &claims.Claims{}, p.keyFunc, parserOpts...)
	if err != nil {
		return nil, normalizeParseError(err)
	}
	if !parsed.Valid {
		return nil, ErrTokenInvalid
	}

	c, ok := parsed.Claims.(*claims.Claims)
	if !ok {
		return nil, ErrTokenInvalid
	}

	hasKID := headerKID(parsed) != ""
	if err := p.verifyClaims(c, hasKID); err != nil {
		return nil, err
	}
	return c, nil
}

// baseParserOptions 是默认的解析选项。
//
// 算法**钉死为 EdDSA**：按 token 自己声明的 alg 分流等于信任攻击者输入——
// 那是 JWT 最经典的一类漏洞（把 alg 改成 none 或对称算法）。
func (p *Parser) baseParserOptions() []jwtv5.ParserOption {
	return []jwtv5.ParserOption{
		jwtv5.WithValidMethods([]string{jwtv5.SigningMethodEdDSA.Alg()}),
		jwtv5.WithLeeway(p.opts.ClockSkew),
		jwtv5.WithExpirationRequired(),
	}
}

// keyFunc 按 kid 选公钥。
func (p *Parser) keyFunc(token *jwtv5.Token) (any, error) {
	kid := headerKID(token)

	if kid != "" {
		key, ok := p.keys.PublicKeyFor(kid)
		if !ok {
			// **不做遍历兜底**。回退遍历会让 kid 形同虚设：攻击者把 header
			// 里的 kid 改成任意值，验签就退回"逐个试"。
			return nil, ErrUnknownKID
		}
		return key, nil
	}

	// 无 kid 的存量 token：只在明确的截止时刻之前接受。
	if !p.unkeyedTokenAllowed() {
		return nil, ErrUnkeyedTokenRejected
	}
	return p.orderedKeyFunc(token)
}

// orderedKeyFunc 为无 kid 的存量 token 逐个尝试公钥。
//
// jwtv5 的 keyFunc 只能返回一把密钥，因此这里返回一个 VerificationKeySet，
// 由库按顺序尝试。顺序固定为 active 优先——绝大多数存量 token 由它签出。
func (p *Parser) orderedKeyFunc(*jwtv5.Token) (any, error) {
	ordered := p.keys.OrderedKeys()
	keys := make([]jwtv5.VerificationKey, 0, len(ordered))
	for _, k := range ordered {
		keys = append(keys, k.Public)
	}
	return jwtv5.VerificationKeySet{Keys: keys}, nil
}

// unkeyedTokenAllowed 报告当前是否仍接受无 kid 的令牌。
func (p *Parser) unkeyedTokenAllowed() bool {
	if p.opts.UnkeyedTokenAcceptUntil.IsZero() {
		return false
	}
	return p.now().Before(p.opts.UnkeyedTokenAcceptUntil)
}

// verifyClaims 校验标准与业务 claims。
//
// hasKID 为 false（存量令牌）时，缺失 iss/aud/sub 可以放行——但**存在即必须匹配**。
// jti 与 token_id 的一致性任何情况下都不放宽：它们是撤销与审计的锚点，
// 分叉意味着"按 ID 撤销"会出现两个都对不上的候选值。
func (p *Parser) verifyClaims(c *claims.Claims, hasKID bool) error {
	var errs []error

	if c.Issuer != "" && c.Issuer != p.opts.Issuer {
		errs = append(errs, fmt.Errorf("%w: issuer mismatch", ErrTokenInvalid))
	} else if c.Issuer == "" && hasKID {
		errs = append(errs, fmt.Errorf("%w: issuer is required", ErrTokenInvalid))
	}

	if len(c.Audience) > 0 {
		if !hasAnyAudience(c.Audience, p.opts.Audiences) {
			errs = append(errs, fmt.Errorf("%w: audience mismatch", ErrTokenInvalid))
		}
	} else if hasKID {
		errs = append(errs, fmt.Errorf("%w: audience is required", ErrTokenInvalid))
	}

	switch {
	case c.Subject != "":
		if c.Subject != strconv.FormatInt(c.UserID, 10) {
			errs = append(errs, fmt.Errorf("%w: subject does not match uid", ErrTokenInvalid))
		}
	case hasKID:
		errs = append(errs, fmt.Errorf("%w: subject is required", ErrTokenInvalid))
	}

	if c.ID == "" || c.TokenID == "" || c.ID != c.TokenID {
		errs = append(errs, fmt.Errorf(
			"%w: jti and token_id must both be present and equal", ErrTokenInvalid))
	}

	if c.IssuedAt == nil || c.NotBefore == nil || c.ExpiresAt == nil {
		errs = append(errs, fmt.Errorf("%w: iat/nbf/exp are all required", ErrTokenInvalid))
	}

	return errors.Join(errs...)
}

// hasAnyAudience 报告令牌的 audience 是否命中任一允许值。
func hasAnyAudience(tokenAudience jwtv5.ClaimStrings, allowed []string) bool {
	for _, aud := range tokenAudience {
		if slices.Contains(allowed, aud) {
			return true
		}
	}
	return false
}

// headerKID 取 header 里的 kid。
func headerKID(token *jwtv5.Token) string {
	raw, ok := token.Header["kid"]
	if !ok {
		return ""
	}
	kid, ok := raw.(string)
	if !ok {
		return ""
	}
	return kid
}

// normalizeParseError 把底层库的错误归一到本包的哨兵错误。
//
// 归一化是必需的：上层用 errors.Is 分流"无效"与"过期"（前者要求重新登录，
// 后者可以尝试刷新）。直接透出底层错误，上层的分支会在换库或换版本时
// 静默失效——而失效的方式是"所有令牌都被当成无效"，用户被迫全部重登。
func normalizeParseError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrUnknownKID):
		return ErrUnknownKID
	case errors.Is(err, ErrUnkeyedTokenRejected):
		return ErrUnkeyedTokenRejected
	case errors.Is(err, jwtv5.ErrTokenExpired):
		return fmt.Errorf("%w: %w", ErrTokenExpired, err)
	default:
		return fmt.Errorf("%w: %w", ErrTokenInvalid, err)
	}
}

// cloneClaims 深拷贝 claims。
//
// 缓存返回共享指针的话，任何一个调用方修改返回值都会污染后续所有命中——
// 而那种污染在测试里几乎不可能复现（取决于请求顺序）。
func cloneClaims(c *claims.Claims) *claims.Claims {
	if c == nil {
		return nil
	}
	cloned := *c
	cloned.Audience = slices.Clone(c.Audience)
	cloned.Roles = slices.Clone(c.Roles)
	return &cloned
}

// 编译期确认实现满足接口。
var (
	_ RotatingTokenParser = (*Parser)(nil)
	_ RotatingTokenIssuer = (*Issuer)(nil)
	_ ed25519.PublicKey   = ed25519.PublicKey(nil)
)
