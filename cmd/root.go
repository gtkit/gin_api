package cmd

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/gtkit/gin-api/internal/appmeta"

	"github.com/spf13/cobra"
)

// rootOptions 承载在所有命令间共享的根级选项，作为各子命令工厂的入参统一透传
// （当前为空占位，后续新增全局 flag 时挂在此处，避免改动各命令工厂签名）。
type rootOptions struct{}

// Execute 是 CLI 的顶层入口：构造 root 命令树并执行，供 main 调用。
// 返回的 error 交由 main 决定退出码；命令自身已 SilenceErrors，不重复打印。
//
// 信号上下文在**这里**建立而不是等到 App.Run：装配阶段（数据库、Redis、
// 限流探针）同样做 I/O，若信号监听迟到，SIGTERM 会按默认处置直接终止进程，
// 装配回滚一行都不会执行。App.Run 自己的 signal.Notify 与本层并存——
// 首个信号取消本 ctx（经 Run 内的翻译协程汇聚为关停请求）、同时被 Run 的
// 监听读到，两条路径幂等汇聚；第二个信号的强制语义仍由 Run 的监听独立判定。
func Execute() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return NewRootCmd().ExecuteContext(ctx)
}

// NewRootCmd 构造 root cobra 命令，并把所有经 RegisterRootCommand 注册的子命令
// （server / migrate / jwt / module / project / doctor 等）挂载上去。
// SilenceUsage/SilenceErrors 关闭 cobra 默认的用法与错误输出，错误统一交给调用方处理。
func NewRootCmd() *cobra.Command {
	opts := &rootOptions{}

	rootCmd := &cobra.Command{
		Use:           appmeta.CommandName,
		Short:         appmeta.RuntimeName + " runtime",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	rootCmd.AddCommand(buildRootCommands(opts)...)

	return rootCmd
}

// commandError 为命令执行错误统一附上动作前缀（如 "run server: ..."），
// 便于从 CLI 输出快速定位是哪一步失败；err 为 nil 时原样返回 nil。
func commandError(action string, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("%s: %w", action, err)
}
