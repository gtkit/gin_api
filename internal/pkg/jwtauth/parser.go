package jwtauth

import (
	"context"
	"errors"

	jwtv5 "github.com/golang-jwt/jwt/v5"
	"github.com/gtkit/gojwt/claims"
)

// TokenParser 抽象 token 验证能力，与 middleware.TokenParser 形状一致
// （internal/pkg 不反向 import middleware，故此处自持接口）。
type TokenParser interface {
	ParseToken(ctx context.Context, tokenString string, opts ...jwtv5.ParserOption) (*claims.Claims, error)
}

// FallbackParser 组合主验证器与可选的 Laravel legacy 验证器：
// 先走主验证（Ed25519），失败且 legacy 存在时回退。
// 每个验证器只接受自己钉死的算法，不按 token header 的 alg 预分流（那等于信任攻击者输入）。
type FallbackParser struct {
	primary TokenParser
	legacy  *LaravelVerifier
}

// NewFallbackParser 创建组合 parser。legacy 传 nil 表示不启用回退（行为等同裸 primary）。
func NewFallbackParser(primary TokenParser, legacy *LaravelVerifier) (*FallbackParser, error) {
	if primary == nil {
		return nil, errors.New("fallback parser: primary token parser is required")
	}

	return &FallbackParser{primary: primary, legacy: legacy}, nil
}

// ParseToken 先用主验证器解析；主验证失败且 legacy 启用时回退 legacy。
// 两路都失败时返回 errors.Join 的联合错误，两类客户端的失败原因都保留在日志链上。
func (p *FallbackParser) ParseToken(
	ctx context.Context, tokenString string, opts ...jwtv5.ParserOption,
) (*claims.Claims, error) {
	parsed, primaryErr := p.primary.ParseToken(ctx, tokenString, opts...)
	if primaryErr == nil {
		return parsed, nil
	}
	if p.legacy == nil {
		return nil, primaryErr
	}

	parsed, legacyErr := p.legacy.ParseToken(ctx, tokenString, opts...)
	if legacyErr == nil {
		return parsed, nil
	}

	return nil, errors.Join(primaryErr, legacyErr)
}
