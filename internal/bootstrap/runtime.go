package bootstrap

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/pkg/httpcache"
	"github.com/gtkit/gin-api/internal/pkg/jwtauth"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/platform/data"

	"github.com/gtkit/logger/v2"
)

// Runtime represents process-wide runtime dependencies.
//
// 字段全部私有：连接句柄一旦能被外部取走并各自持有，"谁负责关闭"就不再有唯一答案，
// 而这正是资源泄漏的来源。
type Runtime struct {
	config    *config.Config
	resources *data.Resources
	owners    *runtimeResources
	cache     httpcache.ResponseCache
	// jwt 是轮换密钥体系（签发器 + 验签器 + 密钥集）。
	jwt *jwtauth.Assembly
	// certificate is validated before resources open and reused by HTTP.
	certificate *tls.Certificate
	// Abnormal file flush and normal Close share the native logging owner.
	drain        func(context.Context) error
	force        <-chan struct{}
	drainCleanup onceCleanup
}

// NewRuntime initializes the fixed process resources. Failure cleanup
// is independent of startup cancellation and has one shared 30 second budget.
func NewRuntime(ctx context.Context, opts Options) (*Runtime, error) {
	budget := &rollbackBudget{force: opts.forceSignal()}
	defer budget.Close()
	return newRuntime(ctx, opts, budget)
}

func newRuntime(ctx context.Context, opts Options, budget *rollbackBudget) (_ *Runtime, err error) {
	ctx, finish := forceContext(ctx, opts.forceSignal())
	defer finish()
	ctx, cancel := context.WithTimeout(ctx, runtimeStartupTimeout)
	defer cancel()
	factories := defaultRuntimeFactories()
	if opts.factories != nil {
		factories = *opts.factories
	}
	cfg, err := openResource(ctx, "configuration", factories.loadConfig)
	if err != nil {
		return nil, err
	}
	ApplyRuntimeTimezone(cfg)
	jwtAssembly, err := initJWTWithConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("init jwt: %w", err)
	}
	if jwtAssembly == nil {
		return nil, errors.New("jwt assembly is nil")
	}
	var certificate *tls.Certificate
	if cfg.App.EnableHTTPS {
		certificate, err = loadAndVerifyCertificate(newHTTPConfig(cfg))
		if err != nil {
			return nil, fmt.Errorf("init tls: %w", err)
		}
	}
	owners, err := openRuntimeResources(ctx, cfg, factories, false, budget)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, owners.Close(budget.Context(ctx)))
		}
	}()
	if err := initProcessRuntime(cfg); err != nil {
		return nil, err
	}
	resources, err := data.ResourcesFromClients(owners.database, owners.redis, owners.redis.Configured())
	if err != nil {
		return nil, fmt.Errorf("build resource view: %w", err)
	}
	logResourceStates(resources)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	logger.Infof("jwt active kid=%s, previous keys=%d", jwtAssembly.KeySet.ActiveKID(), len(cfg.JWT.Previous))
	return &Runtime{config: cfg, resources: resources, owners: owners, cache: owners.cache,
		jwt: jwtAssembly, certificate: certificate, drain: owners.logs.Drain, force: opts.forceSignal()}, nil
}

// logResourceStates 在启动完成时把非健康的资源状态打印一次。
//
// optional 资源启动失败只降级、不阻断启动。若不在此明说，
// "某个能力从进程启动起就没工作过"会长期无人察觉——
// 而它表现出来的症状（某类请求一直失败）看起来像业务 bug。
func logResourceStates(resources *data.Resources) {
	if resources == nil || !resources.RedisConfigured() {
		return
	}
	state := resources.RedisAvailability()
	if state == nil || state.State() == data.AvailabilityHealthy {
		return
	}
	logger.Warnf("resource %s is %s at startup: capabilities depending on it stay unavailable "+
		"until Redis recovery and their own health checks succeed", state.Name(), state.State())
}

// Config returns the typed runtime configuration.
func (r *Runtime) Config() *config.Config {
	if r == nil {
		return nil
	}
	return r.config
}

// Resources returns the process-wide infrastructure connections.
func (r *Runtime) Resources() *data.Resources {
	if r == nil {
		return nil
	}
	return r.resources
}

// Cache returns the borrowed response cache used by explicit route assembly.
func (r *Runtime) Cache() httpcache.ResponseCache {
	if r == nil {
		return nil
	}
	return r.cache
}

// TokenIssuer 返回轮换签发器。
func (r *Runtime) TokenIssuer() jwtauth.RotatingTokenIssuer {
	if r == nil || r.jwt == nil || r.jwt.Issuer == nil {
		return nil
	}
	return r.jwt.Issuer
}

// TokenParser 返回轮换验签器。
func (r *Runtime) TokenParser() jwtauth.TokenParser {
	if r == nil || r.jwt == nil {
		return nil
	}
	return r.jwt.Parser
}

// Close releases resource owners once. It preserves caller cancellation and
// deadlines; only the process entry point may detach a canceled run context.
func (r *Runtime) Close(ctx context.Context) error {
	if r == nil || r.owners == nil {
		return nil
	}
	if err := r.owners.Close(ctx); err != nil {
		return fmt.Errorf("close runtime: %w", err)
	}
	return nil
}

// DrainLogs flushes file buffers without closing SDK instances or notification
// queues when producers prevent resource shutdown. Concurrent callers share the
// result; this does not consume the owner's subsequent normal Close.
func (r *Runtime) DrainLogs(ctx context.Context) error {
	if r == nil || r.drain == nil {
		return nil
	}
	return r.drainCleanup.do(ctx, r.force, func(drainCtx context.Context) error {
		err := func() (err error) {
			defer func() {
				if recovered := recover(); recovered != nil {
					err = fmt.Errorf("logging drain panic: %v: %w", recovered, core.ErrComponentState)
				}
			}()
			return r.drain(drainCtx)
		}()
		return errors.Join(err, drainCtx.Err())
	})
}
