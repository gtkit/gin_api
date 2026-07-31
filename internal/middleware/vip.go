package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/gtkit/goerr"

	"github.com/gtkit/gin-api/internal/pkg/ginc"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
)

// RequireVIP 要求请求对应的当前用户具备有效会员资格。
// 使用约定：
// 1. 该中间件应放在 Auth 之后执行；
// 2. Auth 负责把 userID 写入 gin context；
// 3. RequireVIP 只负责判断会员状态，不重复解析 token。
func RequireVIP(verifier sharedauth.VIPVerifier) gin.HandlerFunc {
	if verifier == nil {
		panic("vip verifier must be provided")
	}

	return func(c *gin.Context) {
		userID := ginc.UserIDFromContext(c)
		if userID == 0 {
			resp.Error(c, goerr.New(nil, goerr.StatusAuth(), "user not authenticated"))
			c.Abort()
			return
		}

		if !verifier.ValidateVIP(c.Request.Context(), userID) {
			resp.Error(c, goerr.New(nil, goerr.StatusVip(), "vip membership is required"))
			c.Abort()
			return
		}

		c.Next()
	}
}
