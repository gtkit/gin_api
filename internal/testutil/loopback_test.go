package testutil

import "testing"

// TestSkipReason 钉住受限判定的两条来源。
//
// 测纯函数而不是 RequireLoopback 本身：后者一旦判定跳过就会终止调用它的
// 测试，断言执行不到，测试以 SKIP 结束而看起来"通过"——那是一个永远不会
// 失败、也就永远证明不了什么的测试。
func TestSkipReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		flag       string
		canBind    bool
		wantSkip   bool
		wantProbed bool
	}{
		{
			// 关键用例：标志置位时即使机器能绑端口也要跳过。
			// 这正是让受限路径在开发机上可复现的那一条——没有它，
			// 受限环境的覆盖率数字永远无法被验证，只能被声称。
			name: "the flag skips even when the machine can bind",
			flag: "1", canBind: true, wantSkip: true, wantProbed: false,
		},
		{
			// 环境变量会说谎：没设标志但环境确实受限时，必须跳过而不是失败。
			name: "a restricted environment without the flag still skips",
			flag: "", canBind: false, wantSkip: true, wantProbed: true,
		},
		{
			name: "a normal environment runs the test",
			flag: "", canBind: true, wantSkip: false, wantProbed: true,
		},
		{
			name: "any value other than 1 is not the flag",
			flag: "0", canBind: true, wantSkip: false, wantProbed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var probed bool
			probe := func() bool {
				probed = true
				return tt.canBind
			}

			reason, skip := skipReason(tt.flag, probe)
			if skip != tt.wantSkip {
				t.Fatalf("skip = %v, want %v", skip, tt.wantSkip)
			}
			if skip && reason == "" {
				t.Fatal("a skip must carry a reason, otherwise the run log says nothing")
			}
			if !skip && reason != "" {
				t.Fatalf("reason = %q, want empty when not skipping", reason)
			}
			if probed != tt.wantProbed {
				t.Fatalf("probed = %v, want %v (the flag must short-circuit the probe)",
					probed, tt.wantProbed)
			}
		})
	}
}

// TestLoopbackUsableIsCached 验证探测只做一次。
//
// 逐个测试探测会在受限环境里产生几十次注定失败的 syscall，
// 而结果对同一个进程不会变。
func TestLoopbackUsableIsCached(t *testing.T) {
	t.Parallel()

	first := loopbackUsable()
	for range 5 {
		if loopbackUsable() != first {
			t.Fatal("loopbackUsable changed its answer within one process")
		}
	}
}
