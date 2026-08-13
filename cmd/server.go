package cmd

import (
	"fmt"
	"io"
	"strings"

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

// gopherBlue 是 Go 品牌主色 Gopher Blue #00ADD8 在 xterm-256 里的对应格：
// 38 号 = #00AFD7，与品牌色的 RGB 差为 (0,2,1)，肉眼不可分。
//
// 用 256 色 SGR（`38;5;N`）而不是 24 位真彩（`38;2;R;G;B`）取精确 #00ADD8：
// 差距已在感知阈之下，收益为零；而 macOS 的 Terminal.app 不支持真彩、会退回
// 近似，换过去反而把一个稳定的结果变成一个依赖终端实现的结果。
const gopherBlue = "\x1b[38;5;38m"

// bannerPalette 是 banner 逐行的前景色，按行下标取模取用。
//
// 默认单色 Gopher Blue：ANSI Shadow 的立体感来自字形本身（`█` 是字面，
// `╗╝═║` 是描边与投影），由字符密度造出，与颜色无关——单色是这个字体的常规
// 用法，不会损失任何层次。渐变只是额外装饰。
//
// 想要渐变就往这里加色阶，打印逻辑不用动。例如青→蓝六段：
//
//	"\x1b[38;5;51m" // #00FFFF
//	"\x1b[38;5;45m" // #00D7FF
//	"\x1b[38;5;39m" // #00AFFF
//	gopherBlue      // #00AFD7
//	"\x1b[38;5;32m" // #0087D7
//	"\x1b[38;5;26m" // #005FD7
var bannerPalette = []string{gopherBlue}

const (
	bannerReset = "\x1b[0m"
	bannerDim   = "\x1b[38;5;240m" // 分隔线用中性灰，不与字形争视觉重心
)

// printBanner 渲染启动横幅：逐行渐变着色，末尾收一条与字形等宽的细线。
//
// 收边线的宽度按 art 的实际显示宽度算，不写死常量——字形换了、或某行长度
// 变了，线会跟着变，不会出现"线比字长一截"这种一眼可见的错位。
// 宽度按 rune 计：art 全部由单元格宽的制表符与块字符组成，len() 会按字节
// 数算出三倍长度。
func printBanner(w io.Writer) {
	lines := strings.Split(strings.Trim(_UI, "\n"), "\n")

	width := 0
	for _, ln := range lines {
		if n := len([]rune(ln)); n > width {
			width = n
		}
	}

	var b strings.Builder
	b.WriteByte('\n')
	for i, ln := range lines {
		b.WriteString(bannerPalette[i%len(bannerPalette)])
		b.WriteString(ln)
		b.WriteString(bannerReset)
		b.WriteByte('\n')
	}
	b.WriteString(bannerDim)
	b.WriteString(strings.Repeat("─", width))
	b.WriteString(bannerReset)
	b.WriteString("\n\n")

	fmt.Fprint(w, b.String())
}

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
			printBanner(cmd.OutOrStdout())

			return commandError("run app", app.Run(ctx))
		},
	}
}
