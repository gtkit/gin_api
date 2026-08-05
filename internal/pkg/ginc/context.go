// Package ginc 提供 gin 相关的协议适配 helper（HTTP 层使用）。
// 仅依赖 gin，不承载业务；与 pkg/response、pkg/sse 同属协议适配包，
// 只应被 transport/http、router、middleware 引用。
package ginc

import (
	"time"

	"github.com/gin-gonic/gin"

	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
)

// userIDKey 是用户 ID 在 gin context 中的键。
// gin v1.12 的 Keys 为 map[any]any，故用私有空结构体类型而非字符串，
// 从类型层面彻底规避跨包键碰撞（零内存、无法被外部伪造）。
type userIDKey struct{}

// SetUserID 将用户 ID 写入 gin context（由鉴权中间件调用）。
func SetUserID(c *gin.Context, userID int64) {
	c.Set(userIDKey{}, userID)
}

// UserIDFromContext 从 gin context 读取用户 ID；未设置时返回 0。
func UserIDFromContext(c *gin.Context) int64 {
	return c.GetInt64(userIDKey{})
}

// principalKey 是已认证主体在 Gin context 里的键。
const principalKey = "auth.principal"

// SetPrincipal 记录本次请求已认证的主体。
func SetPrincipal(c *gin.Context, p sharedauth.Principal) {
	c.Set(principalKey, p)
}

// PrincipalFromContext 取出已认证主体。
//
// ok=false 表示本次请求尚未通过认证——调用方**绝不能**把它解释成"允许"。
func PrincipalFromContext(c *gin.Context) (sharedauth.Principal, bool) {
	if c == nil {
		return sharedauth.Principal{}, false
	}
	v, exists := c.Get(principalKey)
	if !exists {
		return sharedauth.Principal{}, false
	}
	p, ok := v.(sharedauth.Principal)
	return p, ok
}

// tokenIdentityKey 是本次请求所用令牌的标识在 gin context 中的键。
type tokenIdentityKey struct{}

// TokenIdentity 是本次请求所用访问令牌的可撤销标识。
//
// **与 Principal 分开**是刻意的：Principal 回答"你是谁"，供授权判定消费；
// 这里回答"你这次用的是哪个令牌、它什么时候过期"，只有登出与撤销需要。
// 混进 Principal 会让授权实现能看到令牌 ID——而授权不该关心凭据的身份，
// 那是两个会各自演进的关注点。
type TokenIdentity struct {
	// TokenID 是令牌的 jti，撤销以它为准。
	TokenID string
	// ExpiresAt 是令牌的过期时刻，决定撤销记录该活多久。
	ExpiresAt time.Time
}

// SetTokenIdentity 记录本次请求所用的令牌标识（由鉴权中间件调用）。
func SetTokenIdentity(c *gin.Context, id TokenIdentity) {
	c.Set(tokenIdentityKey{}, id)
}

// TokenIdentityFromContext 取出本次请求的令牌标识。
//
// ok=false 表示本次请求没有经过认证，或令牌不含可撤销的标识。
// 调用方**不能**把它当成"无需撤销"——登出在这种情况下应当报错，
// 否则用户会以为自己已经登出。
func TokenIdentityFromContext(c *gin.Context) (TokenIdentity, bool) {
	if c == nil {
		return TokenIdentity{}, false
	}
	v, exists := c.Get(tokenIdentityKey{})
	if !exists {
		return TokenIdentity{}, false
	}
	id, ok := v.(TokenIdentity)
	return id, ok
}
