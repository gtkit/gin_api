package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	logger "github.com/gtkit/logger/v2"

	"github.com/gtkit/gin-api/internal/middleware"
	userapp "github.com/gtkit/gin-api/internal/module/user/application"
	userhttp "github.com/gtkit/gin-api/internal/module/user/transport/http"
	jwtauth "github.com/gtkit/gin-api/internal/pkg/jwtauth"
	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/gin-api/internal/router"
	"go.opentelemetry.io/otel"

	"github.com/gtkit/gin-api/internal/pkg/metrics"
	authruntime "github.com/gtkit/gin-api/internal/runtime/module/auth"
	userruntime "github.com/gtkit/gin-api/internal/runtime/module/user"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"

	// scaffold:module-imports

	"github.com/gin-gonic/gin"
	"github.com/gtkit/json"
	"github.com/gtkit/verify"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

var (
	initLogWithConfig = applog.InitWithConfig
	initJWTWithConfig = jwtauth.Assemble
	initVerify        = verify.New
	checkJSONRuntime  = json.CheckJSON
)

type providerSet struct {
	httpConfig HTTPConfig
	httpServer httpServer
	workers    *WorkerManager
	shutdown   *ShutdownCoordinator
	rateLimits rateLimitAssembly
	// status 供 App 在 pprof 启动后登记它，让异常退出能出现在 readiness 里。
	status *processStatus
	// metricsCleanup 注销指标的异步回调，由 App 在关停时调用。
	metricsCleanup []func() error
}

// infra 聚合从 Runtime 解析出的进程级基础设施连接与派生配置，供后续各装配阶段共享。
type infra struct {
	resources  *data.Resources
	cfg        *config.Config
	mysqlDB    *gorm.DB
	httpConfig HTTPConfig
	// rateLimits 是限流注册表与键生成器；未配置策略时为零值。
	rateLimits rateLimitAssembly
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
	// user 是 user 模块的 application service。
	//
	// 名字对齐**模块**而不是它当下的某一项用途：叫 verifier 时，
	// 每次给该 service 加一个与"校验"无关的能力，字段名就多错一分，
	// 而生成器给新模块产出的字段一律是 <模块名>Service。
	user *userapp.Service
	// auth 是本地认证模块的装配产物；auth.mode 为 external 时是零值。
	//
	// 零值可用是有意的：外部身份模式下这些端点不存在，而"不存在"应当表达为
	// 一个不被注册的模块，不是一个注册了却永远报错的模块。
	auth authruntime.Assembly
	// scaffold:module-service-fields
}

// middlewares 聚合路由装配所需的 Gin 中间件与授权注册表。
type middlewares struct {
	auth         gin.HandlerFunc
	optionalAuth gin.HandlerFunc
	authorizers  *sharedauth.AuthorizerRegistry
}

// newProviders 按「基础设施 → service → 中间件 → 路由引擎 → worker」五个阶段编排装配，
// 每个阶段收敛到一个单一职责的私有函数，本函数只做顺序拼接与错误透传。
func newProviders(ctx context.Context, runtime *Runtime) (*providerSet, error) {
	// 1. 基础设施：数据库连接、配置文件、HTTP 配置、日志、消息队列、缓存、文件存储等。
	inf, err := resolveInfra(runtime)
	if err != nil {
		return nil, err
	}

	// 2. 指标仪表。必须先于中间件与路由：两者都要在装配期拿到它。
	//    默认 no-op，接导出只需在进程启动前 otel.SetMeterProvider(...)。
	recorder, err := metrics.New(otel.GetMeterProvider().Meter(metricsScope))
	if err != nil {
		return nil, fmt.Errorf("build metrics: %w", err)
	}
	inf.recorder = recorder

	if unregister, dbErr := registerDBPoolMetrics(recorder, inf.resources); dbErr != nil {
		return nil, fmt.Errorf("register db pool metrics: %w", dbErr)
	} else if unregister != nil {
		inf.metricsCleanup = append(inf.metricsCleanup, unregister)
	}

	// 3. Service：各模块的业务逻辑。
	svc, err := buildServices(ctx, inf, runtime)
	if err != nil {
		return nil, err
	}

	// 4. 中间件：鉴权、日志、限流、缓存等。
	// 豁免理由：contextcheck 跟进的是 Authenticate **返回的闭包**——那段代码在
	// 每个请求上执行、用请求自身的 ctx，与装配期无关。装配这一步本身不做 I/O。
	//
	// 与限流那步的区别很实在：buildRateLimits 会 ScriptLoad 并跑探针，是真正的
	// 装配期 I/O，因此它拿的是启动 ctx。这里塞一个用不到的 ctx 才是对 API 的谎报。
	mw, err := buildMiddlewares(svc, runtime) //nolint:contextcheck // 见上：被指认的是请求期闭包
	if err != nil {
		return nil, err
	}

	// 5. 限流。必须先于路由装配：registrar 要按 ID 解析策略，
	//    而策略非法属装配错误，应在任何路由注册之前就失败。
	rateLimits, err := buildRateLimits(ctx, inf.cfg, inf.resources)
	if err != nil {
		return nil, fmt.Errorf("build rate limits: %w", err)
	}
	inf.rateLimits = rateLimits

	// 6. Worker 与关停协调器。必须先于路由装配：readiness 要读它们的状态，
	//    而 worker 声明非法属装配错误，应在任何路由注册之前就失败。
	workers, err := buildWorkers(&inf, svc)
	if err != nil {
		return nil, fmt.Errorf("build workers: %w", err)
	}
	coordinator := NewShutdownCoordinator()

	// 7. 路由引擎：组装路由切片并初始化 Gin 引擎。
	//    路由契约不合法属装配错误，在此透传为启动失败。
	// 豁免理由同上：被指认的是 readyHandler **返回的闭包**，探针在每个请求上
	// 用自己的 ctx 执行；路由装配本身只做注册，不发起任何 I/O。
	status := newProcessStatus(coordinator, workers)
	engine, err := buildEngine(inf, svc, mw, status) //nolint:contextcheck // 见上：被指认的是请求期闭包
	if err != nil {
		return nil, err
	}

	return &providerSet{
		httpConfig:     inf.httpConfig,
		httpServer:     newHTTPServer(engine, inf.httpConfig),
		workers:        workers,
		shutdown:       coordinator,
		rateLimits:     rateLimits,
		status:         status,
		metricsCleanup: inf.metricsCleanup,
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

	return infra{
		resources:  resources,
		cfg:        cfg,
		mysqlDB:    mysqlDB,
		httpConfig: newHTTPConfig(cfg),
	}, nil
}

// buildServices 把基础设施翻译成各模块的 application service，传递最小依赖。
// 新增模块时在此追加一行 service 构造（scaffold 生成器经 module-services 标记注入）。
func buildServices(ctx context.Context, inf infra, runtime *Runtime) (services, error) {
	var svc services

	// 创建 user 模块 application service，传递最小依赖。
	// 装配失败直接上抛：依赖不全的 service 不允许进入运行期。
	userService, err := userruntime.NewService(inf.mysqlDB)
	if err != nil {
		return services{}, fmt.Errorf("build user service: %w", err)
	}
	svc.user = userService

	// 本地认证按模式条件装配。
	//
	// 外部身份模式下整个模块不存在——那种部署里令牌由外部签发，
	// 这些端点没有任何意义，留着只是多一组无人看管的凭据入口。
	if inf.cfg.Auth.LocalEnabled() {
		assembly, authErr := authruntime.New(ctx, authruntime.Deps{
			DB:     inf.mysqlDB,
			Redis:  inf.resources.Rdb(0),
			Issuer: runtime.TokenIssuer(),
			Config: inf.cfg.Auth,
			// 访问令牌寿命的唯一来源是 JWT 配置：签发它的是 JWT 装配。
			AccessTTL: inf.cfg.JWT.EffectiveTokenTTL(),
			KeyPrefix: inf.cfg.App.Name,
			Metrics:   inf.recorder,
		})
		if authErr != nil {
			return services{}, fmt.Errorf("build auth module: %w", authErr)
		}
		svc.auth = assembly
	}
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

	auth, err := middleware.Authenticate(middleware.AuthOptions{
		Verifier:   svc.user,
		Parser:     tokenParser,
		Revocation: revocation,
	})
	if err != nil {
		return middlewares{}, err
	}
	optionalAuth, err := middleware.Authenticate(middleware.AuthOptions{
		Verifier:          svc.user,
		Parser:            tokenParser,
		Revocation:        revocation,
		AllowMissingToken: true,
	})
	if err != nil {
		return middlewares{}, err
	}

	authorizers, err := buildAuthorizers(svc)
	if err != nil {
		return middlewares{}, err
	}

	return middlewares{auth: auth, optionalAuth: optionalAuth, authorizers: authorizers}, nil
}

// buildAuthorizers 登记进程内全部具名授权实现。
//
// 这里是唯一的注册点：ID 在此与 RouteSpec 的引用对齐，启动期一次性校验，
// 引用了未登记的 ID 会直接启动失败。
func buildAuthorizers(svc services) (*sharedauth.AuthorizerRegistry, error) {
	reg := sharedauth.NewAuthorizerRegistry()

	// role.admin 目前**没有权威角色源**：仓库里没有角色表，也没有 IAM adapter。
	// 唯一安全的占位是恒拒绝——绝不能仅凭 30 天 TTL 的 JWT role claim 放行 admin，
	// 那等于把"一次签发"当成"持续有效的高权限"。
	// 接入真实角色源后替换本实现，RouteSpec 无需改动。
	if err := reg.Register("role.admin", sharedauth.DenyAll(
		"no authoritative role source is wired yet; replace this adapter after integrating one")); err != nil {
		return nil, err
	}

	// entitlement.vip 是共享的会员资格判定，供多个模块经 RouteSpec 引用，
	// 不复制进任何模板——复制会让"会员判定"出现多份可能不一致的实现。
	if err := reg.Register("entitlement.vip", newVIPAuthorizer(svc.user)); err != nil {
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

// buildEngine 组装 v1 模块路由切片并初始化 Gin engine（含健康检查路由）。
// 新增模块时只需在 v1Modules 切片里追加一行（scaffold 生成器经 modules 标记注入）。
func buildEngine(inf infra, svc services, mw middlewares, status router.ProcessStatus) (*gin.Engine, error) {
	cfg := inf.cfg

	// 组装模块路由切片——新增模块只需在此追加一行。
	//
	// 认证模块不在这个切片里：它按 auth.mode 条件注册（见下方 append）。
	// 外部身份模式下它根本不存在，而不是注册一个永远报错的模块。
	v1Modules := []router.Module{
		userhttp.NewModule(svc.user),
		// scaffold:modules
	}
	// 认证模块只在本地认证模式下注册。
	if svc.auth.Module != nil {
		v1Modules = append(v1Modules, svc.auth.Module)
	}
	// var v2Modules []router.Module

	engine, err := router.InitRouterWithVersionedModules(string(cfg.Env), router.VersionedModules{
		V1: v1Modules,
		// V2: v2Modules,
	}, router.RouterOptions{
		AllowedOrigins:  cfg.CORS.AllowedOrigins,
		LogRequestBody:  cfg.Log.AccessRequestBody,
		LogResponseBody: cfg.Log.AccessResponseBody,
		MaxBodyBytes:    cfg.App.MaxBodyBytes,
		// 默认空 = 不信任任何代理。必须显式传递：Gin 不设置时的默认是信任所有代理。
		TrustedProxies:  cfg.Proxy.TrustedProxies,
		RemoteIPHeaders: cfg.Proxy.RemoteIPHeaders,
		Metrics:         inf.recorder,
		// 系统探针与鉴权都经描述符统一装配，不再旁路注册、不再由模块自己 Use。
		HealthChecker: newHealthChecker(inf.resources),
		// readiness 用的 Redis 必需性与资源装配同源，避免两处口径漂移。
		RedisRequired:          redisRequired(cfg),
		RateLimits:             inf.rateLimits.registry,
		RateLimitKeys:          inf.rateLimits.keys,
		ProcessStatus:          status,
		AuthMiddleware:         mw.auth,
		OptionalAuthMiddleware: mw.optionalAuth,
		Authorizers:            mw.authorizers,
	})
	if err != nil {
		return nil, err
	}

	return engine, nil
}

// buildWorkers 组装进程级后台任务。
//
// 母版内置的业务 worker 只有一个：注销档案清理，且**按配置条件注册**——
// 未设置保留期时它根本不存在（而不是注册一个空转的实例占着名字）。
// 其余后台任务由使用者在 scaffold:module-workers 标记前追加一行 Worker 构造。
// 形参 services 保持在位，使追加「需要 application service 的 worker」
// 是纯追加式改动，无需改签名。
//
// 集合为空不意味着 worker 机制多余——WorkerManager 配合 app.go 的分相关停
// （停 producer → 等 LB 传播 → 关 HTTP → drain consumer）保证两类语义各自成立：
// producer 不会在关停期继续生产任务，consumer 仍能消费完在飞请求写入的任务。
//
// 返回 error 是因为 worker 的声明（类别/生命周期/组合/重名）在注册期即校验：
// 非法声明是编码错误，启动时零成本可判，留到运行期只能靠观察行为反推。
func buildWorkers(inf *infra, _ services) (*WorkerManager, error) {
	var workers []Worker

	// 注销档案清理：未配置保留期时不注册。
	//
	// 这张表存的是个人信息（原手机号、邮箱、昵称、来源 IP），保留期必须由使用方
	// 按所在司法辖区确定——各行业与各地对最长保留期、删除权响应时限的要求并不
	// 一致，脚手架替使用者选一个数字只会让它在某些辖区是错的，而且是静默错的。
	cleanup, err := userruntime.NewDeactivationCleanup(inf.mysqlDB, inf.cfg.Modules.User)
	if err != nil {
		return nil, err
	}
	if w := newDeactivationCleanupWorker(cleanup); w != nil {
		workers = append(workers, w)

		// 健康状态走异步回调：连续失败数与最后成功时间都是**状态**，
		// 而这个任务默认一天只跑一次。主动上报的话这两个指标一天只有一个
		// 数据点，中间二十多小时没有值——告警规则拿不到数，只能配"无数据"
		// 这种钝规则。异步回调则在每个采集周期读一次当前值。
		unregister, regErr := inf.recorder.RegisterWorkerHealth(w.Name(),
			func() metrics.WorkerHealth {
				failures, lastSuccess := cleanup.Health()
				return metrics.WorkerHealth{
					ConsecutiveFailures: failures,
					LastSuccess:         lastSuccess,
				}
			})
		if regErr != nil {
			return nil, fmt.Errorf("register cleanup worker metrics: %w", regErr)
		}
		inf.metricsCleanup = append(inf.metricsCleanup, unregister)
	}

	// scaffold:module-workers

	return NewWorkerManager(DefaultWorkerBudget(), workers...)
}

// newTokenParser 按配置装配注入 Auth 中间件的 token parser：
// jwt.laravel 关闭时保持裸 Ed25519 issuer（与现状一致）；
// 开启时包一层 Laravel 存量 token 回退验证（只验不签，过渡期兼容）。
func newTokenParser(cfg *config.Config, issuer jwtauth.TokenParser) (middleware.TokenParser, error) {
	laravelCfg := cfg.JWT.Laravel
	if !laravelCfg.Enabled {
		return issuer, nil
	}

	laravelVerifier, err := jwtauth.NewLaravelVerifier(laravelCfg.Secret, laravelCfg.Algorithm, sharedauth.ClientRole)
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

// initProcessRuntime 初始化**进程级、无连接**的运行时设施：时区、日志、校验器、JSON 实现。
//
// 数据库与 Redis 连接不在这里建立：它们是有生命周期的资源，由 Resources 统一持有并逆序关闭。
// 旧实现把两者混在一起，于是"初始化"既包含无需清理的全局设置，又包含必须清理的连接，
// 失败路径要清理哪些就没有确定答案。
func initProcessRuntime(cfg *config.Config) error {
	applyRuntimeTimezone(cfg)
	initLogWithConfig(cfg)
	// JWT issuer 不在此处创建：改由 NewRuntime 创建一次并持有，避免"预检创建一份又丢弃、
	// 随后再创建一份"的冗余（见 runtime.go）。
	if err := initVerify(); err != nil {
		return fmt.Errorf("init validator: %w", err)
	}
	checkJSONRuntime()

	applog.InitAccessLog(cfg)

	// 非生产允许 0.0.0.0/0 以便本地调试转发头，但不能让它无声地被带到预发环境。
	// 生产环境该配置由 config.Validate 直接拒绝，走不到这里。
	// 填了却没接线的扩展位：不阻断启动，但必须说出来。
	// 一个填了却静默无效的配置，比没有这个配置更容易让人误判能力边界。
	for _, w := range cfg.UnwiredExtensionWarnings() {
		logger.Warn(w)
	}

	// 个人信息无保留期：配置注释承诺过会提示一次，此前只是承诺。
	for _, w := range cfg.RetentionWarnings() {
		logger.Warn(w)
	}

	for _, w := range cfg.Proxy.Warnings(cfg.Env) {
		logger.Warn(w)
	}

	logger.Infof("runtime initialized with env=%s", cfg.Env)
	return nil
}

// resolveDBCredentials 决定本次装配的数据库连接使用哪套凭据。
//
// 只有 migrate 子命令会请求迁移凭据；server 恒走运行凭据。迁移凭据未配置时回退运行凭据
// 并打印告警——不失败，因为存量部署尚未配置迁移凭据，直接 fail 会破坏它们。
// 告警只说明"正在使用运行凭据"，绝不带用户名或密码。
func resolveDBCredentials(cfg *config.Config, opts Options) config.DBCredentials {
	if !opts.UseMigrationCredentials {
		return cfg.Database.RuntimeCredentials()
	}
	if creds, ok := cfg.Database.MigrationCredentials(); ok {
		return creds
	}
	logger.Warn(migrationFallbackWarning)
	return cfg.Database.RuntimeCredentials()
}

// migrationFallbackWarning 是回退告警的固定文本。
//
// 刻意做成无格式化占位符的常量：告警绝不能带用户名或密码，而一旦允许拼接，
// 就迟早有人为了"方便排障"把凭据加进来。测试锁住这一点。
const migrationFallbackWarning = "migration is using the runtime database credentials: " +
	"set database.migrate_username to grant DDL only to a dedicated account"

func applyRuntimeTimezone(cfg *config.Config) {
	if cfg == nil || strings.TrimSpace(cfg.App.Timezone) == "" {
		return
	}
	loc, err := time.LoadLocation(cfg.App.Timezone)
	if err != nil {
		logger.Warn("load runtime timezone failed", zap.String("timezone", cfg.App.Timezone), zap.Error(err))
		return
	}
	// 在所有 goroutine 启动之前设置，后续应通过显式 *time.Location 传递。
	time.Local = loc //nolint:gosmopolitan // Process-wide timezone is set once before any worker goroutine starts.
}

// closeProcessRuntime 收尾进程级设施（刷日志缓冲）。
//
// 它**不关闭任何连接**：连接由 Resources.Close 按建立的逆序释放。
func closeProcessRuntime() {
	applog.SyncAccessLog()
	logger.Sync()
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
