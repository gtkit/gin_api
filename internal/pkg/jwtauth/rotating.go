package jwtauth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	jwtv5 "github.com/golang-jwt/jwt/v5"
	"github.com/gtkit/gojwt"
	"github.com/gtkit/gojwt/claims"
)

// 验签失败的归一化错误。
//
// 归一化是必需的：上层用 errors.Is 分流"令牌无效"与"令牌过期"，
// 直接把底层库的错误透出去，上层的分支会在换库或换版本时静默失效。
var (
	// ErrTokenInvalid 表示令牌不可信（签名、结构、算法、kid 任一不通过）。
	ErrTokenInvalid = errors.New("jwtauth: token is invalid")
	// ErrTokenExpired 表示令牌已过期。
	ErrTokenExpired = errors.New("jwtauth: token is expired")
	// ErrUnknownKID 表示令牌声明了一个本进程不认识的 kid。
	ErrUnknownKID = errors.New("jwtauth: unknown key id")
	// ErrUnkeyedTokenRejected 表示无 kid 的存量令牌已过接受期限。
	ErrUnkeyedTokenRejected = errors.New("jwtauth: tokens without a key id are no longer accepted")
)

// IssuerOptions 是签发侧的必需配置。
//
// issuer 与 audiences 都是**必填**：一个不声明自己是谁、也不声明发给谁的
// 令牌，在多服务共享密钥或密钥意外复用时无法区分用途——拿到 A 服务的令牌
// 就能用在 B 服务上。
type IssuerOptions struct {
	Issuer    string
	Audiences []string
	TTL       time.Duration
	// Prv 是业务域标识，写入 claims 保持存量 shape。
	Prv string
}

// ParserOptions 是验签侧的配置。
type ParserOptions struct {
	Issuer    string
	Audiences []string
	// ClockSkew 允许的时钟偏移，上限固定为 maxClockSkew。
	ClockSkew time.Duration
	// UnkeyedTokenAcceptUntil 是无 kid 存量令牌的**绝对**接受截止时刻。
	//
	// 零值表示不接受任何无 kid 令牌。用绝对时刻而不是时长：
	// 用时长的话每次重启都会自动顺延，兼容口就变成永久的。
	UnkeyedTokenAcceptUntil time.Time
}

// RotatingTokenIssuer 只用 active 密钥签发，header 必带 kid。
//
// ctx 参数目前未使用，但保留在签名里：签发侧未来会接黑名单预检或审计写入，
// 那时需要透传取消与超时；等到那时再改签名会波及全部调用点。
type RotatingTokenIssuer interface {
	Issue(ctx context.Context, uid int64, roles ...string) (string, error)
	// Refresh 验签走 parser 全集，重签只用 active——即刷新会把旧 kid 的令牌换成新 kid。
	Refresh(ctx context.Context, token string) (string, error)
	ActiveKID() string
}

// RotatingTokenParser 按 kid 选键验签。
type RotatingTokenParser interface {
	ParseToken(ctx context.Context, token string, opts ...jwtv5.ParserOption) (*claims.Claims, error)
}

// Issuer 实现 RotatingTokenIssuer。
//
// 使用 gojwt 签发，WithKeyID 将 active kid 写入令牌头；验签仍由轮换 parser 负责。
type Issuer struct {
	keys   *KeySet
	opts   IssuerOptions
	parser RotatingTokenParser
	signer *gojwt.JwtEd25519
}

// NewIssuer 构造签发器。
func NewIssuer(keys *KeySet, parser RotatingTokenParser, opts IssuerOptions) (*Issuer, error) {
	if keys == nil {
		return nil, errors.New("jwt issuer requires a key set")
	}
	if len(keys.SigningKey()) != ed25519.PrivateKeySize {
		return nil, errors.New("jwt issuer requires a valid signing private key")
	}
	var errs []error
	if opts.Issuer == "" {
		errs = append(errs, errors.New("jwt.issuer is required: an unidentified token cannot be scoped"))
	}
	if len(opts.Audiences) == 0 {
		errs = append(errs, errors.New(
			"jwt.audiences must contain at least one entry: without it a token issued for one "+
				"service can be replayed against another that shares the key"))
	}
	if opts.TTL <= 0 {
		errs = append(errs, errors.New("jwt token ttl must be positive"))
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	public, _ := keys.PublicKeyFor(keys.ActiveKID())
	signer, err := gojwt.NewJwtEd25519FromKeys(keys.SigningKey(), public,
		gojwt.WithKeyID(keys.ActiveKID()),
		gojwt.WithTokenDuration(opts.TTL),
		// gojwt 要求刷新窗口不短于 token TTL；本项目的 Refresh 仍经轮换 parser 重签。
		gojwt.WithRefreshDuration(opts.TTL),
	)
	if err != nil {
		return nil, fmt.Errorf("build gojwt issuer: %w", err)
	}
	opts.Audiences = slices.Clone(opts.Audiences)
	return &Issuer{keys: keys, opts: opts, parser: parser, signer: signer}, nil
}

// ActiveKID 返回签发用的 kid。
func (i *Issuer) ActiveKID() string { return i.keys.ActiveKID() }

// Issue 用 active 密钥签发一个新令牌。
func (i *Issuer) Issue(_ context.Context, uid int64, roles ...string) (string, error) {
	tokenID, err := newTokenID()
	if err != nil {
		return "", err
	}

	// 保留现有 128-bit 十六进制 ID；gojwt 同步 jti 与 token_id。
	signed, err := i.signer.GenerateToken(uid,
		claims.WithIssuer(i.opts.Issuer),
		claims.WithAudience(i.opts.Audiences...),
		claims.WithSubject(formatUID(uid)),
		claims.WithJwtID(tokenID),
		claims.WithPrv(i.opts.Prv),
		claims.WithRoles(roles...),
	)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return signed, nil
}

// Refresh 验签后用 active 密钥重签。
//
// 重签意味着旧 kid 的令牌会换成新 kid——这正是轮换的收敛机制：
// 活跃用户在重叠窗口内自然迁移到新密钥，无需强制重登。
func (i *Issuer) Refresh(ctx context.Context, token string) (string, error) {
	if i.parser == nil {
		return "", errors.New("jwt issuer cannot refresh without a parser")
	}

	parsed, err := i.parser.ParseToken(ctx, token)
	if err != nil {
		return "", err
	}
	return i.Issue(ctx, parsed.UserID, parsed.Roles...)
}

// newTokenID 生成 128-bit 随机令牌 ID。
func newTokenID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate token id: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

// formatUID 把用户 ID 渲染成十进制字符串（写入 sub）。
func formatUID(uid int64) string {
	return fmt.Sprintf("%d", uid)
}
