package bootstrap

import (
	"errors"
	"fmt"
	"strings"
	"time"

	logger "github.com/gtkit/logger/v2"

	"github.com/gtkit/gin-api/internal/middleware"
	userapp "github.com/gtkit/gin-api/internal/module/user/application"
	userhttp "github.com/gtkit/gin-api/internal/module/user/transport/http"
	legacyjwtauth "github.com/gtkit/gin-api/internal/pkg/jwtauth"
	legacylog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/platform/config"
	legacydao "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/gin-api/internal/router"
	userruntime "github.com/gtkit/gin-api/internal/runtime/module/user"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"

	// scaffold:module-imports

	"github.com/gin-gonic/gin"
	"github.com/gtkit/json"
	"github.com/gtkit/tools/mailx"
	"github.com/gtkit/verify"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

var (
	initLogWithConfig = legacylog.InitWithConfig
	initJWTWithConfig = legacyjwtauth.NewWithConfig
	initVerify        = verify.New
	checkJSONRuntime  = json.CheckJSON
	buildMailClient   = newMailClientFromConfig
)

type providerSet struct {
	httpConfig HTTPConfig
	httpServer httpServer
	workers    *WorkerManager
	shutdown   *ShutdownCoordinator
}

// infra 聚合从 Runtime 解析出的进程级基础设施连接与派生配置，供后续各装配阶段共享。
type infra struct {
	resources  *legacydao.Resources
	cfg        *config.Config
	mysqlDB    *gorm.DB
	httpConfig HTTPConfig
	// mailClient SMTP 客户端；email.host 未配置时为 nil，消费方注入前判空。
	mailClient *mailx.Client
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
func newProviders(runtime *Runtime) (*providerSet, error) {
	// 1. 基础设施：数据库连接、配置文件、HTTP 配置、日志、消息队列、缓存、文件存储等。
	inf, err := resolveInfra(runtime)
	if err != nil {
		return nil, err
	}

	// 2. Service：各模块的业务逻辑。
	svc, err := buildServices(inf)
	if err != nil {
		return nil, err
	}

	// 3. 中间件：鉴权、日志、限流、缓存等。
	mw, err := buildMiddlewares(svc, runtime)
	if err != nil {
		return nil, err
	}

	// 4. Worker 与关停协调器。必须先于路由装配：readiness 要读它们的状态，
	//    而 worker 声明非法属装配错误，应在任何路由注册之前就失败。
	workers, err := buildWorkers(svc)
	if err != nil {
		return nil, fmt.Errorf("build workers: %w", err)
	}
	coordinator := NewShutdownCoordinator()

	// 5. 路由引擎：组装路由切片并初始化 Gin 引擎。
	//    路由契约不合法属装配错误，在此透传为启动失败。
	engine, err := buildEngine(inf, svc, mw, newProcessStatus(coordinator, workers))
	if err != nil {
		return nil, err
	}

	return &providerSet{
		httpConfig: inf.httpConfig,
		httpServer: newHTTPServer(engine, inf.httpConfig),
		workers:    workers,
		shutdown:   coordinator,
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
	mailClient, err := buildMailClient(cfg)
	if err != nil {
		return infra{}, fmt.Errorf("init mail client: %w", err)
	}

	return infra{
		resources:  resources,
		cfg:        cfg,
		mysqlDB:    mysqlDB,
		httpConfig: newHTTPConfig(cfg),
		mailClient: mailClient,
	}, nil
}

// newMailClientFromConfig 把 email 配置翻译成 mailx 客户端。
// email.host 为空视为未启用邮件能力，返回 (nil, nil) 不阻塞启动；
// 配置存在但非法（如 from 地址格式错误）fail-fast。
// 465 端口自动走隐式 SSL（SMTPS），其余端口走 mailx 默认的强制 TLS（STARTTLS）。
func newMailClientFromConfig(cfg *config.Config) (*mailx.Client, error) {
	if cfg == nil || strings.TrimSpace(cfg.Email.Host) == "" {
		return nil, nil
	}

	opts := make([]mailx.Option, 0, 4)
	if cfg.Email.Port > 0 {
		opts = append(opts, mailx.WithPort(cfg.Email.Port))
	}
	if cfg.Email.Port == 465 {
		opts = append(opts, mailx.WithImplicitSSL())
	}
	if cfg.Email.User != "" {
		opts = append(opts, mailx.WithCredentials(cfg.Email.User, cfg.Email.Pass))
	}
	if cfg.Email.Alias != "" {
		opts = append(opts, mailx.WithFromName(cfg.Email.Alias))
	}

	return mailx.New(cfg.Email.Host, cfg.Email.User, opts...)
}

// buildServices 把基础设施翻译成各模块的 application service，传递最小依赖。
// 新增模块时在此追加一行 service 构造（scaffold 生成器经 module-services 标记注入）。
func buildServices(inf infra) (services, error) {
	var svc services

	// 创建 user 模块 application service，传递最小依赖。
	// 装配失败直接上抛：依赖不全的 service 不允许进入运行期。
	userService, err := userruntime.NewService(inf.mysqlDB)
	if err != nil {
		return services{}, fmt.Errorf("build user service: %w", err)
	}
	svc.user = userService
	// scaffold:module-services

	return svc, nil
}

// buildMiddlewares 构建路由所需的鉴权中间件。
// legacy Laravel 兼容开启时，token parser 注入回退组合校验（只验不签，过渡期兼容）。
func buildMiddlewares(svc services, runtime *Runtime) (middlewares, error) {
	tokenParser, err := newTokenParser(runtime.Config(), runtime.TokenIssuer())
	if err != nil {
		return middlewares{}, err
	}

	auth, err := middleware.Authenticate(middleware.AuthOptions{
		Verifier: svc.user,
		Parser:   tokenParser,
	})
	if err != nil {
		return middlewares{}, err
	}
	optionalAuth, err := middleware.Authenticate(middleware.AuthOptions{
		Verifier:          svc.user,
		Parser:            tokenParser,
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
	v1Modules := []router.Module{
		userhttp.NewModule(svc.user),
		// scaffold:modules
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
		// 系统探针与鉴权都经描述符统一装配，不再旁路注册、不再由模块自己 Use。
		HealthChecker: newHealthChecker(inf.resources),
		// readiness 用的 Redis 必需性与资源装配同源，避免两处口径漂移。
		RedisRequired:          redisRequired(cfg),
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
// 作为脚手架母版，这里默认不注册任何业务 worker：具体后台任务由使用者在
// scaffold:module-workers 标记前追加一行 Worker 构造。形参 services 保持在位，
// 使追加「需要 application service 的 worker」是纯追加式改动，无需改签名。
//
// 集合为空不意味着 worker 机制多余——WorkerManager 配合 app.go 的分相关停
// （停 producer → 等 LB 传播 → 关 HTTP → drain consumer）保证两类语义各自成立：
// producer 不会在关停期继续生产任务，consumer 仍能消费完在飞请求写入的任务。
//
// 返回 error 是因为 worker 的声明（类别/生命周期/组合/重名）在注册期即校验：
// 非法声明是编码错误，启动时零成本可判，留到运行期只能靠观察行为反推。
func buildWorkers(_ services) (*WorkerManager, error) {
	return NewWorkerManager(
		DefaultWorkerBudget(),
		// scaffold:module-workers
	)
}

// newTokenParser 按配置装配注入 Auth 中间件的 token parser：
// jwt.legacy_laravel 关闭时保持裸 Ed25519 issuer（与现状一致）；
// 开启时包一层 Laravel 存量 token 回退验证（只验不签，过渡期兼容）。
func newTokenParser(cfg *config.Config, issuer legacyjwtauth.TokenParser) (middleware.TokenParser, error) {
	legacyCfg := cfg.JWT.LegacyLaravel
	if !legacyCfg.Enabled {
		return issuer, nil
	}

	laravelVerifier, err := legacyjwtauth.NewLaravelVerifier(legacyCfg.Secret, legacyCfg.Algorithm, sharedauth.ClientRole)
	if err != nil {
		return nil, fmt.Errorf("init legacy laravel verifier: %w", err)
	}

	return legacyjwtauth.NewFallbackParser(issuer, laravelVerifier)
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

	legacylog.InitAccessLog(cfg)

	// 非生产允许 0.0.0.0/0 以便本地调试转发头，但不能让它无声地被带到预发环境。
	// 生产环境该配置由 config.Validate 直接拒绝，走不到这里。
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
	legacylog.SyncAccessLog()
	logger.Sync()
}
