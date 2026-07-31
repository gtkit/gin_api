package cmd

import (
	"context"
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
		RunE: func(_ *cobra.Command, _ []string) error {
			app, err := bootstrap.NewApp(bootstrap.Options{})
			if err != nil {
				return commandError("bootstrap app", err)
			}
			fmt.Printf("\x1b[32m%s\x1b[0m", _UI)

			return commandError("run app", app.Run(context.Background()))
		},
	}
}
