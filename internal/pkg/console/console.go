// Package console 命令行辅助方法
package console

import (
	"fmt"
	"io"
	"os"

	"github.com/gtkit/logger/v2"
	"go.uber.org/zap"
)

// ANSI SGR 序列。自实现而非依赖第三方库：本包对颜色的全部需求就是这三种，
// 而此前为它独占一个 direct require（`github.com/mgutz/ansi`，版本停在
// 2020-07-06）。这个成本由每个派生项目继承，且它们无从选择。
//
// 取值**逐字节对齐替换前的行为**——`ansi.Color("MSG", "green")` 的实测输出是
// "\x1b[0;32mMSG\x1b[0m"，注意 `0;` 前缀（先复位再设色）而不是裸 `\x1b[32m`。
// 两者在终端上看不出差别，但对断言输出字节的调用方不等价。
const (
	colorGreen  = "\x1b[0;32m"
	colorRed    = "\x1b[0;31m"
	colorYellow = "\x1b[0;33m"
	colorReset  = "\x1b[0m"
)

var (
	consoleWriter io.Writer = os.Stdout
	exitFunc                = os.Exit
)

// Success 打印一条成功消息，绿色输出.
func Success(msg string) {
	colorOut(msg, colorGreen)
}

// Error 打印一条报错消息，红色输出.
func Error(msg string) {
	colorOut(msg, colorRed)
}

// Warning 打印一条提示消息，黄色输出.
func Warning(msg string) {
	colorOut(msg, colorYellow)
}

// Exit 打印一条报错消息，并退出 os.Exit(1).
func Exit(msg string) {
	Error(msg)
	exitFunc(1)
}

// ExitIf 语法糖，自带 err != nil 判断.
func ExitIf(err error) {
	if err != nil {
		Exit(err.Error())
	}
}

// colorOut 内部使用，设置高亮颜色.
//
// 不做 TTY 检测：替换前的实现同样不做，重定向到文件时一样会写入转义序列。
// 加检测是行为变更，与本次「只换实现、不改行为」的目标冲突。
func colorOut(message, color string) {
	if _, err := fmt.Fprintln(consoleWriter, color+message+colorReset); err != nil {
		logger.Error("write console message failed", zap.Error(err))
	}
}

func setConsoleOutputForTest(w io.Writer) func() {
	previous := consoleWriter
	consoleWriter = w
	return func() {
		consoleWriter = previous
	}
}

func setConsoleExitForTest(fn func(int)) func() {
	previous := exitFunc
	exitFunc = fn
	return func() {
		exitFunc = previous
	}
}
