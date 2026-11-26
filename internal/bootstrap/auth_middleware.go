package bootstrap

import (
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/middleware"
	"github.com/gtkit/gin-api/internal/pkg/jwtauth"
	"github.com/gtkit/gin-api/internal/platform/config"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
)

// buildMiddlewares 构建路由所需的鉴权中间件。
// Laravel 存量 token 兼容开启时，token parser 注入回退组合校验（只验不签，过渡期兼容）。
func buildMiddlewares(svc services, runtime *Runtime) (gin.HandlerFunc, error) {
	tokenParser, err := newTokenParser(runtime.Config(), runtime.TokenParser())
	if err != nil {
		return nil, err
	}

	// 撤销检查只在本地认证模式下存在：外部身份模式的令牌由外部签发，
	// 撤销也归外部，本服务没有撤销表可查。
	//
	// 传 nil 接口值要小心——直接传 svc.auth.Revocation（一个可能为 nil 的
	// 具体指针）会让接口非 nil 而内部指针为 nil，中间件的 != nil 判断因此
	// 通过，然后在第一个请求上空指针解引用。这里显式判空后再赋值。
	var revocation middleware.RevocationChecker
	if svc.auth.Revocation != nil {
		revocation = svc.auth.Revocation
	}

	var sessions sharedauth.SessionVerifier
	if runtime.Config().Auth.SessionLifecycle {
		sessions = svc.accounts.Sessions
		if sessions == nil {
			return nil, fmt.Errorf("auth: session verification source is required")
		}
	}
	return middleware.Auth(middleware.AuthOptions{
		Sessions:   sessions,
		Verifier:   svc.accounts.Users,
		Parser:     tokenParser,
		Revocation: revocation,
	}), nil
}

// newTokenParser 为 Ed25519 parser 按需附加有截止时间的 Laravel 回退验签。
func newTokenParser(cfg *config.Config, issuer jwtauth.TokenParser) (middleware.TokenParser, error) {
	laravelCfg := cfg.JWT.Laravel
	if !laravelCfg.Enabled {
		return issuer, nil
	}

	laravelVerifier, err := jwtauth.NewLaravelVerifier(laravelCfg.Secret, laravelCfg.Algorithm, sharedauth.ClientRole,
		time.Duration(cfg.JWT.ClockSkewSeconds)*time.Second)
	if err != nil {
		return nil, fmt.Errorf("init laravel verifier: %w", err)
	}

	// 配置层已校验格式，这里把截止时间交给运行期 parser 执行兼容窗口约束。
	acceptUntil, err := time.Parse(time.RFC3339, strings.TrimSpace(cfg.JWT.LaravelAcceptUntil))
	if err != nil {
		return nil, fmt.Errorf(
			"jwt.laravel.enabled requires jwt.laravel_accept_until as an RFC3339 instant: "+
				"a compatibility window without an end date never ends: %w", err)
	}

	return jwtauth.NewFallbackParser(issuer, laravelVerifier, acceptUntil)
}
