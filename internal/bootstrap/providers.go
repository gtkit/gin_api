package bootstrap

import (
	"fmt"
	"strings"
	"time"

	"github.com/gtkit/gin-api/internal/middleware"
	userapp "github.com/gtkit/gin-api/internal/module/user/application"
	userhttp "github.com/gtkit/gin-api/internal/module/user/transport/http"
	"github.com/gtkit/gin-api/internal/pkg/file"
	legacyjwtauth "github.com/gtkit/gin-api/internal/pkg/jwtauth"
	legacylog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	legacydao "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/gin-api/internal/router"
	userruntime "github.com/gtkit/gin-api/internal/runtime/module/user"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
	logger "github.com/gtkit/logger/v2"

	// scaffold:module-imports

	"github.com/gin-gonic/gin"
	"github.com/gtkit/json"
	"github.com/gtkit/tools/mailx"
	"github.com/gtkit/verify"
	"go.uber.org/zap"
)

var (
	initLogWithConfig  = legacylog.InitWithConfig
	initNewsWithConfig = news.InitWithConfig
	initFileWithConfig = file.InitWithConfig
	initDAOWithConfig  = legacydao.NewWithCredentials
	initJWTWithConfig  = legacyjwtauth.NewWithConfig
	initVerify         = verify.New
	checkJSONRuntime   = json.CheckJSON
	buildMailClient    = newMailClientFromConfig
)

type providerSet struct {
	httpConfig HTTPConfig
	httpServer httpServer
	workers    *WorkerManager
}

// infra 聚合从 Runtime 解析出的进程级基础设施连接与派生配置，供后续各装配阶段共享。
type infra struct {
	dao        legacydao.Dao
	cfg        *config.Config
	mysqlDB    legacydao.MyDB
	httpConfig HTTPConfig
	// mailClient SMTP 客户端；email.host 未配置时为 nil，消费方注入前判空。
	mailClient *mailx.Client
}

// services 聚合各模块的 application service，供中间件、路由、worker 三个阶段引用。
// 新增模块时在此追加一个具名字段（scaffold 生成器经 module-service-fields 标记注入）。
type services struct {
	verifier *userapp.Service
	// scaffold:module-service-fields
}

// middlewares 聚合路由装配所需的 Gin 中间件。
type middlewares struct {
	auth gin.HandlerFunc
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

	// 4. 路由引擎：组装路由切片并初始化 Gin 引擎。
	engine := buildEngine(inf, svc, mw)

	// 5. Worker：后台任务。
	return &providerSet{
		httpConfig: inf.httpConfig,
		httpServer: newHTTPServer(engine, inf.httpConfig),
		workers:    buildWorkers(svc),
	}, nil
}

// resolveInfra 从 Runtime 取出基础设施连接（MySQL）与派生的 HTTP 配置。
func resolveInfra(runtime *Runtime) (infra, error) {
	dao := runtime.DB()
	if dao == nil {
		return infra{}, fmt.Errorf("legacy dao is not initialized")
	}

	cfg := runtime.Config()
	mailClient, err := buildMailClient(cfg)
	if err != nil {
		return infra{}, fmt.Errorf("init mail client: %w", err)
	}

	return infra{
		dao:        dao,
		cfg:        cfg,
		mysqlDB:    dao.Mdb(),
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
	svc.verifier = userruntime.NewService(inf.mysqlDB)
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

	return middlewares{
		auth: middleware.Auth(svc.verifier, tokenParser),
	}, nil
}

// buildEngine 组装 v1 模块路由切片并初始化 Gin engine（含健康检查路由）。
// 新增模块时只需在 v1Modules 切片里追加一行（scaffold 生成器经 modules 标记注入）。
func buildEngine(inf infra, svc services, mw middlewares) *gin.Engine {
	cfg := inf.cfg

	// 组装模块路由切片——新增模块只需在此追加一行。
	v1Modules := []router.Module{
		userhttp.NewModule(svc.verifier, mw.auth),
		// scaffold:modules
	}
	// var v2Modules []router.Module

	engine := router.InitRouterWithVersionedModules(string(cfg.Env), router.VersionedModules{
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
	})
	router.RegisterHealthRoutes(engine, newHealthChecker(inf.dao))

	return engine
}

// buildWorkers 组装进程级后台任务。
//
// 作为脚手架母版，这里默认不注册任何业务 worker：具体后台任务由使用者在
// scaffold:module-workers 标记前追加一行 Worker 构造。形参 services 保持在位，
// 使追加「需要 application service 的 worker」是纯追加式改动，无需改签名。
//
// 集合为空不意味着 worker 机制多余——worker.go 的 WorkerManager 配合 app.go 的
// 「启动 workers → HTTP、关停 HTTP → workers」反序编排，保证 HTTP 优雅关停期间
// 在飞请求写入的任务仍能被消费。该语义由 bootstrap-module-assembly spec 约束。
func buildWorkers(_ services) *WorkerManager {
	return NewWorkerManager(
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

func initLegacyRuntime(cfg *config.Config, opts Options) error {
	applyRuntimeTimezone(cfg)
	initLogWithConfig(cfg)
	initNewsWithConfig(cfg)
	initFileWithConfig(cfg)
	initDAOWithConfig(cfg, resolveDBCredentials(cfg, opts))
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

func closeLegacyRuntime() {
	legacylog.SyncAccessLog()
	legacydao.DBClose()
	logger.Sync()
}
