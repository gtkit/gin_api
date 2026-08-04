package bootstrap

import (
	"slices"
	"sync/atomic"

	"github.com/gtkit/gin-api/internal/router"
)

// processStatus 把关停协调器与 worker 管理器的状态翻译成 readiness 的输入。
//
// readiness 不只是依赖存活：一个正在 draining 的实例、一个 required worker 已经
// 异常退出的实例，DB 与 Redis 可能全都健康，但它已经不该再接流量。
type processStatus struct {
	coordinator *ShutdownCoordinator
	workers     *WorkerManager
	// pprof 是可选的调试端点；异常退出时它要出现在降级列表里。
	//
	// **原子指针且后置注入**：pprof 在 App.Run 里才启动，晚于本结构构造；
	// 而 readiness 从每个探针请求并发读取它。用原子指针让写入一次、读取多次
	// 无需加锁，也不必让整个装配顺序为它让路。
	//
	// 未启用 pprof 时保持 nil。不上报的话，一个已经不存在的端点不会留下任何
	// 痕迹——运维要用它时才发现早已退出，而那时无从判断退出时间与原因。
	pprof atomic.Pointer[pprofHandle]
}

var _ router.ProcessStatus = (*processStatus)(nil)

func newProcessStatus(coordinator *ShutdownCoordinator, workers *WorkerManager) *processStatus {
	return &processStatus{coordinator: coordinator, workers: workers}
}

// attachPprof 在 pprof 端点启动后登记它，供 readiness 上报降级。
//
// 允许 h 为 nil（未启用 pprof）：那时 DegradedCapabilities 不会提到它。
//
// 也允许**接收者**为 nil：直接构造 App 的测试不经过装配层，因而没有
// processStatus。让登记在那种场景下静默跳过，好过要求每个测试都去构造一个
// 它并不关心的依赖——那种要求最终会让人给 App 加一个"仅测试用"的构造函数，
// 而那才是真正会漂移的东西。
func (s *processStatus) attachPprof(h *pprofHandle) {
	if s == nil {
		return
	}
	s.pprof.Store(h)
}

// Draining 报告进程是否已进入优雅关停。
func (s *processStatus) Draining() bool {
	return s.coordinator.Phase() == PhaseDraining
}

// DegradedCapabilities 返回已降级的**可选** worker 名，按名称稳定排序。
//
// 稳定排序是必需的：这个列表进 readiness 响应体，顺序抖动会干扰比对与告警去重。
func (s *processStatus) DegradedCapabilities() []string {
	degraded := s.workerNames(false)
	// pprof 异常退出是**降级**而不是不可用：调试端点没了不该让实例被摘流，
	// 那会把一次端点意外退出放大成一次容量损失。但必须报出来。
	if s.pprof.Load().Exited() {
		degraded = append(degraded, "pprof")
		slices.Sort(degraded)
	}
	return degraded
}

// FailedRequirements 返回已失效的**必需** worker 名，按名称稳定排序。
//
// required worker 的运行期异常同时会触发全进程关停，但两者之间存在一个窗口：
// 关停已被请求、尚未走到 draining 相位。这个列表让 readiness 在那个窗口里
// 也能立刻报告不可用。
func (s *processStatus) FailedRequirements() []string {
	return s.workerNames(true)
}

// workerNames 按 required 过滤已降级/已失效的 worker 名。
func (s *processStatus) workerNames(required bool) []string {
	degraded := s.workers.Degraded()
	if len(degraded) == 0 {
		return nil
	}

	// 从 worker 声明反查 required，而不是在 degraded 表里另存一份：
	// 存两份就会有两份走样的机会。
	requiredByName := make(map[string]bool, len(s.workers.workers))
	for _, w := range s.workers.workers {
		requiredByName[w.Name()] = w.Required()
	}

	names := make([]string, 0, len(degraded))
	for name := range degraded {
		if requiredByName[name] == required {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}
