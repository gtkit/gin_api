package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/gtkit/goerr"
	jwtclaims "github.com/gtkit/gojwt/claims"

	"github.com/gtkit/gin-api/internal/pkg/ginc"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
)

type TokenParser interface {
	ParseToken(string, ...gojwt.ParserOption) (*jwtclaims.Claims, error)
}

// Auth authenticates requests with an injected JWT parser and user verifier.
func Auth(verifier sharedauth.UserVerifier, parser TokenParser) gin.HandlerFunc {
	return newAuthMiddleware(verifier, parser, false)
}

// OptionalAuth authenticates requests only when the client sends a token.
func OptionalAuth(verifier sharedauth.UserVerifier, parser TokenParser) gin.HandlerFunc {
	return newAuthMiddleware(verifier, parser, true)
}

func newAuthMiddleware(verifier sharedauth.UserVerifier, parser TokenParser, allowMissingToken bool) gin.HandlerFunc {
	if verifier == nil {
		panic("verifier must be provided")
	}
	if parser == nil {
		panic("token parser must be provided")
	}

	return func(c *gin.Context) {
		token, err := tokenFromHeader(c)
		if err != nil {
			if allowMissingToken {
				c.Next()
				return
			}

			resp.Error(c, goerr.New(err, goerr.StatusAuthHeader()))
			c.Abort()

			return
		}

		claims, err := parser.ParseToken(token)
		if err != nil {
			resp.Error(c, goerr.New(err, goerr.StatusAuth(), "token parse failed"))
			c.Abort()

			return
		}

		if err := claims.VerifyRole(sharedauth.ClientRole); err != nil {
			resp.Error(c, goerr.New(err, goerr.StatusAuth(), "role is invalid"))
			c.Abort()

			return
		}

		ok, err := verifier.ValidateUser(c.Request.Context(), claims.UserID)
		if err != nil {
			// 基础设施故障（如 DB 不可用）不能伪装成鉴权失败：返回 503，
			// 让客户端/网关按"服务暂时不可用"重试，而不是误判为"用户无效"(401)。
			resp.Error(c, goerr.New(err, goerr.StatusDependencyUnavailable(), "user verification unavailable"))
			c.Abort()

			return
		}
		if !ok {
			resp.Error(c, goerr.New(nil, goerr.StatusAuth(), "user is unavailable"))
			c.Abort()

			return
		}

		ginc.SetUserID(c, claims.UserID)
		c.Next()
	}
}

func tokenFromHeader(c *gin.Context) (string, error) {
	token := c.Request.Header.Get("App-Token")
	if token == "" {
		// 仅 WebSocket 升级请求允许从 query 读 token：浏览器 WebSocket API 无法自定义请求头。
		// SSE 若也需要 query token，须绑定到明确路由白名单再放行（浏览器 EventSource 同样
		// 无法自定义请求头，但 Accept: text/event-stream 可被普通请求伪造，不能单独作为依据）。
		if isWebSocketUpgrade(c.Request) {
			token = c.Request.URL.Query().Get("App-Token")
		}
		if token == "" {
			return "", errors.New("App-Token header is empty")
		}
	}

	return token, nil
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}
