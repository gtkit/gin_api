package cmd

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/gtkit/gin-api/internal/bootstrap"

	logger "github.com/gtkit/logger/v2"
)

// signalSupervisor 是进程内**唯一**的 OS 信号监督者
// （runtime-state-machine spec）。
//
// 唯一性是硬要求：两处监听会同时收到同一个物理信号，任何"看对方是否已请求
// 关停"的判定都成为竞态——调度顺序不同即可让**首次**信号被判成第二次而直接
// 强制关停，整段优雅 drain 被跳过。曾经 cmd 与 App 各监听一份，正是这个形状。
//
// 翻译规则：
//
//	第一次 → RequestShutdown（优雅）+ 取消 ctx（覆盖装配期：数据库、Redis、
//	         限流探针都在 NewApp 里做 I/O，信号必须能中止它们并触发回滚）
//	第二次 → Force（取消各阶段剩余等待、直接关连接、非零退出）
//	第三次及以后 → 不再处理：两次已经表达完全部语义，再多也只是同一个意图
type signalSupervisor struct {
	shutdown *bootstrap.ShutdownCoordinator
	// ctx 传给 NewApp 与 App.Run；首个信号取消它。
	ctx    context.Context
	cancel context.CancelFunc
	stop   func()
	// once 保证 Close 幂等（stop 会 close 通道，重复关闭 panic）。
	once sync.Once
}

// newSignalSupervisor 安装信号监听并返回监督者。
//
// 调用方必须 defer Close()：signal.Stop 只解除注册、不关闭通道，
// 没有独立退出信号的监听协程会在命令返回后永久阻塞。
func newSignalSupervisor(parent context.Context) *signalSupervisor {
	ctx, cancel := context.WithCancel(parent)
	sup := &signalSupervisor{
		shutdown: bootstrap.NewShutdownCoordinator(),
		ctx:      ctx,
		cancel:   cancel,
	}

	// 缓冲 2：两次信号足以表达全部语义，第三次之后丢弃不损失信息。
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	sup.stop = func() {
		signal.Stop(signals)
		close(done)
	}

	go sup.watch(signals, done)
	return sup
}

// watch 按到达顺序翻译信号；done 关闭（= 命令返回）时退出。
func (s *signalSupervisor) watch(signals <-chan os.Signal, done <-chan struct{}) {
	first := true
	for {
		select {
		case sig := <-signals:
			if first {
				logger.Infof("received %s: starting graceful shutdown", sig)
				s.shutdown.RequestShutdown(nil)
				// 取消 ctx：装配期的 I/O 与启动屏障都靠它中止。
				s.cancel()
				first = false
				continue
			}
			logger.Warnf("received %s again: forcing shutdown", sig)
			s.shutdown.Force()
			return
		case <-done:
			return
		}
	}
}

// Close 解除信号注册并结束监听协程。
//
// 幂等：stop 关闭 done 通道，重复关闭会 panic。命令树上 defer 与显式收尾
// 很容易重叠，让"多关一次"成为进程崩溃的原因不值得。
func (s *signalSupervisor) Close() {
	s.once.Do(func() {
		s.stop()
		s.cancel()
	})
}
