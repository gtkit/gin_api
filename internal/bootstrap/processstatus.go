package bootstrap

import (
	"slices"

	"github.com/gtkit/gin-api/internal/router"
)

// processStatus 把关停协调器与 worker 管理器的状态翻译成 readiness 的输入。
//
// readiness 不只是依赖存活：一个正在 draining 的实例、一个 required worker 已经
// 异常退出的实例，DB 与 Redis 可能全都健康，但它已经不该再接流量。
type processStatus struct {
	coordinator *ShutdownCoordinator
	workers     *WorkerManager
}

var _ router.ProcessStatus = (*processStatus)(nil)

func newProcessStatus(coordinator *ShutdownCoordinator, workers *WorkerManager) router.ProcessStatus {
	return &processStatus{coordinator: coordinator, workers: workers}
}

// Draining 报告进程是否已进入优雅关停。
func (s *processStatus) Draining() bool {
	return s.coordinator.Phase() == PhaseDraining
}

// DegradedCapabilities 返回已降级的**可选** worker 名，按名称稳定排序。
//
// 稳定排序是必需的：这个列表进 readiness 响应体，顺序抖动会干扰比对与告警去重。
func (s *processStatus) DegradedCapabilities() []string {
	return s.workerNames(false)
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
