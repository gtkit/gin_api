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
	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/gin-api/internal/platform/telemetry"
)

// TASKQUEUE: 启用可选队列时取消下一行 import 声明和 infra.tasksProducer 字段的注释。
// wiring:begin providers.taskqueue.import taskqueue line
// import "github.com/gtkit/gin-api/internal/optional/taskqueue"
// wiring:end providers.taskqueue.import

// UPLOAD: 启用本地上传时取消下面两个 import 声明及 infra.uploader 字段的注释。
// wiring:begin providers.upload.import upload line
// import "github.com/gtkit/gin-api/internal/optional/upload"
// import "github.com/gtkit/gouploader"
// wiring:end providers.upload.import

// OUTBOUND: 恢复出站 HTTP 接线时取消此 import 与 infra.outbound 字段的注释。
// wiring:begin providers.outbound.import outbound line
// import "github.com/gtkit/gin-api/internal/optional/outbound"
// wiring:end providers.outbound.import

// AUDITLOG: 恢复审计接线时取消此 import 与 infra.auditRecorder 字段的注释。
// wiring:begin providers.auditlog.import auditlog line
// import "github.com/gtkit/gin-api/internal/optional/auditlog"
// wiring:end providers.auditlog.import

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
	// OUTBOUND: 具名出站客户端、重试与熔断，供业务服务显式注入。
	// wiring:begin providers.outbound.field outbound line
	// outbound *outbound.Downstreams
	// wiring:end providers.outbound.field
	// AUDITLOG: 审计记录器，Run 由 consumer worker 管理。
	// wiring:begin providers.auditlog.field auditlog line
	// auditRecorder *auditlog.Recorder
	// wiring:end providers.auditlog.field
	// cache 是 GET 响应缓存装配器；关闭时它产出透传中间件。
	cache httpcache.ResponseCache
	// telemetry 是 OTel 导出侧；关停时按独立预算 flush。
	telemetry *telemetry.Provider
	// TASKQUEUE: 供业务 service 显式注入的入队端，启用时取消注释。
	// wiring:begin providers.taskqueue.field taskqueue line
	// tasksProducer *taskqueue.Producer
	// wiring:end providers.taskqueue.field
	// UPLOAD: 由业务服务共享的上传器，关闭由 providerCleanup 负责。
	// wiring:begin providers.upload.field upload line
	// uploader *gouploader.Uploader
	// wiring:end providers.upload.field
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

	// Runtime 已在任何 HTTP 绑定前通过 initProcessRuntime 完成校验器初始化。

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

	// OUTBOUND: 先恢复 outbound.go 及配置接线，再取消下面调用的注释。
	// wiring:begin providers.outbound.call outbound line
	// if err := buildOutbound(&inf, cleanup); err != nil {
	// 	return nil, err
	// }
	// wiring:end providers.outbound.call

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

	// TASKQUEUE: 启用时先恢复 taskqueue.go 的实现，再取消下面调用的注释。
	// 必须位于 buildServices 之前，业务 service 才能通过 inf.tasksProducer 接收依赖。
	// wiring:begin providers.taskqueue.call taskqueue line
	// if err := buildTaskQueueProducer(&inf, cleanup); err != nil {
	// 	return nil, err
	// }
	// wiring:end providers.taskqueue.call

	// UPLOAD: 先恢复 upload.go，再按实际部署填写目录、大小上限和类型白名单。
	// 这组值仅为接线示例，两个存储目录均应由部署显式确定。
	// wiring:begin providers.upload.call upload line
	// uploadOptions := upload.LocalOptions{
	// 	Dir:          "./storage/uploads",
	// 	MaxFileSize:  16 << 20,
	// 	AllowedTypes: []string{".jpg", ".png", ".pdf"},
	// }
	// if err := buildLocalUploader(&inf, cleanup, uploadOptions,
	// 	gouploader.WithChunkTempDir("./storage/upload-chunks"),
	// ); err != nil {
	// 	return nil, err
	// }
	// wiring:end providers.upload.call

	// AUDITLOG: 恢复 auditlog.go 后取消注释；记录器不在此启动 goroutine。
	// wiring:begin providers.auditlog.call auditlog line
	// if err := buildAuditLog(&inf); err != nil {
	// 	return nil, err
	// }
	// wiring:end providers.auditlog.call

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
