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
	"github.com/gtkit/gojwt"
	"github.com/gtkit/gojwt/claims"
)

// Parser 实现 RotatingTokenParser。
type Parser struct {
	opts    ParserOptions
	cache   *claimsCache
	now     func() time.Time
	byKID   map[string]*gojwt.JwtEd25519
	ordered []*gojwt.JwtEd25519
}

// NewParser 构造验签器。
//
// cacheMaxEntries 为 0 表示禁用缓存；负值即配置错误（一个"负容量"的缓存
// 没有任何合理解释，静默当成 0 会掩盖配置里的笔误）。
func NewParser(keys *KeySet, opts ParserOptions, cacheMaxEntries int) (*Parser, error) {
	if keys == nil || len(keys.OrderedKeys()) == 0 {
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

	p := &Parser{
		opts: opts, cache: newClaimsCache(cacheMaxEntries), now: time.Now,
		byKID: make(map[string]*gojwt.JwtEd25519),
	}
	for _, key := range keys.OrderedKeys() {
		verifier, err := gojwt.NewJwtEd25519Verifier(key.Public)
		if err != nil {
			return nil, fmt.Errorf("jwt verifier %q: %w", key.KID, err)
		}
		p.byKID[key.KID] = verifier
		p.ordered = append(p.ordered, verifier)
	}
	return p, nil
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

	parsed, keyed, err := p.parseUncached(ctx, token, opts...)
	if err != nil {
		return nil, err
	}

	// 无 kid 的迁移令牌逐次检查截止时间，避免 exp 更晚的缓存越过迁移窗口。
	if useCache && keyed {
		p.cache.put(token, parsed)
	}
	// 返回深拷贝：调用方修改返回值不能影响缓存里的条目。
	return cloneClaims(parsed), nil
}

// parseUncached 执行真正的验签。
func (p *Parser) parseUncached(ctx context.Context, token string, opts ...jwtv5.ParserOption) (*claims.Claims, bool, error) {
	if len(token) > gojwt.MaxTokenBytes {
		return nil, false, fmt.Errorf("%w: token exceeds %d bytes", ErrTokenInvalid, gojwt.MaxTokenBytes)
	}
	// 此处只选键并拒绝缺失的业务字段，不信任未验签结果。最终验签和日期
	// 解码交给 SDK，保留其纳秒精度、严格 JSON 和算法检查，避免修改全局 TimePrecision。
	rawClaims := &claims.Claims{}
	header, _, err := jwtv5.NewParser().ParseUnverified(token, rawClaims)
	if err != nil {
		return nil, false, normalizeParseError(err)
	}
	if header.Method.Alg() != jwtv5.SigningMethodEdDSA.Alg() {
		return nil, false, ErrTokenInvalid
	}
	kid := headerKID(header)
	verifiers := p.ordered
	if kid != "" {
		verifier, ok := p.byKID[kid]
		if !ok {
			return nil, false, ErrUnknownKID
		}
		verifiers = []*gojwt.JwtEd25519{verifier}
	} else if !p.unkeyedTokenAllowed() {
		return nil, false, ErrUnkeyedTokenRejected
	}
	// SDK 兼容旧 token 时会补齐 token_id；应用策略要求原始 jti/token_id
	// 都存在，必须在该规范化之前检查，不能让升级放宽撤销锚点的要求。
	if err := p.verifyClaims(rawClaims, kid != ""); err != nil {
		return nil, false, err
	}
	// 安全选项**最后**应用：jwtv5 后设选项覆盖先设的，调用方传入的
	// WithValidMethods / WithoutClaimsValidation 之类若排在后面，就能把
	// 算法白名单与 exp 必需一起关掉——一个本意是"补充"的入口变成了绕过口。
	parserOpts := append(append([]jwtv5.ParserOption{}, opts...), p.baseParserOptions()...)

	for _, verifier := range verifiers {
		var c *claims.Claims
		c, err = verifier.ParseTokenContext(ctx, token, parserOpts...)
		if err == nil {
			return c, kid != "", nil
		}
		// 仅无 kid 存量 token 可以尝试下一把公钥；过期、格式和策略错误
		// 不能通过换钥兜底。具名 token 的候选集始终只有一把密钥。
		if !errors.Is(err, jwtv5.ErrTokenSignatureInvalid) {
			break
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, false, err
	}
	return nil, false, normalizeParseError(err)
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
	if c.ExpiresAt != nil {
		cloned.ExpiresAt = new(*c.ExpiresAt)
	}
	if c.IssuedAt != nil {
		cloned.IssuedAt = new(*c.IssuedAt)
	}
	if c.NotBefore != nil {
		cloned.NotBefore = new(*c.NotBefore)
	}
	if c.AuthTime != nil {
		cloned.AuthTime = new(*c.AuthTime)
	}
	if c.Ext != nil {
		cloned.Ext = make(map[string]any, len(c.Ext))
		for key, value := range c.Ext {
			cloned.Ext[key] = cloneClaimValue(value)
		}
	}
	if c.Custom != nil {
		cloned.Custom = make(map[string]any, len(c.Custom))
		for key, value := range c.Custom {
			cloned.Custom[key] = cloneClaimValue(value)
		}
	}
	return &cloned
}

// cloneClaimValue 复制从 JSON 解码得到的扩展字段，避免嵌套 map/slice 共享缓存内容。
func cloneClaimValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(v))
		for key, item := range v {
			cloned[key] = cloneClaimValue(item)
		}
		return cloned
	case []any:
		cloned := make([]any, len(v))
		for index, item := range v {
			cloned[index] = cloneClaimValue(item)
		}
		return cloned
	default:
		return value
	}
}

// 编译期确认实现满足接口。
var (
	_ RotatingTokenParser = (*Parser)(nil)
	_ RotatingTokenIssuer = (*Issuer)(nil)
	_ ed25519.PublicKey   = ed25519.PublicKey(nil)
)
