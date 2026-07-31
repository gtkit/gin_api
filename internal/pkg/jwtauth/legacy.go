package jwtauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	jwtv5 "github.com/golang-jwt/jwt/v5"
	"github.com/gtkit/gojwt/claims"
)

// legacySigningMethods 将配置算法名映射为 golang-jwt 的签名方法。
// 只允许 HMAC 家族：legacy 验证器与 Laravel（tymon/jwt-auth）共享对称 secret。
var legacySigningMethods = map[string]jwtv5.SigningMethod{
	"hs256": jwtv5.SigningMethodHS256,
	"hs384": jwtv5.SigningMethodHS384,
	"hs512": jwtv5.SigningMethodHS512,
}

// LaravelVerifier 验证旧 Laravel（tymon/jwt-auth）签发的存量 token：只验不签。
// 过渡期兼容层——Laravel 下线且存量 token 全部过期后，整包删除本文件与 FallbackParser。
//
// 安全约束：验证器只接受构造时钉死的单一算法（不信任 token header 的 alg 声明），
// 且要求 token 必须携带 exp（无过期时间的 token 等于无限信任窗口，一律拒绝）。
type LaravelVerifier struct {
	secret []byte
	method jwtv5.SigningMethod
	role   string
}

// NewLaravelVerifier 创建 Laravel 存量 token 验证器。
// algorithm 取值 hs256|hs384|hs512（大小写不敏感，留空按 hs256）；
// role 是验证通过后合成进 claims 的客户端角色，由装配方传入（如 sharedauth.ClientRole）。
func NewLaravelVerifier(secret, algorithm, role string) (*LaravelVerifier, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("legacy laravel verifier: secret is required")
	}
	if strings.TrimSpace(role) == "" {
		return nil, errors.New("legacy laravel verifier: role is required")
	}

	alg := strings.ToLower(strings.TrimSpace(algorithm))
	if alg == "" {
		alg = "hs256"
	}
	method, ok := legacySigningMethods[alg]
	if !ok {
		return nil, fmt.Errorf("legacy laravel verifier: algorithm %q is not supported (hs256|hs384|hs512)", algorithm)
	}

	return &LaravelVerifier{
		secret: []byte(secret),
		method: method,
		role:   role,
	}, nil
}

// ParseToken 按钉死的 HMAC 算法验签并校验 exp/nbf，成功后把 tymon 载荷适配为 gojwt claims：
// UserID 取自 sub（兼容 JSON 数字与字符串编码），Roles 合成为构造时注入的角色。
// 签名与 gojwt Tokener 的 ParseToken 对齐，便于与 Ed25519 主验证器组合。
func (v *LaravelVerifier) ParseToken(tokenString string, opts ...jwtv5.ParserOption) (*claims.Claims, error) {
	parserOpts := append([]jwtv5.ParserOption{
		jwtv5.WithValidMethods([]string{v.method.Alg()}),
		jwtv5.WithExpirationRequired(),
	}, opts...)
	parser := jwtv5.NewParser(parserOpts...)

	mapClaims := jwtv5.MapClaims{}
	if _, err := parser.ParseWithClaims(tokenString, mapClaims, v.keyFunc); err != nil {
		return nil, fmt.Errorf("parse legacy laravel token: %w", err)
	}

	userID, err := legacyUserID(mapClaims)
	if err != nil {
		return nil, err
	}

	return v.adaptClaims(mapClaims, userID), nil
}

func (v *LaravelVerifier) keyFunc(*jwtv5.Token) (any, error) {
	return v.secret, nil
}

// adaptClaims 把验签通过的 tymon 载荷适配为 gojwt claims.Claims，
// 使现有 Auth 中间件的 VerifyRole / ValidateUser 流程无需感知 token 来源。
func (v *LaravelVerifier) adaptClaims(mapClaims jwtv5.MapClaims, userID int64) *claims.Claims {
	adapted := &claims.Claims{
		UserID: userID,
		Roles:  []string{v.role},
	}
	adapted.Subject = strconv.FormatInt(userID, 10)
	if exp, err := mapClaims.GetExpirationTime(); err == nil && exp != nil {
		adapted.ExpiresAt = exp
	}
	if iat, err := mapClaims.GetIssuedAt(); err == nil && iat != nil {
		adapted.IssuedAt = iat
	}

	return adapted
}

// legacyUserID 从 tymon 的 sub claim 提取用户主键。
// tymon 依 PHP 侧序列化差异可能把 sub 编码为 JSON 数字或字符串，两种都接受；
// 缺失、非整数、非正数一律报错，绝不放行 UserID==0 的 claims。
func legacyUserID(mapClaims jwtv5.MapClaims) (int64, error) {
	raw, ok := mapClaims["sub"]
	if !ok {
		return 0, errors.New("legacy laravel token: sub claim is missing")
	}

	var userID int64
	switch sub := raw.(type) {
	case float64:
		if sub != math.Trunc(sub) {
			return 0, fmt.Errorf("legacy laravel token: sub %v is not an integer", sub)
		}
		userID = int64(sub)
	case json.Number:
		parsed, err := sub.Int64()
		if err != nil {
			return 0, fmt.Errorf("legacy laravel token: sub %q is not an integer: %w", sub.String(), err)
		}
		userID = parsed
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(sub), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("legacy laravel token: sub %q is not an integer: %w", sub, err)
		}
		userID = parsed
	default:
		return 0, fmt.Errorf("legacy laravel token: sub has unsupported type %T", raw)
	}

	if userID <= 0 {
		return 0, fmt.Errorf("legacy laravel token: sub %d must be positive", userID)
	}

	return userID, nil
}
