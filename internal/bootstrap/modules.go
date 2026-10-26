// 模块装配集中入口：业务服务、鉴权、路由及后台任务。
// providers.go 决定装配阶段顺序；本文件维护模块依赖与脚手架接线标记。
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/logger/v2"

	"github.com/gtkit/gin-api/internal/middleware"
	authapp "github.com/gtkit/gin-api/internal/module/auth/application"
	authhttp "github.com/gtkit/gin-api/internal/module/auth/transport/http"
	"github.com/gtkit/gin-api/internal/pkg/jwtauth"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/router"
	"github.com/gtkit/gin-api/internal/runtime/module/account"
	authruntime "github.com/gtkit/gin-api/internal/runtime/module/auth"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
)

// UPLOAD: 启用上传/下载路由示例时取消下一行 import 声明的注释。
// wiring:begin modules.upload.import upload line
// import "github.com/gtkit/gin-api/internal/optional/upload"
// wiring:end modules.upload.import

// AUDITLOG: 恢复审计路由示例时取消下一行 import 的注释。
// wiring:begin modules.auditlog.import auditlog line
// import "github.com/gtkit/gin-api/internal/optional/auditlog"
// wiring:end modules.auditlog.import

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
	// UPLOAD: 在 buildServices 中用 inf.uploader 构造业务处理器并赋值。
	// wiring:begin modules.upload.fields upload line
	// uploadFile   gin.HandlerFunc
	// downloadFile gin.HandlerFunc
	// wiring:end modules.upload.fields
}

// middlewares 聚合路由装配所需的 Gin 中间件与授权取用函数。
type middlewares struct {
	auth         gin.HandlerFunc
	optionalAuth gin.HandlerFunc
	// authorizeByID 按授权者标识取中间件，供引用了具名授权的模块路由使用。
	//
	// 装配期就绑定注册表：让模块路由拿一个只需传标识的函数，模块因此不必
	// 认识注册表，也不可能拿到一个未绑定注册表的授权中间件。
	authorizeByID func(string) gin.HandlerFunc
}

// buildServices 借用基础设施装配模块，按 module-services 标记接入生成代码。
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

	return svc, nil
}

// buildMiddlewares 构建路由所需的鉴权中间件。
// Laravel 存量 token 兼容开启时，token parser 注入回退组合校验（只验不签，过渡期兼容）。
func buildMiddlewares(svc services, runtime *Runtime) (middlewares, error) {
	tokenParser, err := newTokenParser(runtime.Config(), runtime.TokenParser())
	if err != nil {
		return middlewares{}, err
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

	authOpts := middleware.AuthOptions{
		Verifier:   svc.accounts.Users,
		Parser:     tokenParser,
		Revocation: revocation,
	}
	auth := middleware.Auth(authOpts)
	optionalAuth := middleware.OptionalAuth(authOpts)

	authorizers, err := buildAuthorizers(svc)
	if err != nil {
		return middlewares{}, err
	}

	return middlewares{
		auth:          auth,
		optionalAuth:  optionalAuth,
		authorizeByID: newAuthorizeByID(authorizers),
	}, nil
}

// buildAuthorizers 登记进程内全部具名授权实现。
//
// 这里是唯一的注册点：模块路由经 authorizeByID 取用，
// 未登记的 ID 在装配时记录错误并绑定恒拒绝中间件。
func buildAuthorizers(svc services) (*sharedauth.AuthorizerRegistry, error) {
	reg := sharedauth.NewAuthorizerRegistry()

	// role.admin 查 user_roles 表这一权威角色源。**这是示例实现，可按需删除**
	// （删除清单见 SCAFFOLD.md）。
	//
	// 它刻意不采信 JWT 的 role claim：令牌寿命可达数十天，其中的角色是签发那一刻
	// 的事实。仅凭它放行 admin 等于把"一次签发"当成"持续有效的高权限"——
	// 一个被撤销权限的账号在令牌过期前仍然畅通无阻。
	//
	// 未接入角色数据时（表空或未建），判定一律拒绝，因此**替换恒拒绝占位没有放宽默认行为**。
	if err := reg.Register("role.admin",
		newRoleAuthorizer(svc.accounts.Roles, adminRoleName)); err != nil {
		return nil, err
	}

	// entitlement.vip 是共享的会员资格判定，供多个模块路由按 ID 引用，
	// 不复制进任何模板——复制会让"会员判定"出现多份可能不一致的实现。
	if err := reg.Register("entitlement.vip", newVIPAuthorizer(svc.accounts.VIP)); err != nil {
		return nil, err
	}

	// 把恒拒绝的占位在启动时说清楚：否则"某个接口一直 403"会变成一桩悬案，
	// 而最坏的排查结果是有人把它改成凭 JWT role claim 放行。
	for _, id := range reg.IDs() {
		if a, ok := reg.Lookup(id); ok {
			if deny, isDeny := a.(sharedauth.DenyAllAuthorizer); isDeny {
				logger.Warnf("authorizer %q denies every request: %s", id, deny.Reason)
			}
		}
	}

	return reg, nil
}

// newAuthorizeByID 返回按授权者标识取中间件的函数，供业务模块路由使用。
//
// 未登记的标识返回**恒拒绝**中间件并记一条错误日志，既不 panic 也不放行：
// middleware.Authorize 对 nil authorizer 是 panic，而那会把一个装配问题变成
// 运行期崩溃；返回 nil 更糟——nil handler 挂进链同样在首个请求上崩。
// 恒拒绝是唯一安全的兜底，配合日志使"某接口一直 403"不至于变成悬案。
func newAuthorizeByID(reg *sharedauth.AuthorizerRegistry) func(string) gin.HandlerFunc {
	return func(id string) gin.HandlerFunc {
		authorizer, ok := reg.Lookup(id)
		if !ok {
			logger.Errorf("authorizer %q is not registered; denying every request to the routes "+
				"that reference it (register it in buildAuthorizers)", id)
			authorizer = sharedauth.DenyAll(fmt.Sprintf("authorizer %q is not registered", id))
		}
		return middleware.Authorize(authorizer, id)
	}
}

// buildEngine 通过统一路由入口挂载各模块，并初始化 Gin engine。
// 新增模块在 RegisterModules 中追加 Register 调用。
func buildEngine(inf infra, svc services, mw middlewares, status router.ProcessStatus) (*gin.Engine, error) {
	cfg := inf.cfg

	opt := router.Options{
		AccessLogger:    inf.accessLogger,
		AllowedOrigins:  cfg.CORS.AllowedOrigins,
		LogRequestBody:  cfg.Log.AccessRequestBody,
		LogResponseBody: cfg.Log.AccessResponseBody,
		MaxBodyBytes:    cfg.App.MaxBodyBytes,
		TracingEnabled:  cfg.Telemetry.Enabled && cfg.Telemetry.Traces,
		ServiceName:     cfg.App.Name,
		// 默认空 = 不信任任何代理。必须显式传递：Gin 不设置时的默认是信任所有代理。
		TrustedProxies:  cfg.Proxy.TrustedProxies,
		RemoteIPHeaders: cfg.Proxy.RemoteIPHeaders,
		Metrics:         inf.recorder,
		HealthChecker:   newHealthChecker(inf.resources),
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
	_, preIdentityRateLimits, unresolvedRateLimits := router.NewRateLimitStager(opt)

	// 各模块在 /api/v1 组上用 gin 的方式注册自己的路由。
	// 新增模块只需在这里加一行 Register。
	opt.RegisterModules = func(v1 *gin.RouterGroup) {
		// AUDITLOG: 在 audited 上注册需要审计的业务写路由；此处不自动审计全部接口。
		// wiring:begin modules.auditlog.routes auditlog line
		// audited := v1.Group("", mw.auth, auditlog.Middleware(inf.auditRecorder.Enqueue))
		// _ = audited // 用实际业务路由注册替换这一行。
		// wiring:end modules.auditlog.routes
		// UPLOAD: 恢复 services 字段和 import 后，按业务路由约定取消下面注释。
		// 处理器负责文件记录、权限与响应；未提供处理器时不注册路由。
		// wiring:begin modules.upload.routes upload line
		// if svc.uploadFile != nil {
		// 	v1.POST("/files", middleware.MultipartBody(0), mw.auth, svc.uploadFile)
		// }
		// if svc.downloadFile != nil {
		// 	v1.GET("/files/:id", mw.auth, upload.FileHeaders(upload.DefaultPolicy()), svc.downloadFile)
		// }
		// wiring:end modules.upload.routes

		// 认证模块只在本地认证模式下注册。外部身份模式下这些端点不存在，
		// 而不是注册一组永远报错的路由。
		if svc.auth.Service != nil {
			authhttp.Register(v1, authhttp.Deps{
				Service: svc.auth.Service,
				Auth:    mw.auth,
				// 认证端点不保证身份（请求发生在拿到身份之前），
				// 注入 pre-identity 取用函数：身份作用域引用即装配错误。
				RateLimit: preIdentityRateLimits,
			})
		}
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

// buildWorkers 组装进程级后台任务。
//
// 可选队列的接线示例见 taskqueue.go 和 internal/optional/taskqueue/README.md。
// 其余后台任务在本函数中追加 Worker 构造；
// 需要 application service 或装配期 ctx 时，在调用点把它们加进形参即可——
// 调用点只有一处，签名跟着真实消费者走，不为尚不存在的 worker 预留。
//
// 集合为空不意味着 worker 机制多余——WorkerManager 配合 app.go 的分相关停
// （停 producer → 等 LB 传播 → 关 HTTP → drain consumer）保证两类语义各自成立：
// producer 不会在关停期继续生产任务，consumer 仍能消费完在飞请求写入的任务。
//
// 返回 error 是因为 worker 的声明（类别/生命周期/组合/重名）在注册期即校验：
// 非法声明是编码错误，启动时零成本可判，留到运行期只能靠观察行为反推。
func buildWorkers(inf *infra) (_ *WorkerManager, err error) {
	// 本函数对自身的进程级副作用（下方注册的指标回调）承担失败回滚责任：
	// 新增任务构造或指标注册时，中途返回也必须回收此前注册的回调。
	cleanupsBefore := len(inf.metricsCleanup)
	defer func() {
		if err != nil {
			err = errors.Join(err, rollbackWorkerMetricCleanups(inf, cleanupsBefore))
		}
	}()
	var workers []Worker

	// TASKQUEUE: 启用时恢复 taskqueue.go 的实现，并取消下面接线的注释。
	// wiring:begin modules.taskqueue.workers taskqueue line
	// queueWorkers, err := buildTaskQueueWorkers(inf)
	// if err != nil {
	// 	return nil, err
	// }
	// workers = append(workers, queueWorkers...)
	// wiring:end modules.taskqueue.workers

	// AUDITLOG: 恢复 auditlog.go 后取消注释；HTTP 停止后再排空审计记录。
	// wiring:begin modules.auditlog.workers auditlog line
	// if inf.auditRecorder != nil {
	// 	workers = append(workers, &auditLogWorker{recorder: inf.auditRecorder})
	// }
	// wiring:end modules.auditlog.workers

	manager, err := NewWorkerManager(DefaultWorkerBudget(), workers...)
	if err != nil {
		return nil, err
	}
	manager.recorder = inf.recorder
	return manager, nil
}

// rollbackWorkerMetricCleanups 逆序执行 cleanupsBefore 之后追加的指标注销
// 回调并截断切片，让失败后的 infra 状态与进入 buildWorkers 之前一致。
//
// 副作用产生点与错误返回点之间的窗口只有构造者自己看得见：调用方按
// "成功后登记回滚"的模式工作，凡在 buildWorkers 内部注册、又在同函数内
// 失败的回调必然逃逸调用方的回滚栈——那是机制上的系统性缺口，
// 不是某一处的偶然遗漏（bootstrap-module-assembly spec）。
func rollbackWorkerMetricCleanups(inf *infra, cleanupsBefore int) error {
	added := inf.metricsCleanup[cleanupsBefore:]
	var errs []error
	for _, a := range slices.Backward(added) {
		if cerr := a(); cerr != nil {
			errs = append(errs, fmt.Errorf("unregister worker metrics during rollback: %w", cerr))
		}
	}
	inf.metricsCleanup = inf.metricsCleanup[:cleanupsBefore]
	return errors.Join(errs...)
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
