package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	jwtauth "github.com/gtkit/gin-api/internal/pkg/jwtauth"
	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"

	logger "github.com/gtkit/logger/v2"
)

// Runtime represents process-wide runtime dependencies.
//
// 字段全部私有：连接句柄一旦能被外部取走并各自持有，"谁负责关闭"就不再有唯一答案，
// 而这正是资源泄漏的来源。
type Runtime struct {
	config    *config.Config
	resources *data.Resources
	// jwt 是轮换密钥体系（签发器 + 验签器 + 密钥集）。
	jwt *jwtauth.Assembly
	// drained 保证日志排空恰好一次。它**只管排空**，与连接关闭分开计数：
	// 两者共用一个开关时，"资源不能关"会顺带把进程终止报告一起丢掉（见 DrainLogs）。
	drained atomic.Bool
	// drainInterrupt 是日志排空的提前放弃信号（强制关停的 ForcedCh）。
	// 由 App 在进入请求并发阶段之前安装一次；nil 表示排空只受预算约束。
	drainInterrupt <-chan struct{}
}

// SetDrainInterrupt 安装日志排空的提前放弃信号。
//
// 只允许在进程装配阶段调用一次：Close 与本方法没有并发保护，
// 进入请求并发阶段后再改动就是数据竞争。
func (r *Runtime) SetDrainInterrupt(ch <-chan struct{}) {
	if r != nil {
		r.drainInterrupt = ch
	}
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

	// 装配期的排空同样受第二个信号约束：信号可能在装配途中到达，而这条
	// 路径此前固定传 nil——"再按一次立刻退出"在启动失败时不成立。
	interrupt := opts.forceSignal()

	if err := initProcessRuntime(cfg); err != nil {
		return nil, err
	}

	resources, err := data.NewResources(
		ctx, cfg, resolveDBCredentials(cfg, opts), resourceRequirements(cfg), data.DefaultBudget(),
	)
	if err != nil {
		closeProcessRuntime(interrupt)
		return nil, fmt.Errorf("open resources: %w", err)
	}

	logResourceStates(resources)

	// JWT 轮换体系只在此处装配一次。密钥集在进程生命周期内不可变——
	// 热重载需要原子替换 parser 与其缓存，否则会出现"缓存里的结果由一把
	// 已经被移除的密钥验出"这种无法解释的状态。轮换通过重启完成。
	jwtAssembly, err := initJWTWithConfig(cfg)
	if err != nil {
		return nil, closeRuntimeOnFailure(resources, interrupt, fmt.Errorf("init jwt: %w", err))
	}
	if jwtAssembly == nil {
		return nil, closeRuntimeOnFailure(resources, interrupt, errors.New("jwt assembly is nil"))
	}
	logger.Infof("jwt active kid=%s, previous keys=%d",
		jwtAssembly.KeySet.ActiveKID(), len(cfg.JWT.Previous))

	return &Runtime{
		config:         cfg,
		resources:      resources,
		jwt:            jwtAssembly,
		drainInterrupt: interrupt,
	}, nil
}

// closeRuntimeOnFailure 在装配后段失败时清理已建立的资源，并把清理错误附加到根因上。
//
// 根因用 %w 保留可 errors.Is/As，清理错误只作附加信息——
// 反过来会让调用方拿不到真正的失败原因。
func closeRuntimeOnFailure(
	resources *data.Resources, interrupt <-chan struct{}, cause error,
) error {
	closeProcessRuntime(interrupt)
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
func logResourceStates(resources *data.Resources) {
	if resources == nil || !resources.RedisConfigured() {
		return
	}
	state := resources.RedisAvailability()
	if state == nil || state.State() == data.AvailabilityHealthy {
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
func (r *Runtime) Resources() *data.Resources {
	if r == nil {
		return nil
	}
	return r.resources
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

// Close releases runtime resources and reports failures to the caller.
//
// 关闭错误必须返回而不是只记日志：吞掉它会让进程在资源关闭失败时仍报告
// "正常退出"，编排器与脚本据此做出错误判断。重复调用返回与首次相同的结果，
// 幂等由 Resources 自身保证（其内部记录首次关闭结果）；日志刷写仅执行一次。
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}

	var err error
	if r.resources != nil {
		err = r.resources.Close()
	}
	r.DrainLogs()
	if err != nil {
		return fmt.Errorf("close resources: %w", err)
	}
	return nil
}

// DrainLogs 刷写进程级日志与告警队列。幂等，可独立于连接关闭调用。
//
// **与 Close 分开是必需的**：连接与进程终止报告是两件事，共用一个开关时
// "现在不能关连接"会顺带把退出告警与错误日志一起丢掉。存活 worker 手里的
// 连接确实不能关（等价于使用中拔资源），但那与"把已经记下的日志送出去"
// 毫无关系——而后者恰恰是排查那次异常退出的唯一线索。
func (r *Runtime) DrainLogs() {
	if r == nil {
		return
	}
	if r.drained.CompareAndSwap(false, true) {
		closeProcessRuntime(r.drainInterrupt)
	}
}
