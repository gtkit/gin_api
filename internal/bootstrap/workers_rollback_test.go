package bootstrap

import (
	"reflect"
	"testing"
)

// TestNewWorkerManagerOwningCleanupsRollsBackOnFailure 钉住装配副作用的所有权
// （bootstrap-module-assembly spec）：buildWorkers 窗口内注册的指标回调在
// WorkerManager 构造失败时由本函数自身逆序撤销并截断——调用方按"成功后
// 登记回滚"工作，这个窗口只有构造者自己兜得住。
func TestNewWorkerManagerOwningCleanupsRollsBackOnFailure(t *testing.T) {
	t.Run("构造失败时逆序撤销本段新增回调并截断", func(t *testing.T) {
		var calls []string
		inf := &infra{metricsCleanup: []func() error{
			func() error { calls = append(calls, "pre-existing"); return nil },
		}}
		cleanupsBefore := len(inf.metricsCleanup)
		inf.metricsCleanup = append(inf.metricsCleanup,
			func() error { calls = append(calls, "added-1"); return nil },
			func() error { calls = append(calls, "added-2"); return nil },
		)

		// 零值声明非法（类别/生命周期未声明），构造必然失败。
		manager, err := newWorkerManagerOwningCleanups(inf, cleanupsBefore, &funcWorker{name: "invalid"})
		if err == nil || manager != nil {
			t.Fatalf("got (%v, %v), want construction failure", manager, err)
		}

		if want := []string{"added-2", "added-1"}; !reflect.DeepEqual(calls, want) {
			t.Fatalf("rollback calls = %v, want %v (逆序、且不触碰既有回调)", calls, want)
		}
		if len(inf.metricsCleanup) != cleanupsBefore {
			t.Fatalf("metricsCleanup len = %d, want %d (截断到进入前)", len(inf.metricsCleanup), cleanupsBefore)
		}
	})

	t.Run("构造成功时不触碰任何回调", func(t *testing.T) {
		var calls []string
		inf := &infra{metricsCleanup: []func() error{
			func() error { calls = append(calls, "pre-existing"); return nil },
		}}
		cleanupsBefore := len(inf.metricsCleanup)
		inf.metricsCleanup = append(inf.metricsCleanup,
			func() error { calls = append(calls, "added-1"); return nil },
		)

		manager, err := newWorkerManagerOwningCleanups(inf, cleanupsBefore, &funcWorker{
			name: "valid", kind: KindConsumer, lifecycle: LifecycleLongRunning, required: false,
		})
		if err != nil || manager == nil {
			t.Fatalf("got (%v, %v), want success", manager, err)
		}
		if len(calls) != 0 {
			t.Fatalf("cleanups were invoked on success: %v", calls)
		}
		if len(inf.metricsCleanup) != cleanupsBefore+1 {
			t.Fatalf("metricsCleanup len = %d, want %d (成功路径保留新增回调)", len(inf.metricsCleanup), cleanupsBefore+1)
		}
	})
}
