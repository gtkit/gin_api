package jwtauth

import (
	"context"
	"errors"
	"time"

	jwtv5 "github.com/golang-jwt/jwt/v5"
	"github.com/gtkit/gojwt/claims"
)

// TokenParser 抽象 token 验证能力，与 middleware.TokenParser 形状一致
// （internal/pkg 不反向 import middleware，故此处自持接口）。
type TokenParser interface {
	ParseToken(ctx context.Context, tokenString string, opts ...jwtv5.ParserOption) (*claims.Claims, error)
}

// FallbackParser 组合主验证器与可选的 Laravel Laravel 验证器：
// 先走主验证（Ed25519），失败且 Laravel 验证器存在时回退。
// 每个验证器只接受自己钉死的算法，不按 token header 的 alg 预分流（那等于信任攻击者输入）。
type FallbackParser struct {
	primary TokenParser
	laravel *LaravelVerifier
	// acceptUntil 是回退验签的**绝对**截止时刻，零值表示立即停止回退。
	//
	// 零值 fail-closed 而不是"不设限"：Laravel 兼容是过渡措施，没有终点的
	// 过渡就是永久的。装配层要求开启回退时必须给出截止时刻，因此这里的零值
	// 只可能来自编码错误——那种情况下拒绝回退是唯一安全的行为。
	acceptUntil time.Time
	// now 可注入，用于测试截止前后的行为。
	now func() time.Time
}

// NewFallbackParser 创建组合 parser。
//
// laravel 传 nil 表示不启用回退（行为等同裸 primary），此时 acceptUntil 被忽略。
//
// acceptUntil 是**绝对时刻**而不是时长：时长会让每次重启都自动顺延，
// 于是"过渡期"永远不会结束——而那正是这个字段要防的事。
func NewFallbackParser(
	primary TokenParser, laravel *LaravelVerifier, acceptUntil time.Time,
) (*FallbackParser, error) {
	if primary == nil {
		return nil, errors.New("fallback parser: primary token parser is required")
	}
	if laravel != nil && acceptUntil.IsZero() {
		return nil, errors.New(
			"fallback parser: laravel fallback requires an absolute cutoff instant; " +
				"a compatibility window without an end date never ends")
	}

	return &FallbackParser{
		primary: primary, laravel: laravel,
		acceptUntil: acceptUntil, now: time.Now,
	}, nil
}

// fallbackAllowed 报告当前是否仍接受 Laravel 回退验签。
//
// 与 Parser.unkeyedTokenAllowed 同一形态：两者是**不同的兼容维度**
// （缺 kid 的存量令牌 vs 不同算法族的 Laravel 令牌），因此各有各的截止时刻，
// 共用一个会让其中一条的下线被另一条阻塞。
func (p *FallbackParser) fallbackAllowed() bool {
	if p.acceptUntil.IsZero() {
		return false
	}
	return p.now().Before(p.acceptUntil)
}

// ParseToken 先用主验证器解析；主验证失败且 Laravel 回退已启用时再试一次。
// 两路都失败时返回 errors.Join 的联合错误，两类客户端的失败原因都保留在日志链上。
func (p *FallbackParser) ParseToken(
	ctx context.Context, tokenString string, opts ...jwtv5.ParserOption,
) (*claims.Claims, error) {
	parsed, primaryErr := p.primary.ParseToken(ctx, tokenString, opts...)
	if primaryErr == nil {
		return parsed, nil
	}
	if p.laravel == nil {
		return nil, primaryErr
	}
	// 截止之后不再回退：只校验配置格式而不在运行期执行它，等于给运维一个
	// **虚假的**下线承诺——开关不关，存量令牌就永远有效。
	if !p.fallbackAllowed() {
		return nil, primaryErr
	}

	parsed, laravelErr := p.laravel.ParseToken(ctx, tokenString, opts...)
	if laravelErr == nil {
		return parsed, nil
	}

	return nil, errors.Join(primaryErr, laravelErr)
}
