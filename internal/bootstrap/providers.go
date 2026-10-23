package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/gtkit/logger/v2"

	"github.com/gtkit/gin-api/internal/middleware"
	authapp "github.com/gtkit/gin-api/internal/module/auth/application"
	authhttp "github.com/gtkit/gin-api/internal/module/auth/transport/http"
	"github.com/gtkit/gin-api/internal/pkg/jwtauth"
	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/gin-api/internal/router"
	"go.opentelemetry.io/otel"

	"github.com/gtkit/gin-api/internal/pkg/httpcache"
	"github.com/gtkit/gin-api/internal/pkg/httpclient"
	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/internal/pkg/tasks"
	"github.com/gtkit/gin-api/internal/platform/telemetry"
	"github.com/gtkit/gin-api/internal/runtime/module/account"
	authruntime "github.com/gtkit/gin-api/internal/runtime/module/auth"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"

	// scaffold:module-imports

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type providerSet struct {
	cleanup    *providerCleanup
	httpConfig HTTPConfig
	httpServer httpServer
	workers    *WorkerManager
	shutdown   *ShutdownCoordinator
	// status 供 App 在 pprof 启动后登记它，让异常退出能出现在 readiness 里。
	status *processStatus
}

// infra 聚合从 Runtime 解析出的进程级基础设施连接与派生配置，供后续各装配阶段共享。
type infra struct {
	accessLogger *logger.Logger
	resources    *data.Resources
	cfg          *config.Config
	mysqlDB      *gorm.DB
	httpConfig   HTTPConfig
	// rateLimits 是限流注册表与键生成器；未配置策略时为零值。
	rateLimits rateLimitAssembly
	// outbound 是具名出站执行入口（客户端 + 熔断），供各模块按下游名取用。
	outbound *httpclient.Downstreams
	// cache 是 GET 响应缓存装配器；关闭时它产出透传中间件。
	cache httpcache.ResponseCache
	// telemetry 是 OTel 导出侧；关停时按独立预算 flush。
	telemetry *telemetry.Provider
	// tasksProducer 是异步任务入队端；未启用时 Enqueue 报 ErrDisabled。
	tasksProducer *tasks.Producer
	// recorder 是指标仪表。默认走 OTel 的 no-op：不接导出时每次记录都是
	// 空操作，因此这里无条件构造，调用点不必判空。
	recorder *metrics.Recorder
	// metricsCleanup 注销异步回调，进程关停时调用。
	//
	// 不注销的话，回调会继续持有已经停掉的 worker 与已关闭的连接池，
	// 采集到的是一份永远不再变化的陈旧状态。
	metricsCleanup []func() error
}

// services 聚合各模块的 application service，供中间件、路由、worker 三个阶段引用。
// 新增模块时在此追加一个具名字段（scaffold 生成器经 module-service-fields 标记注入）。
type services struct {
	cleanup func() error
	// auth 是本地认证模块的装配产物；auth.mode 为 external 时是零值。
	//
	// 零值可用是有意的：外部身份模式下这些端点不存在，而"不存在"应当表达为
	// 一个不被注册的模块，不是一个注册了却永远报错的模块。
	auth authruntime.Assembly
	// accounts 独立于 auth 业务模块，提供账号状态、会员及权威角色校验。
	accounts account.Assembly
	// scaffold:module-service-fields
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

func newProvidersWithBudget(
	ctx context.Context, runtime *Runtime, shutdown *ShutdownCoordinator, budget *rollbackBudget,
) (_ *providerSet, err error) {
	// 失败回滚：装配已产生的进程级副作用（指标回调、限流注册表）逆序撤销。
	// 只关 Runtime 是不够的——指标回调挂在进程级 MeterProvider 上持有已关闭
	// 连接池的引用，限流执行体的后台 goroutine 仍在运行。
	// 成功时把同一份登记交给 App，不再维护第二份关闭清单。
	cleanup := &providerCleanup{}
	defer func() {
		if err != nil {
			err = errors.Join(err, cleanup.rollback(budget.Context(ctx)))
		}
	}()

	// 0. 校验翻译器。必须先于任何可能发生请求绑定的时点：validator 在首次
	//    校验某类型时缓存字段名元数据，晚初始化会让已缓存类型永远输出
	//    Go 字段名。它不依赖任何资源、无副作用可回滚，失败即启动失败。
	if err := initValidation(); err != nil {
		return nil, err
	}

	// 1. 基础设施：数据库连接、配置文件、HTTP 配置、日志、消息队列、缓存、文件存储等。
	inf, err := resolveInfra(runtime)
	if err != nil {
		return nil, err
	}

	// 2a. 遥测导出侧。**必须先于指标仪表**：Recorder 在构造时就从全局
	//     MeterProvider 取 Meter，之后再换 provider，那些仪表仍挂在旧的上——
	//     表现为"接了 OTLP 但一条指标也没出去"，而且没有任何报错。
	//     关闭时它不改动任何全局 provider，进程与没有这一步完全等价。
	telemetryProvider, err := telemetry.New(
		ctx, inf.cfg.Telemetry, inf.cfg.App.Name)
	if err != nil {
		return nil, fmt.Errorf("build telemetry: %w", err)
	}
	inf.telemetry = telemetryProvider
	cleanup.push("telemetry", cleanupTelemetry, telemetryProvider.Shutdown)
	if telemetryProvider.Enabled() {
		logger.Infof("OpenTelemetry export enabled (endpoint=%s, metrics=%t, traces=%t)",
			inf.cfg.Telemetry.Endpoint, inf.cfg.Telemetry.Metrics, inf.cfg.Telemetry.Traces)
	}

	// 2a'. 数据层追踪埋点（GORM + 全部已初始化 Redis DB）。必须在 telemetry.New
	//      之后：otelgorm 在构造期就从全局 TracerProvider 取 tracer，先挂后设
	//      provider 与 2b 对 Meter 的顾虑是同一件事。埋点无条件挂载、不设开关，
	//      未接导出时走全局 no-op tracer（「埋点与导出是两件事」，
	//      docs/observability.md）；失败即装配失败——埋点缺失是静默的观测盲区。
	if err := inf.resources.InstrumentTracing(inf.cfg.Database.Name); err != nil {
		return nil, fmt.Errorf("instrument data-layer tracing: %w", err)
	}

	// 2b. 指标仪表。必须先于中间件与路由：两者都要在装配期拿到它。
	//     未接导出时全局是 no-op，每次记录都是空操作。
	recorder, err := metrics.New(otel.GetMeterProvider().Meter(metricsScope))
	if err != nil {
		return nil, fmt.Errorf("build metrics: %w", err)
	}
	inf.recorder = recorder

	if unregister, dbErr := registerDBPoolMetrics(recorder, inf.resources); dbErr != nil {
		return nil, fmt.Errorf("register db pool metrics: %w", dbErr)
	} else if unregister != nil {
		inf.metricsCleanup = append(inf.metricsCleanup, unregister)
		cleanup.push("db pool metrics", cleanupMetrics, func(context.Context) error { return unregister() })
	}

	// 3. 限流。必须先于 service 与路由装配：认证模块的账号维度限流要从
	//    注册表取策略，路由 registrar 要按 ID 解析策略——策略非法属装配错误，
	//    应在两者之前就失败。
	rateLimits, err := buildRateLimits(ctx, inf.cfg, inf.resources)
	if err != nil {
		return nil, fmt.Errorf("build rate limits: %w", err)
	}
	inf.rateLimits = rateLimits
	cleanup.push("rate limits", cleanupExtensions, func(context.Context) error { return closeRateLimits(rateLimits) })

	// 3b. 出站执行入口：具名客户端 + 按同一批下游名登记的熔断实例
	//     （outbound-http-resilience spec）。必须先于 service 装配：下游集合
	//     是启动期事实，service 在构造时就按名字取用，名字没登记属装配错误。
	//     不做 I/O（只建连接池），因此不需要启动 ctx。
	outbound, err := httpclient.NewDownstreams(inf.cfg.Outbound)
	if err != nil {
		return nil, fmt.Errorf("build outbound downstreams: %w", err)
	}
	inf.outbound = outbound
	cleanup.push("outbound http clients", cleanupExtensions, func(context.Context) error { return outbound.Close() })
	if names := outbound.Names(); len(names) > 0 {
		logger.Infof("outbound downstreams assembled with circuit breakers: %v", names)
	}

	// 3c. 响应缓存。Runtime 已按 Redis 可达性完成缓存初始化；
	//     这里仅取得它的 typed view，默认关闭时仍产出透传中间件。
	//     必须先于路由装配：模块在注册只读路由时就要挂上它。Redis 已配置
	//     但启动期不可达时由 owner 禁用，避免 providers 重复构造和关闭 Store。
	responseCache := runtime.Cache()
	if responseCache == nil {
		return nil, errors.New("response cache is not initialized")
	}
	inf.cache = responseCache
	if responseCache.Enabled() {
		logger.Infof("GET response cache enabled (store=%s, ttl=%s); it only applies to routes that "+
			"explicitly mount the middleware, and requests carrying identity always bypass it",
			inf.cfg.Cache.Store, inf.cfg.Cache.EffectiveTTL())
	}

	// 3d. 异步任务入队端。必须先于 service：service 要注入它才能入队。
	//     未启用时它是一个空壳，Enqueue 报 ErrDisabled 而不是静默丢弃——
	//     "以为任务排上了、实际什么也没发生"是这类能力最难查的失效形态。
	tasksProducer, err := tasks.NewProducer(inf.cfg.Tasks, inf.resources.Rdb(inf.cfg.Tasks.RedisDB))
	if err != nil {
		return nil, fmt.Errorf("build task producer: %w", err)
	}
	inf.tasksProducer = tasksProducer
	cleanup.push("task producer", cleanupExtensions, tasksProducer.Shutdown)

	// 4. Service：各模块的业务逻辑。
	svc, err := buildServicesWithBudget(ctx, inf, runtime, budget)
	if err != nil {
		return nil, err
	}
	if svc.cleanup != nil {
		cleanup.push("modules", cleanupModules, func(context.Context) error { return svc.cleanup() })
	}

	// 5. 中间件：鉴权、日志、限流、缓存等。
	// 豁免理由：contextcheck 跟进的是 Authenticate **返回的闭包**——那段代码在
	// 每个请求上执行、用请求自身的 ctx，与装配期无关。装配这一步本身不做 I/O。
	//
	// 与限流那步的区别很实在：buildRateLimits 会 ScriptLoad 并跑探针，是真正的
	// 装配期 I/O，因此它拿的是启动 ctx。这里塞一个用不到的 ctx 才是对 API 的谎报。
	mw, err := buildMiddlewares(svc, runtime) //nolint:contextcheck // 见上：被指认的是请求期闭包
	if err != nil {
		return nil, err
	}

	// 6. Worker 与关停协调器。必须先于路由装配：readiness 要读它们的状态，
	//    而 worker 声明非法属装配错误，应在任何路由注册之前就失败。
	//    worker 健康指标的注销回调由 buildWorkers 追加进 inf.metricsCleanup，
	//    只在构造成功后按长度差把增量纳入清理登记。
	cleanupsBefore := len(inf.metricsCleanup)
	workers, err := buildWorkers(&inf)
	if err != nil {
		return nil, fmt.Errorf("build workers: %w", err)
	}
	for _, unregister := range inf.metricsCleanup[cleanupsBefore:] {
		if unregister != nil {
			cleanup.push("worker metrics", cleanupMetrics, func(context.Context) error { return unregister() })
		}
	}
	// 协调器优先用注入的那个：信号在 NewApp 之前就可能到达，唯一的信号监督者
	// （cmd 层）因此比装配更早构造它。未注入时自建，行为不变。
	coordinator := shutdown
	if coordinator == nil {
		coordinator = NewShutdownCoordinator()
	}
	// 清理预算接回强制信号：cleanup ctx 刻意剥离了调用方的取消，
	// 不单独接回的话，第二个信号之后仍要在每个 worker 的清理预算上依次等满。
	workers.SetForceSignal(coordinator.ForcedCh())

	// 7. 路由引擎：组装路由切片并初始化 Gin 引擎。
	//    路由契约不合法属装配错误，在此透传为启动失败。
	// 豁免理由同上：被指认的是 readyHandler **返回的闭包**，探针在每个请求上
	// 用自己的 ctx 执行；路由装配本身只做注册，不发起任何 I/O。
	status := newProcessStatus(coordinator, workers)
	engine, err := buildEngine(inf, svc, mw, status) //nolint:contextcheck // 见上：被指认的是请求期闭包
	if err != nil {
		return nil, err
	}

	// 装配成功：副作用的释放所有权移交 App 的关停序列。
	return &providerSet{
		cleanup:    cleanup,
		httpConfig: inf.httpConfig,
		httpServer: newHTTPServer(engine, inf.httpConfig),
		workers:    workers,
		shutdown:   coordinator,
		status:     status,
	}, nil
}

// resolveInfra 从 Runtime 取出基础设施连接与派生的 HTTP 配置。
func resolveInfra(runtime *Runtime) (infra, error) {
	resources := runtime.Resources()
	if resources == nil {
		return infra{}, errors.New("runtime resources are not initialized")
	}
	mysqlDB := resources.Mdb()
	if mysqlDB == nil {
		return infra{}, errors.New("mysql connection is not available")
	}

	cfg := runtime.Config()
	if runtime.owners == nil || nilResource(runtime.owners.logs) {
		return infra{}, errors.New("runtime logging is not initialized")
	}
	access := runtime.owners.logs.AccessLogger()
	if access == nil {
		return infra{}, errors.New("runtime access logger is not initialized")
	}

	return infra{
		accessLogger: access,
		resources:    resources,
		cfg:          cfg,
		mysqlDB:      mysqlDB,
		httpConfig:   newHTTPConfig(cfg),
	}, nil
}

// buildServicesWithBudget 装配模块，按 module-services 标记接入生成代码。
func buildServicesWithBudget(ctx context.Context, inf infra, runtime *Runtime, budget *rollbackBudget) (_ services, err error) {
	var svc services
	modules := &rollbackStack{}
	defer func() {
		if err != nil {
			budget.Context(ctx)
			err = errors.Join(err, modules.unwind())
		}
	}()
	svc.cleanup = modules.unwind

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

	// scaffold:module-services

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

	// scaffold:module-authorizers

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

// newAuthorizeByID 返回按授权者标识取中间件的函数，供生成模块的路由使用。
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
				"that reference it (register it at the scaffold:module-authorizers marker)", id)
			authorizer = sharedauth.DenyAll(fmt.Sprintf("authorizer %q is not registered", id))
		}
		return middleware.Authorize(authorizer, id)
	}
}

// buildEngine 组装 v1 模块路由切片并初始化 Gin engine（含健康检查路由）。
// 新增模块时只需在 v1Modules 切片里追加一行（scaffold 生成器经 modules 标记注入）。
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
	rateLimitStager, preIdentityRateLimits, unresolvedRateLimits := router.NewRateLimitStager(opt)
	_ = rateLimitStager // 供 scaffold:modules 注入的业务路由使用。

	// 各模块在 /api/v1 组上用 gin 的方式注册自己的路由。
	// 新增模块只需在这里加一行 Register。
	opt.RegisterModules = func(v1 *gin.RouterGroup) {
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
		// scaffold:modules
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
// 母版按配置与已登记的处理器/周期任务装配队列 worker；未启用或集合为空时不注册。
// 其余后台任务由使用者在 scaffold:module-workers 标记前追加一行 Worker 构造；
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
	// defer 覆盖**全部**错误出口，而不只是末尾的构造校验——scaffold:module-workers
	// 扩展段加入多个"构造 + 注册指标"块之后，任何中途 return 都不得让此前
	// 已注册的回调逃逸回滚（bootstrap-module-assembly spec）。
	cleanupsBefore := len(inf.metricsCleanup)
	defer func() {
		if err != nil {
			err = errors.Join(err, rollbackWorkerMetricCleanups(inf, cleanupsBefore))
		}
	}()
	var workers []Worker

	// 队列消费者：未启用队列或未注册任何处理器时不注册。
	//
	// 处理器由使用者在下方标记处按 tasks.Registrar 追加。没有处理器就不注册
	// 消费者——一个空转的 worker 只会让关停多等一轮、让监控里多一个永远
	// 空闲的实例，而不会处理任何东西。
	if consumer := newAsynqConsumerWorker(tasks.NewObservedConsumer(
		inf.cfg.Tasks, inf.resources.Rdb(inf.cfg.Tasks.RedisDB), "asynq-consumer", inf.recorder,
		// scaffold:task-handlers
	)); consumer != nil {
		workers = append(workers, consumer)
	}

	// 周期任务调度器：未启用队列/调度器、或未声明任何周期任务时不注册。
	//
	// 周期任务由使用者在下方标记处按 tasks.PeriodicTask 追加，**每条都必须声明
	// 重复投递时靠什么保证正确**（零值在构造期被拒绝）——底层调度器没有任何
	// 跨实例协调，租约只把重复降到易主窗口内，消除不了它。
	scheduler, schedulerErr := tasks.NewScheduler(
		inf.cfg.Tasks, inf.resources.Rdb(inf.cfg.Tasks.RedisDB), "asynq-scheduler",
		schedulerObserver(),
		// scaffold:periodic-tasks
	)
	if schedulerErr != nil {
		return nil, fmt.Errorf("build task scheduler: %w", schedulerErr)
	}
	if worker := newAsynqSchedulerWorker(scheduler); worker != nil {
		workers = append(workers, worker)
	}

	// scaffold:module-workers

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

// newTokenParser 按配置装配注入 Auth 中间件的 token parser：
// jwt.laravel 关闭时保持裸 Ed25519 issuer（与现状一致）；
// 开启时包一层 Laravel 存量 token 回退验证（只验不签，过渡期兼容）。
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

	// 截止时刻必须真正注入运行期。
	//
	// 此前它只在配置层被校验格式，从未被读取——于是只要 enabled 不关，
	// Laravel 令牌永久有效，而配置里那个日期给了运维一个虚假的下线承诺。
	// 格式已由 config.Validate 保证，这里再解析一次是为了把它变成 time.Time。
	acceptUntil, err := time.Parse(time.RFC3339, strings.TrimSpace(cfg.JWT.LaravelAcceptUntil))
	if err != nil {
		return nil, fmt.Errorf(
			"jwt.laravel.enabled requires jwt.laravel_accept_until as an RFC3339 instant: "+
				"a compatibility window without an end date never ends: %w", err)
	}

	return jwtauth.NewFallbackParser(issuer, laravelVerifier, acceptUntil)
}

// metricsScope 是本进程仪表的作用域名。
const metricsScope = "github.com/gtkit/gin-api"

// registerDBPoolMetrics 以异步回调暴露数据库连接池用量。
//
// 走异步而不是在每次查询后上报：连接池用量每时每刻都在变，没有一个业务事件
// 可以挂靠；而采集时读一次 sql.DB.Stats() 恰好是它设计的用法。
//
// 拿不到 sql.DB 时返回 nil 而不是错误：那意味着数据库根本没接（如某些
// 最小装配），指标缺失是正确结果，不该让进程起不来。
func registerDBPoolMetrics(recorder *metrics.Recorder, resources *data.Resources) (func() error, error) {
	if resources == nil {
		return nil, nil
	}
	gormDB := resources.Mdb()
	if gormDB == nil {
		return nil, nil
	}
	sqlDB, err := gormDB.DB()
	if err != nil || sqlDB == nil {
		return nil, nil //nolint:nilerr // 见上：没有数据库时缺指标是正确结果
	}

	return recorder.RegisterDBPoolStats(func() metrics.DBPoolStats {
		stats := sqlDB.Stats()
		return metrics.DBPoolStats{
			Open:    stats.OpenConnections,
			InUse:   stats.InUse,
			Idle:    stats.Idle,
			Wait:    stats.WaitCount,
			MaxOpen: stats.MaxOpenConnections,
		}
	})
}
