package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/pkg/jwtauth"
	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/gin-api/internal/plugin"

	applog "github.com/gtkit/gin-api/internal/pkg/log"
)

// Runtime represents process-wide runtime dependencies.
//
// 字段全部私有：连接句柄一旦能被外部取走并各自持有，"谁负责关闭"就不再有唯一答案，
// 而这正是资源泄漏的来源。
type Runtime struct {
	config     *config.Config
	resources  *data.Resources
	plugins    *core.PluginRegistry
	cache      plugin.ResponseCache
	middleware plugin.MiddlewareBuilder
	// jwt 是轮换密钥体系（签发器 + 验签器 + 密钥集）。
	jwt *jwtauth.Assembly
	// The isolated drain and normal Stop share the selected logging owner.
	drain     func(context.Context) error
	force     <-chan struct{}
	drainMu   sync.Mutex
	drainDone chan struct{}
	drainErr  error
}

// NewRuntime initializes only the explicitly registered plugins. Failure cleanup
// is independent of startup cancellation and has one shared 30 second budget.
func NewRuntime(ctx context.Context, opts Options) (*Runtime, error) {
	budget := &rollbackBudget{force: opts.forceSignal()}
	defer budget.Close()
	return newRuntime(ctx, opts, budget)
}

func newRuntime(ctx context.Context, opts Options, budget *rollbackBudget) (_ *Runtime, err error) {
	if opts.RegisterPlugins == nil {
		return nil, errors.New("register plugins: registrar is required")
	}
	registry := core.NewPluginRegistry()
	if err := registry.SetForceSignal(opts.forceSignal()); err != nil {
		return nil, err
	}
	set, err := opts.RegisterPlugins(registry)
	if err != nil {
		return nil, fmt.Errorf("register plugins: %w", err)
	}
	if err := set.validate(); err != nil {
		return nil, err
	}
	// Core owns partial Init/Start rollback as well as normal stopping.
	if err := registry.Init(ctx); err != nil {
		return nil, fmt.Errorf("init plugins: %w", err)
	}
	if err := registry.Start(ctx); err != nil {
		return nil, fmt.Errorf("start plugins: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, registry.Stop(budget.Context(ctx)))
		}
	}()
	cfg, err := set.Configuration.Value()
	if err != nil {
		return nil, fmt.Errorf("read runtime configuration: %w", err)
	}
	if cfg == nil {
		return nil, errors.New("runtime configuration is nil")
	}
	if err := initProcessRuntime(cfg); err != nil {
		return nil, err
	}
	resources, err := data.ResourcesFromClients(set.Database, set.Redis, set.Redis.Configured())
	if err != nil {
		return nil, fmt.Errorf("build resource view: %w", err)
	}
	logResourceStates(resources)
	jwtAssembly, err := initJWTWithConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("init jwt: %w", err)
	}
	if jwtAssembly == nil {
		return nil, errors.New("jwt assembly is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	applog.Infof("jwt active kid=%s, previous keys=%d", jwtAssembly.KeySet.ActiveKID(), len(cfg.JWT.Previous))
	return &Runtime{
		config: cfg, resources: resources, plugins: registry, cache: set.Cache,
		middleware: set.Middleware, jwt: jwtAssembly, drain: set.DrainLogs, force: opts.forceSignal(),
	}, nil
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
	applog.Warnf("resource %s is %s at startup: capabilities depending on it stay unavailable "+
		"until the process restarts", state.Name(), state.State())
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

// CachePlugin returns the response-cache lifecycle adapter for provider assembly.
func (r *Runtime) CachePlugin() plugin.ResponseCache {
	if r == nil {
		return nil
	}
	return r.cache
}

// TokenIssuer 返回轮换签发器。
func (r *Runtime) TokenIssuer() jwtauth.RotatingTokenIssuer {
	if r == nil || r.jwt == nil {
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

// Close stops the registered owners once. It preserves caller cancellation and
// deadlines; only the process entry point may detach a canceled run context.
func (r *Runtime) Close(ctx context.Context) error {
	if r == nil || r.plugins == nil {
		return nil
	}
	if err := r.plugins.Stop(ctx); err != nil {
		return fmt.Errorf("close runtime: %w", err)
	}
	return nil
}

// DrainLogs drains the selected logging owner when live workers prevent stopping
// shared resources. Concurrent callers share the result; a waiter can cancel its
// own wait without starting a second drain. SDK calls still require cooperation.
func (r *Runtime) DrainLogs(ctx context.Context) error {
	if r == nil || r.drain == nil {
		return nil
	}
	r.drainMu.Lock()
	if done := r.drainDone; done != nil {
		r.drainMu.Unlock()
		select {
		case <-done:
			return r.drainErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r.drainDone = make(chan struct{})
	done := r.drainDone
	r.drainMu.Unlock()
	drainCtx, finish := cleanupContext(ctx, r.force)
	defer finish()
	err := func() (err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("logging drain panic: %v: %w", recovered, core.ErrPluginContractViolation)
			}
		}()
		return r.drain(drainCtx)
	}()
	err = errors.Join(err, drainCtx.Err())
	r.drainMu.Lock()
	r.drainErr = err
	close(done)
	r.drainMu.Unlock()
	return err
}
