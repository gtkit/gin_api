//go:build !windows

// 本文件整体不在 Windows 上编译。
//
// 两个测试都靠 proc.Signal(syscall.SIGTERM) 向自身投递信号，而 Windows 的
// os.Process.Signal 对 Kill 以外的信号一律返回 "not supported by windows"——
// 那是平台缺少对应的运行时行为，不是测试写法的问题，加 t.Skip 只是把同一个
// 事实换个地方表达。helper deliverSignal / waitFor 只被本文件使用，随文件
// 一起排除不会留下悬空符号。
//
// 服务的部署目标是 Linux 容器（Dockerfile 与 make build 都固定 GOOS=linux），
// 因此这两条断言在交付路径上始终被执行。

package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// deliverSignal 向本进程投递一个信号，并等它确实被内核送达。
//
// 自带一个屏蔽监听：不装的话 SIGTERM 的默认处置会直接杀掉测试进程。
func deliverSignal(t *testing.T, sig syscall.Signal) {
	t.Helper()

	block := make(chan os.Signal, 1)
	signal.Notify(block, sig)
	defer signal.Stop(block)

	proc, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	if err := proc.Signal(sig); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	select {
	case <-block:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s was never delivered", sig)
	}
}

// waitFor 轮询等待条件成立。
//
// 信号从内核送达到监督者协程完成翻译之间有调度间隔，直接断言会偶发失败。
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}

// TestSupervisorFirstSignalIsGraceful 钉住第一次信号的翻译
// （runtime-state-machine spec）：请求优雅关停并取消 ctx（覆盖装配期），
// **不**强制。
//
// 本测试与 bootstrap 侧的 TestAppDoesNotRegisterSignalHandlers 成对：
// 那边保证 App 不再自己监听，这边保证唯一监督者确实在做翻译。
// 信号监听是进程级状态，因此不并行。
func TestSupervisorFirstSignalIsGraceful(t *testing.T) {
	sup := newSignalSupervisor(context.Background())
	defer sup.Close()

	deliverSignal(t, syscall.SIGTERM)

	waitFor(t, func() bool {
		select {
		case <-sup.shutdown.Requested():
			return true
		default:
			return false
		}
	}, "first signal did not request a graceful shutdown")

	if sup.shutdown.Forced() {
		t.Fatal("first signal escalated to the forced path: the graceful drain would be skipped")
	}
	// ctx 必须一并取消：装配期的 I/O（数据库、Redis、限流探针）只认 ctx。
	waitFor(t, func() bool { return sup.ctx.Err() != nil },
		"first signal did not cancel the context; assembly-phase I/O would keep running")
}

// TestSupervisorSecondSignalForces 钉住第二次信号进入强制路径。
func TestSupervisorSecondSignalForces(t *testing.T) {
	sup := newSignalSupervisor(context.Background())
	defer sup.Close()

	deliverSignal(t, syscall.SIGTERM)
	waitFor(t, func() bool {
		select {
		case <-sup.shutdown.Requested():
			return true
		default:
			return false
		}
	}, "first signal was not observed")

	deliverSignal(t, syscall.SIGTERM)
	waitFor(t, sup.shutdown.Forced, "second signal did not force shutdown")
}

// TestSupervisorCloseStopsWatching 钉住监听协程随命令返回而退出：
// signal.Stop 只解除注册、不关闭通道，没有独立退出信号的协程会永久阻塞。
func TestSupervisorCloseStopsWatching(t *testing.T) {
	sup := newSignalSupervisor(context.Background())
	sup.Close()

	// Close 之后 ctx 已取消（命令结束，App 也该随之收尾）。
	if sup.ctx.Err() == nil {
		t.Fatal("Close did not cancel the context")
	}
	// 再次 Close 不得 panic：defer 与显式收尾在命令树上很容易重叠，
	// 而 stop 关闭 done 通道——不做幂等，多关一次就是进程崩溃。
	// **这里不替换 sup.stop**：替换掉就等于把要验的那条路径绕开了。
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("second Close panicked: %v", r)
		}
	}()
	sup.Close()
}
