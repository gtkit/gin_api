// 进程级资源与启动阶段编排；模块服务、路由和后台任务装配见 modules.go。
package bootstrap

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"

	"github.com/gtkit/logger/v2"
	"go.opentelemetry.io/otel"
	"gorm.io/gorm"

	"github.com/gtkit/gin-api/internal/pkg/httpcache"
	"github.com/gtkit/gin-api/internal/pkg/httpclient"
	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/internal/pkg/tasks"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/gin-api/internal/platform/telemetry"
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

func newProvidersWithBudget(
	ctx context.Context, runtime *Runtime, shutdown *ShutdownCoordinator, budget *rollbackBudget,
) (_ *providerSet, err error) {
	// 失败时逆序撤销已登记的副作用；成功时把同一份清理所有权交给 App。
	cleanup := &providerCleanup{}
	defer func() {
		if err != nil {
			err = errors.Join(err, cleanup.rollback(budget.Context(ctx)))
		}
	}()

	// 0. 首次绑定前初始化翻译器，避免 validator 缓存未经翻译的字段元数据。
	if err := initValidation(); err != nil {
		return nil, err
	}

	// 1. 基础设施视图：从 Runtime 借用已初始化的配置、数据库与日志，
	//    并据此派生 HTTP 配置。连接的关闭权仍属于 Runtime，
	//    这里不重新创建连接，也不接管它们的生命周期。
	inf, err := resolveInfra(runtime)
	if err != nil {
		return nil, err
	}

	// 2a. 遥测先于指标仪表：Recorder 在构造时绑定全局 MeterProvider。
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

	// 2a'. telemetry.New 之后安装数据层追踪，失败即终止装配。
	//      可恢复 Redis Owner 也在此获知 tracing 就绪；恢复候选须先完成埋点再发布。
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

	// 3b. 先构造具名出站客户端与熔断实例，供 service 按名取用；此处不做 I/O。
	outbound, err := httpclient.NewDownstreams(inf.cfg.Outbound)
	if err != nil {
		return nil, fmt.Errorf("build outbound downstreams: %w", err)
	}
	inf.outbound = outbound
	cleanup.push("outbound http clients", cleanupExtensions, func(context.Context) error { return outbound.Close() })
	if names := outbound.Names(); len(names) > 0 {
		logger.Infof("outbound downstreams assembled with circuit breakers: %v", names)
	}

	// 3c. 借用 Runtime 所有的响应缓存视图，不重复创建或关闭 Store。
	//     Redis 恢复后既有中间件延迟接入 Store，Enabled 仅用于报告当前状态。
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
	svc, err := buildServices(ctx, inf, runtime)
	if err != nil {
		return nil, err
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
	httpConfig := newHTTPConfig(cfg)
	if runtime.certificate != nil {
		httpConfig.TLSConfig.Certificates = []tls.Certificate{*runtime.certificate}
	}
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
		httpConfig:   httpConfig,
	}, nil
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
