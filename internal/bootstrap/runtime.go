package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	gojwt "github.com/gtkit/gojwt"

	"github.com/gtkit/gin-api/internal/platform/config"
	legacydao "github.com/gtkit/gin-api/internal/platform/data"

	logger "github.com/gtkit/logger/v2"
)

// Runtime represents process-wide runtime dependencies.
//
// 字段全部私有：连接句柄一旦能被外部取走并各自持有，"谁负责关闭"就不再有唯一答案，
// 而这正是资源泄漏的来源。
type Runtime struct {
	config      *config.Config
	resources   *legacydao.Resources
	tokenIssuer *gojwt.JwtEd25519
	closed      atomic.Bool
}

// NewRuntime builds runtime dependencies that can be reused by commands.
//
// ctx 约束资源建立的总时长：没有它，一个可路由但无响应的数据库地址会让启动无限期挂起，
// 而编排器的启动探针早已超时并重启容器——形成重启循环。
//
// 任一步失败都在本函数内清理已建立的部分：调用方拿到 nil，没有可关闭的句柄。
func NewRuntime(ctx context.Context, opts Options) (*Runtime, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	if err := initProcessRuntime(cfg); err != nil {
		return nil, err
	}

	resources, err := legacydao.NewResources(
		ctx, cfg, resolveDBCredentials(cfg, opts), resourceRequirements(cfg), legacydao.DefaultBudget(),
	)
	if err != nil {
		closeProcessRuntime()
		return nil, fmt.Errorf("open resources: %w", err)
	}

	logResourceStates(resources)

	// JWT issuer 只在此处创建一次（旧实现预检创建一份又丢弃、随后再重建一份，属冗余）。
	tokenIssuer, err := initJWTWithConfig(cfg)
	if err != nil {
		return nil, closeRuntimeOnFailure(resources, fmt.Errorf("init jwt: %w", err))
	}
	if tokenIssuer == nil {
		return nil, closeRuntimeOnFailure(resources, errors.New("jwt issuer is nil"))
	}

	return &Runtime{
		config:      cfg,
		resources:   resources,
		tokenIssuer: tokenIssuer,
	}, nil
}

// closeRuntimeOnFailure 在装配后段失败时清理已建立的资源，并把清理错误附加到根因上。
//
// 根因用 %w 保留可 errors.Is/As，清理错误只作附加信息——
// 反过来会让调用方拿不到真正的失败原因。
func closeRuntimeOnFailure(resources *legacydao.Resources, cause error) error {
	closeProcessRuntime()
	if err := resources.Close(); err != nil {
		return errors.Join(cause, fmt.Errorf("close resources after failure: %w", err))
	}
	return cause
}

// logResourceStates 在启动完成时把非健康的资源状态打印一次。
//
// optional 资源启动失败只降级、不阻断启动。若不在此明说，
// "某个能力从进程启动起就没工作过"会长期无人察觉——
// 而它表现出来的症状（某类请求一直失败）看起来像业务 bug。
func logResourceStates(resources *legacydao.Resources) {
	state := resources.RedisAvailability()
	if state == nil || state.State() == legacydao.AvailabilityHealthy {
		return
	}
	logger.Warnf("resource %s is %s at startup: capabilities depending on it stay unavailable "+
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
func (r *Runtime) Resources() *legacydao.Resources {
	if r == nil {
		return nil
	}
	return r.resources
}

// TokenIssuer returns the initialized JWT issuer for explicit injection.
func (r *Runtime) TokenIssuer() *gojwt.JwtEd25519 {
	if r == nil {
		return nil
	}

	return r.tokenIssuer
}

// Close releases runtime resources exactly once.
func (r *Runtime) Close() {
	if r == nil || !r.closed.CompareAndSwap(false, true) {
		return
	}

	if err := r.resources.Close(); err != nil {
		logger.Errorf("close resources: %v", err)
	}
	closeProcessRuntime()
}
