package ratelimit

import (
	"net"
	"os"
	"strings"
	"sync"
	"testing"
)

// noLoopbackEnv 是强制声明"本环境禁止绑定回环端口"的标志。
//
// 与仓库内 internal/testutil 用的是同一个名字：受限环境的判定必须全仓同口径，
// 否则同一次 `make check-restricted` 里一半测试跳过、一半失败。
const noLoopbackEnv = "ALLOW_NO_LOOPBACK"

// loopbackUsable 探测本机能否绑定回环端口，每个测试二进制只做一次。
//
// 逐个测试探测会在受限环境里产生几十次注定失败的 syscall，而结果对同一个
// 进程不会变。
var loopbackUsable = sync.OnceValues(func() (bool, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return false, err
	}
	return true, listener.Close()
})

// requireLoopback 声明该测试需要绑定本地回环端口，不具备则跳过。
//
// **本包自己实现而不是复用 internal/testutil**：ratelimit 是可独立发布的包
// （pkg/），它的测试同样不该 import 本仓的 internal——那会让这个包无法被
// 单独取用。代价是这段判定有两份，因此两份必须保持同口径（见 loopbackSkipReason）。
//
// 谁绑端口谁调用：miniredis.RunT 会起一个真实的 TCP 监听，因此每个用到它的
// 测试或 helper 都必须先过这道判定。漏掉一处的后果不是那个测试失败，而是
// 整个 `make check-restricted` 变红——而它只在发布门禁里跑，于是问题会一路
// 潜伏到发版当天。
func requireLoopback(t *testing.T) {
	t.Helper()

	probe := func() bool {
		available, err := loopbackUsable()
		if available && err != nil {
			t.Fatalf("close loopback probe: %v", err)
		}
		if err != nil {
			t.Logf("loopback unavailable: %v", err)
		}
		return available
	}
	if reason, skip := loopbackSkipReason(os.Getenv(noLoopbackEnv), probe); skip {
		t.Skip(reason)
	}
}

// loopbackSkipReason 是判定本身，抽成纯函数以便测试。
//
// 直接测 requireLoopback 做不到：它一旦判定跳过就会终止调用它的测试，
// 断言语句根本执行不到——测试会以 SKIP 结束而看起来"通过"。
//
// 两条来源缺一不可：
//
//	**显式标志**（ALLOW_NO_LOOPBACK=1）→ 无条件跳过。这让标志在能绑端口的
//	  机器上也真正生效，受限环境的覆盖率数字因此可以在开发机上复现。
//	**实际探测** → 真绑一次 127.0.0.1:0。环境变量会说谎：没设标志但环境确实
//	  受限时，只看标志会让测试直接失败，而那种失败看起来像代码坏了。
//
// probe 延迟求值：标志置位时不该再去碰网络。
func loopbackSkipReason(flag string, probe func() bool) (string, bool) {
	if flag == "1" {
		return "skipped: " + noLoopbackEnv + "=1 forces the restricted-sandbox path", true
	}
	if !probe() {
		return "skipped: this environment cannot bind a loopback port", true
	}
	return "", false
}

// TestLoopbackSkipReason 钉住两条判定来源，且各自给出可辨识的理由。
func TestLoopbackSkipReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		flag     string
		usable   bool
		wantSkip bool
		wantHint string
	}{
		{name: "标志置位即跳过", flag: "1", usable: true, wantSkip: true, wantHint: noLoopbackEnv},
		{name: "标志置位时不探测", flag: "1", usable: false, wantSkip: true, wantHint: noLoopbackEnv},
		{name: "环境确实受限也跳过", flag: "", usable: false, wantSkip: true, wantHint: "cannot bind"},
		{name: "能绑端口则不跳过", flag: "", usable: true, wantSkip: false},
		{name: "标志取其他值不算置位", flag: "0", usable: true, wantSkip: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			probed := false
			reason, skip := loopbackSkipReason(tt.flag, func() bool {
				probed = true
				return tt.usable
			})
			if skip != tt.wantSkip {
				t.Fatalf("skip = %v, want %v (reason %q)", skip, tt.wantSkip, reason)
			}
			if tt.wantHint != "" && !strings.Contains(reason, tt.wantHint) {
				t.Fatalf("reason %q 不含 %q，排查时看不出是哪条来源生效", reason, tt.wantHint)
			}
			// 标志置位时不该再碰网络：那次探测的结果不影响结论，
			// 而在受限环境里它是一次注定失败的 syscall。
			if tt.flag == "1" && probed {
				t.Fatal("标志已置位却仍执行了探测")
			}
		})
	}
}
