package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/gtkit/logger/v2"

	authapp "github.com/gtkit/gin-api/internal/module/auth/application"
	"github.com/gtkit/gin-api/internal/runtime/module/account"
	authruntime "github.com/gtkit/gin-api/internal/runtime/module/auth"
)

// services 保存启动时装配好的业务模块产物，供中间件、路由和 worker 装配阶段复用。
// 每个字段代表需要在这些阶段之间传递的服务或共享能力，不是模块目录的登记清单。
//
// 新增业务模块时，需要完成三处对应接线：
//  1. 在本结构体中增加具名字段。只有一个服务时保存 *xxxapp.Service；
//     像 auth 一样还需共享撤销检查等能力时，保存 xxxruntime.Assembly。
//  2. 在 buildServices 中调用 internal/runtime/module/<模块> 的装配入口，
//     传入所需配置和已有基础设施，检查构造错误，再把产物赋给 svc 的对应字段。
//     仓储、客户端与 application service 的具体组装留在该模块的 runtime 包。
//  3. 在 buildEngine 的 RegisterModules 闭包中，将该字段传给模块的
//     transport/http.Register；有后台任务时，再按其真实依赖接入 worker 装配。
//
// 仅增加字段不会自动构造服务，也不会自动注册路由。业务 application 不依赖
// 本结构体；跨模块所需能力通过各自声明的窄接口注入。
type services struct {
	// auth 是本地认证模块的装配产物；auth.mode 为 external 时是零值。
	//
	// 零值可用是有意的：外部身份模式下这些端点不存在，而"不存在"应当表达为
	// 一个不被注册的模块，不是一个注册了却永远报错的模块。
	auth authruntime.Assembly
	// accounts 独立于 auth 业务模块，提供账号状态、会员及权威角色校验。
	accounts account.Assembly
	// optionalServices 承载 optional 构建的业务模块产物（默认构建为空载体）。
	optionalServices //nolint:unused // embedded only by optional builds
}

// buildServices 用 Runtime 提供的依赖构造业务模块。
func buildServices(ctx context.Context, inf infra, runtime *Runtime) (services, error) {
	var svc services

	// 本地认证按模式条件装配。
	//
	// 外部身份模式下整个模块不存在——那种部署里令牌由外部签发，
	// 这些端点没有任何意义，留着只是多一组无人看管的凭据入口。
	if inf.cfg.Auth.LocalEnabled() {
		// 账号维度限流按策略存在性注入（预置能力门控：策略缺席即维度关闭）。
		// 缺席时打一条 Warn——本地认证启用而该维度关闭是有意决策才对，
		// 不该是漏配而无人知晓。
		var accounts authapp.AccountLimiter
		if inf.rateLimits.registry != nil {
			if limiter, ok := inf.rateLimits.registry.Lookup(authruntime.AccountRateLimitPolicyID); ok {
				accounts = authruntime.NewAccountLimiter(limiter, inf.rateLimits.keys, inf.recorder)
			}
		}
		if accounts == nil {
			logger.Warnf("account-dimension credential rate limiting is off: define ratelimit "+
				"policy %q to guard against distributed password spraying", authruntime.AccountRateLimitPolicyID)
		}

		assembly, authErr := authruntime.New(ctx, authruntime.Deps{
			DB:     inf.mysqlDB,
			Redis:  inf.resources.Rdb(0),
			Issuer: runtime.TokenIssuer(),
			Config: inf.cfg.Auth,
			// 访问令牌寿命的唯一来源是 JWT 配置：签发它的是 JWT 装配。
			AccessTTL: inf.cfg.JWT.EffectiveTokenTTL(inf.cfg.Auth.Mode),
			// 撤销记录必须活过 exp + 验签 leeway：同一个偏移的单一来源是配置。
			ClockSkew: time.Duration(inf.cfg.JWT.ClockSkewSeconds) * time.Second,
			KeyPrefix: inf.cfg.EffectiveRedisPrefix(),
			Metrics:   inf.recorder,
			Accounts:  accounts,
		})
		if authErr != nil {
			return services{}, fmt.Errorf("build auth module: %w", authErr)
		}
		svc.auth = assembly
	}

	accounts, accountErr := account.New(inf.mysqlDB)
	if accountErr != nil {
		return services{}, fmt.Errorf("build account sources: %w", accountErr)
	}
	svc.accounts = accounts

	if err := buildOptionalServices(&svc, inf); err != nil {
		return services{}, err
	}

	return svc, nil
}
