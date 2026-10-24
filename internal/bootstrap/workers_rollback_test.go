package bootstrap

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// TestRollbackWorkerMetricCleanups 钉住装配副作用的所有权
// （bootstrap-module-assembly spec）：buildWorkers 窗口内注册的指标回调在
// 任意错误出口上由本函数逆序撤销并截断——调用方按"成功后登记回滚"工作，
// 这个窗口只有构造者自己兜得住。
func TestRollbackWorkerMetricCleanups(t *testing.T) {
	t.Run("逆序撤销本段新增回调并截断", func(t *testing.T) {
		var calls []string
		inf := &infra{metricsCleanup: []func() error{
			func() error { calls = append(calls, "pre-existing"); return nil },
		}}
		cleanupsBefore := len(inf.metricsCleanup)
		inf.metricsCleanup = append(inf.metricsCleanup,
			func() error { calls = append(calls, "added-1"); return nil },
			func() error { calls = append(calls, "added-2"); return nil },
		)

		if err := rollbackWorkerMetricCleanups(inf, cleanupsBefore); err != nil {
			t.Fatalf("rollback returned %v, want nil", err)
		}

		if want := []string{"added-2", "added-1"}; !slices.Equal(calls, want) {
			t.Fatalf("rollback calls = %v, want %v (逆序、且不触碰既有回调)", calls, want)
		}
		if len(inf.metricsCleanup) != cleanupsBefore {
			t.Fatalf("metricsCleanup len = %d, want %d (截断到进入前)", len(inf.metricsCleanup), cleanupsBefore)
		}
	})

	t.Run("撤销失败聚合上报且仍截断", func(t *testing.T) {
		inf := &infra{}
		inf.metricsCleanup = append(inf.metricsCleanup,
			func() error { return errors.New("unregister boom") },
		)

		err := rollbackWorkerMetricCleanups(inf, 0)
		if err == nil || !strings.Contains(err.Error(), "unregister boom") {
			t.Fatalf("rollback error = %v, want it to carry the unregister failure", err)
		}
		if len(inf.metricsCleanup) != 0 {
			t.Fatalf("metricsCleanup len = %d, want 0", len(inf.metricsCleanup))
		}
	})
}
