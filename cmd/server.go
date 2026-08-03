package cmd

import (
	"fmt"

	"github.com/gtkit/gin-api/internal/bootstrap"

	"github.com/spf13/cobra"
)

const _UI = `
 ██████╗  ██████╗        ██████╗ ██╗███╗   ██╗       █████╗ ██████╗ ██╗
██╔════╝ ██╔═══██╗      ██╔════╝ ██║████╗  ██║      ██╔══██╗██╔══██╗██║
██║  ███╗██║   ██║█████╗██║  ███╗██║██╔██╗ ██║█████╗███████║██████╔╝██║
██║   ██║██║   ██║╚════╝██║   ██║██║██║╚██╗██║╚════╝██╔══██║██╔═══╝ ██║
╚██████╔╝╚██████╔╝      ╚██████╔╝██║██║ ╚████║      ██║  ██║██║     ██║
 ╚═════╝  ╚═════╝        ╚═════╝ ╚═╝╚═╝  ╚═══╝      ╚═╝  ╚═╝╚═╝     ╚═╝
`

func init() {
	RegisterRootCommand(newServerCmd)
}

// newServerCmd 构造 server 子命令——CLI 的主用途：启动 HTTP 服务与后台 worker。
// 执行流程：bootstrap.NewApp 组装依赖（配置/DB/Redis/路由/worker）→ 打印启动 banner →
// app.Run 阻塞运行直到进程退出。任一步失败经 commandError 附前缀返回。
func newServerCmd(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "server",
		Short: "Run the HTTP server and workers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// 用 cmd.Context() 而不是 context.Background()：装配与运行共用同一棵 ctx，
			// 上层取消（含 cobra 的 ExecuteContext）才能同时作用于资源建立与运行阶段。
			ctx := cmd.Context()

			app, err := bootstrap.NewApp(ctx, bootstrap.Options{})
			if err != nil {
				return commandError("bootstrap app", err)
			}
			fmt.Printf("\x1b[32m%s\x1b[0m", _UI)

			return commandError("run app", app.Run(ctx))
		},
	}
}
