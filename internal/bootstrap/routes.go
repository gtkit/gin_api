package bootstrap

import (
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/middleware"
	authhttp "github.com/gtkit/gin-api/internal/module/auth/transport/http"
	"github.com/gtkit/gin-api/internal/router"
)

// buildEngine 通过统一路由入口挂载各模块，并初始化 Gin engine。
// 新增模块在 RegisterModules 中追加 Register 调用。
func buildEngine(inf infra, svc services, auth gin.HandlerFunc, status router.ProcessStatus) (*gin.Engine, error) {
	cfg := inf.cfg
	httpConfig := newHTTPConfig(cfg)

	opt := router.Options{
		AccessLogger:            inf.accessLogger,
		AllowedOrigins:          cfg.CORS.AllowedOrigins,
		LogRequestBody:          cfg.Log.AccessRequestBody,
		LogResponseBody:         cfg.Log.AccessResponseBody,
		RecoveryStack:           cfg.Log.RecoveryStack,
		MaxBodyBytes:            cfg.App.MaxBodyBytes,
		ServerReadTimeout:       httpConfig.ReadTimeout,
		ServerReadHeaderTimeout: httpConfig.ReadHeaderTimeout,
		MaxInflightRequests:     cfg.App.EffectiveMaxInflightRequests(),
		TracingEnabled:          cfg.Telemetry.Enabled && cfg.Telemetry.Traces,
		ServiceName:             cfg.App.Name,
		// 默认空 = 不信任任何代理。必须显式传递：Gin 不设置时的默认是信任所有代理。
		TrustedProxies:  cfg.Proxy.TrustedProxies,
		RemoteIPHeaders: cfg.Proxy.RemoteIPHeaders,
		Metrics:         inf.recorder,
		HealthChecker:   newHealthChecker(inf.resources, inf.connections),
		// readiness 用的 Redis 必需性与资源装配同源，避免两处口径漂移。
		RedisRequired: RedisRequired(cfg),
		RateLimits:    inf.rateLimits.registry,
		RateLimitKeys: inf.rateLimits.keys,
		ProcessStatus: status,
	}
	// 限流引用的解析结果在全部路由注册完成后统一判定（fail-closed）：
	// 引用了未注册策略、或在不保证身份的路由上引用身份作用域策略，
	// 若被静默跳过/丢弃，配置漏项、拼写错误或作用域误配都会让保护
	// 无提示失效——rate-limiting spec 要求这类引用在装配期失败。
	businessRateLimits, preIdentityRateLimits, unresolvedRateLimits := router.NewRateLimitStager(opt)

	// 各模块在 /api/v1 组上用 gin 的方式注册自己的路由。
	// 新增模块只需在这里加一行 Register。
	opt.RegisterModules = func(v1 *gin.RouterGroup) {
		// 认证模块只在本地认证模式下注册。外部身份模式下这些端点不存在，
		// 而不是注册一组永远报错的路由。
		if svc.auth.Service != nil {
			// Keep credential traffic below the shared HTTP budget, while also
			// respecting the configured DB pool. A total budget of one cannot reserve
			// an additional token-route slot.
			credentialLimit := min(cfg.Database.EffectiveMaxOpenConn(), max(1, cfg.App.EffectiveMaxInflightRequests()-1))
			authhttp.Register(v1, authhttp.Deps{
				Service:             svc.auth.Service,
				Auth:                auth,
				CredentialAdmission: middleware.Admission(credentialLimit, inf.recorder),
				// 认证端点不保证身份（请求发生在拿到身份之前），
				// 注入 pre-identity 取用函数：身份作用域引用即装配错误。
				RateLimit: preIdentityRateLimits,
			})
		}
	}
	opt.RegisterFileModule = func(v1, transfers *gin.RouterGroup) {
		registerOptionalModules(v1, transfers, svc, auth, inf, businessRateLimits)
	}

	engine, err := router.New(cfg.Env, opt)
	if err != nil {
		return nil, err
	}
	// 判定必须在 router.New 之后：引用记录发生在 RegisterModules 执行期间。
	if err := unresolvedRateLimits(); err != nil {
		return nil, fmt.Errorf("resolve rate limit policy references: %w", err)
	}
	return engine, nil
}
