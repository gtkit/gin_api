package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/gtkit/gin-api/cmd"

	"github.com/gtkit/json"
)

// 全局启用 gtkit/json 的宽松解码（字符串/数字互转等容错）。
// 进程级注册，必须在任何 JSON 解析前执行；原挂在顶层 config 包 init，
// 随 config embed 的移除迁至程序入口。
func init() {
	json.RegisterFuzzyDecoders()
}

func main() {
	// 不手工设置 GOMAXPROCS：Go 1.25+ 运行时会自动感知容器 cgroup CPU 限额并动态跟随。
	// 手工按 NumCPU-1 覆盖会在容器里读到宿主核数而严重超配，且会关闭该自动行为。
	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "execute cobra command: %v\n", err)
		// 命令可以声明自己的退出码（doctor 用 0/2/1 区分"全通/仅降级/失败"）。
		// 统一退成 1 会让脚本无法区分"这台机器少了一项可选能力"与"它根本不能干活"。
		var coded interface{ ExitCode() int }
		if errors.As(err, &coded) {
			os.Exit(coded.ExitCode())
		}
		os.Exit(1)
	}
}
