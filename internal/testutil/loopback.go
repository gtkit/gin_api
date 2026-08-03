// Package testutil 提供测试专用的环境能力判定。
//
// 只被 `_test.go` 使用：生产代码引用它会把测试期的跳过语义带进运行期，
// 而"能力不具备就跳过"在生产里的对应行为是启动失败，不是静默略过。
package testutil

import (
	"net"
	"os"
	"sync"
	"testing"
)

// noLoopbackEnv 是强制声明"本环境禁止绑定回环端口"的标志。
const noLoopbackEnv = "ALLOW_NO_LOOPBACK"

// loopbackUsable 探测本机能否绑定回环端口，每个测试二进制只做一次。
//
// 逐个测试探测会在受限环境里产生几十次注定失败的 syscall，而结果对同一个
// 进程不会变。用 sync.OnceValue 而不是 sync.Once + 包级 bool：后者需要在
// 函数里重新绑定包级变量，那是本仓明令禁止的形态（见 check-package-vars）。
var loopbackUsable = sync.OnceValue(func() bool {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return false
	}
	listener.Close()
	return true
})

// RequireLoopback 声明该测试需要绑定或连接本地回环端口，不具备则跳过。
//
// 判定有两条来源，缺一不可：
//
//	**显式标志**（ALLOW_NO_LOOPBACK=1）→ 无条件跳过。
//	  这让标志在**能绑端口的机器上也真正生效**——否则受限环境的覆盖率
//	  数字永远无法在开发机上复现，也就永远无法验证。这正是此前门禁数字
//	  不可复现的直接原因。
//
//	**实际探测** → 真绑一次 127.0.0.1:0，失败则跳过。
//	  环境变量会说谎：没设标志但环境确实受限时，只看标志会让测试直接失败，
//	  而那种失败对使用者毫无提示——它看起来像代码坏了，不像环境不支持。
//
// 必须由**直接**绑定或连接端口的函数调用（测试函数或测试 helper 均可）。
// 架构传感器强制这一点：只修当前这些点而不装防线，下一个新测试就会
// 重新打破受限环境，且失效是静默的。
func RequireLoopback(t *testing.T) {
	t.Helper()

	if reason, skip := skipReason(os.Getenv(noLoopbackEnv), loopbackUsable); skip {
		t.Skip(reason)
	}
}

// skipReason 是判定本身，抽成纯函数以便测试。
//
// 直接测 RequireLoopback 做不到：它一旦判定跳过就会终止调用它的测试，
// 断言语句根本执行不到——测试会以 SKIP 结束而看起来"通过"，
// 而那正是一个永远不会失败、也就永远证明不了什么的测试。
//
// probe 延迟求值：标志置位时不该再去碰网络，那次探测的结果不影响结论，
// 而在受限环境里它是一次注定失败的 syscall。
func skipReason(flag string, probe func() bool) (string, bool) {
	if flag == "1" {
		return "skipped: " + noLoopbackEnv + "=1 forces the restricted-sandbox path", true
	}
	if !probe() {
		return "skipped: this environment cannot bind a loopback port", true
	}
	return "", false
}
