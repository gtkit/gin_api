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
	// 协调器来自进程唯一的信号监督者：信号在 NewApp 之前就可能到达，
	// 协调器因此必须比 App 更早存在。未安装监督者时（测试直接构造命令树）
	// 传 nil，App 自建一个。
	var shutdown *bootstrap.ShutdownCoordinator
	if opts != nil && opts.signals != nil {
		shutdown = opts.signals.shutdown
	}

	return &cobra.Command{
		Use:   "server",
		Short: "Run the HTTP server and workers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// 用 cmd.Context() 而不是 context.Background()：装配与运行共用同一棵 ctx，
			// 上层取消（含 cobra 的 ExecuteContext）才能同时作用于资源建立与运行阶段。
			ctx := cmd.Context()

			app, err := bootstrap.NewApp(ctx, bootstrap.Options{Shutdown: shutdown})
			if err != nil {
				// 装配期收到信号：**纯取消**的错误树是中止的现象而非故障，
				// 按信号语义静默退出（与启动屏障的取消语义一致）；混入
				// 回滚/关闭失败的错误必须保留上报，不得随退出码 0 消失。
				//
				// **强制路径例外**：第二个信号的契约是"以非零退出"。它到达时
				// 装配同样会以纯取消结束，若也归一化成 0，进程就把一次被迫中止
				// 报告成了正常退出——而编排器与脚本正是按退出码判断的。
				if ctx.Err() != nil && bootstrap.IsPureCancellation(err) && !shutdown.Forced() {
					return nil
				}
				return commandError("bootstrap app", err)
			}
			fmt.Printf("\x1b[32m%s\x1b[0m", _UI)

			return commandError("run app", app.Run(ctx))
		},
	}
}
