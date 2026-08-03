package ratelimit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/gtkit/gin-api/internal/router/routespec"
)

// minSecretLength 是 key HMAC 密钥的最小字节数。
const minSecretLength = 32

// 404/405 的路由模式为空，用固定常量代替。
//
// 不用空串：空串会让所有未命中路径共用同一个桶——那恰好是扫描器最常打的
// 那一类请求，一个桶意味着一个正常用户的偶发 404 会和扫描流量抢配额。
// 也不能用原始路径：那是客户端完全可控的高基数输入，会让键空间无限膨胀。
const (
	notFoundRouteKey         = "__not_found__"
	methodNotAllowedRouteKey = "__method_not_allowed__"
)

// KeyBuilder 生成限流键。
//
// 主体标识（IP / userID）经 **HMAC** 而不是普通哈希：普通 SHA 的输入空间
// 可枚举——IPv4 只有 2^32 种，攻击者拿到 Redis 读权限后可以直接反推出
// 每个被限流的 IP。HMAC 带服务端密钥，没有密钥就无法建立这张对照表。
type KeyBuilder struct {
	secret []byte
	prefix string
}

// NewKeyBuilder 校验密钥并构造生成器。
//
// 密钥必须是**专用**的，禁止复用 JWT、数据库或任何其他业务密钥：
// 复用意味着任何一处泄漏都会同时击穿多个用途，而限流键的泄漏
// 与令牌签发密钥的泄漏在响应流程上完全不同。
func NewKeyBuilder(secret string, prefix string) (*KeyBuilder, error) {
	trimmed := strings.TrimSpace(secret)
	if len(trimmed) < minSecretLength {
		return nil, fmt.Errorf(
			"rate limit key secret must be at least %d bytes, got %d: a short secret makes the "+
				"HMAC brute-forceable and the key space enumerable again",
			minSecretLength, len(trimmed))
	}
	if prefix == "" {
		return nil, errors.New("rate limit key builder requires a non-empty prefix")
	}
	return &KeyBuilder{secret: []byte(trimmed), prefix: prefix}, nil
}

// Subject 是限流的主体标识（IP、userID，或全局作用域下为空）。
type Subject struct {
	Scope routespec.RateLimitScope
	Value string
}

// Build 组装限流键。
//
// 键的组成是 `前缀:策略ID:方法:路由模式:scope:HMAC(主体)`。
//
// 四段缺一不可：
//   - **策略 ID**：同一路由可能挂不同限额的策略，缺了它们会共用一个桶；
//   - **方法**：同一路径的 GET 与 POST 是两种代价完全不同的操作；
//   - **路由模式**（不是原始路径）：`/users/:id` 而不是 `/users/8812`——
//     用原始路径会让每个 ID 各占一个桶，限流形同虚设且键空间无界；
//   - **scope + 主体**：真正要限的对象。
func (b *KeyBuilder) Build(
	policy routespec.RateLimitPolicy, method, routePattern string, subject Subject,
) string {
	pattern := routePattern
	if pattern == "" {
		pattern = notFoundRouteKey
	}

	var sb strings.Builder
	sb.WriteString(b.prefix)
	sb.WriteString(":rl:")
	sb.WriteString(policy.ID)
	sb.WriteByte(':')
	sb.WriteString(method)
	sb.WriteByte(':')
	sb.WriteString(pattern)
	sb.WriteByte(':')
	sb.WriteString(subject.Scope.String())

	if subject.Scope != routespec.ScopeGlobal {
		sb.WriteByte(':')
		sb.WriteString(b.hash(subject.Value))
	}

	return sb.String()
}

// hash 用 HMAC-SHA256 派生主体标识，取前 16 字节 hex。
//
// 截断到 128 bit：碰撞概率在任何真实主体规模下都可忽略，而完整的 64 字符
// hex 会让每个 Redis 键平白多出 32 字节——在百万级键规模下不是小数目。
func (b *KeyBuilder) hash(value string) string {
	mac := hmac.New(sha256.New, b.secret)
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

// FallbackRouteKey 返回 404/405 的固定路由标识。
func FallbackRouteKey(methodNotAllowed bool) string {
	if methodNotAllowed {
		return methodNotAllowedRouteKey
	}
	return notFoundRouteKey
}
