package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// 本文件收拢"目标平台具备而开发平台未必具备"的能力判定。
//
// 母版的交付平台是 Linux 容器（Dockerfile 与 make build 都固定 GOOS=linux），
// CI 也只跑 ubuntu——于是每一条依赖 Unix 语义的断言在 Linux 上都真实执行，
// 而在 Windows 开发机上会以一个与被测代码无关的理由变红。
//
// 那种红色的代价不是"少跑了几条测试"，而是**让整份测试输出失去信号价值**：
// 新接手的人第一次跑 go test ./... 就见红，此后再出现的真实失败也会被当作
// "这台机器本来就这样"。这里把平台事实显式化，让 Windows 上的绿色是可信的绿色。
//
// 判定分两类，不可混用：
//
//	确定性事实（权限位、信号）→ 直接看 runtime.GOOS。这类能力不随机器配置变化，
//	  再去探测只是把一个常量包装成一次 syscall。
//	机器相关能力（符号链接）→ 实际探测。Windows 开启开发者模式后可以创建符号链接，
//	  按 GOOS 一刀切会让具备该能力的机器白白失去覆盖。
//
// 与 RequireLoopback 的分工：那条管"受限沙箱"，这里管"非目标操作系统"。

// unixFilePerm 报告当前平台的 os.FileMode 权限位是否具备 Unix 语义。
//
// Windows 不具备：NTFS 的访问控制是 ACL，不映射到权限位，Go 在该平台对普通
// 文件恒报 0666（只按只读属性区分 0444/0666），对目录恒报 0777。于是
// "mode == 0600" 这类断言在那边恒为假，chmod 也不会改变 Stat 的结果。
const unixFilePerm = runtime.GOOS != "windows"

// HasUnixFilePerm 报告当前平台是否具备 Unix 文件权限位语义。
//
// 用在权限断言**夹在其他断言中间**的测试里：那种位置上 RequireUnixFilePerm
// 会终止测试函数，把后面真正的主判据一起带走——跳过的理由是平台，
// 丢掉的却是与平台无关的覆盖。这种场合把权限那段包进 if 即可。
//
// 断言集中在测试末尾或可以独立成文件时，优先用 RequireUnixFilePerm 或
// //go:build !windows：那两种写法在测试输出里留下 SKIP 记录，
// 而 if 是静默的。
func HasUnixFilePerm() bool { return unixFilePerm }

// RequireUnixFilePerm 声明该测试断言 Unix 文件权限位，不具备则跳过。
//
// 只跳过**权限**断言，不要顺手跳过同一测试里的存在性、内容或行为断言：
// 那些在任何平台上都成立，一并跳过等于用平台差异换走真实覆盖。
// 断言分布在同一个测试函数里时，正确做法是把权限那段拆成独立的
// //go:build !windows 文件，而不是给整条用例加这个 Require。
func RequireUnixFilePerm(t *testing.T) {
	t.Helper()

	if !unixFilePerm {
		t.Skip("skipped: " + runtime.GOOS + " has no Unix file permission bits (ACL-based; Go reports 0666/0777)")
	}
}

// RequireSignalDelivery 声明该测试向本进程投递 SIGTERM/SIGINT，不具备则跳过。
//
// Windows 的 os.Process.Signal 对 Kill 以外的信号一律返回
// "not supported by windows"——没有可投递的信号，也就没有可被观察的翻译行为。
func RequireSignalDelivery(t *testing.T) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("skipped: windows cannot deliver SIGTERM/SIGINT to a process")
	}
}

// symlinkUsable 探测本机能否创建符号链接，每个测试二进制只做一次。
//
// 用探测而不是看 GOOS：Windows 在开启开发者模式（或进程持有
// SeCreateSymbolicLinkPrivilege）后可以创建符号链接，按 GOOS 跳过会让那些
// 机器失去本可获得的覆盖。不具备时的报错是
// "A required privilege is not held by the client"。
//
// 用 sync.OnceValue 而非 sync.Once + 包级 bool：后者要在函数里重新绑定包级
// 变量，是本仓明令禁止的形态（见 check-package-vars），与 loopbackUsable 同理。
var symlinkUsable = sync.OnceValue(func() bool {
	dir, err := os.MkdirTemp("", "symlink-probe-")
	if err != nil {
		return false
	}
	defer func() { _ = os.RemoveAll(dir) }()

	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		return false
	}
	return os.Symlink(target, filepath.Join(dir, "link")) == nil
})

// RequireSymlink 声明该测试需要创建符号链接，不具备则跳过。
func RequireSymlink(t *testing.T) {
	t.Helper()

	if !symlinkUsable() {
		t.Skip("skipped: this environment cannot create symbolic links")
	}
}
