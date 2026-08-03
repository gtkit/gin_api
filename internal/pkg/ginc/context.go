// Package ginc 提供 gin 相关的协议适配 helper（HTTP 层使用）。
// 仅依赖 gin，不承载业务；与 pkg/response、pkg/sse 同属协议适配包，
// 只应被 transport/http、router、middleware 引用。
package ginc

import (
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
