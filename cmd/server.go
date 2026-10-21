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

// banner 用到的全部 SGR 序列。
//
// 逐个具名、不内联进调色板：切片里读到的应当是「这条色带由哪些颜色构成」，
// 而不是六串需要逐字符数分号的转义码。改配色时也不必回头核对哪个数字是色号、
// 哪个是模式位。
//
// 一律用 256 色索引（`38;5;N`），不用基本 8 色，也不用 24 位真彩：
//
//   - 基本 8 色**没有确定的 RGB**，由终端主题决定（同一个 `\x1b[32m` 在
//     macOS Terminal 是 #00A600、xterm 是 #00CD00、VS Code Dark+ 是 #0DBC79）。
//     用它等于把颜色的决定权交给终端。
//   - 24 位真彩能取到精确的品牌色，但与 256 色格的差距已在感知阈之下（见
//     gopherBlue），而 macOS 的 Terminal.app 不支持真彩、会退回近似——换过去
//     等于把一个确定的结果变成一个依赖终端实现的结果。
//
// 两个取值不是随手挑的：
//
//   - gopherBlue 取 38 号，是全 256 色里离 Go 品牌主色 #00ADD8 最近的一格
//     （#00AFD7，RGB 差 (0,2,1)，肉眼不可分）。
//   - bannerRule 刻意不取色带里的任何一格——分隔线的作用是收住版面，
//     与字形同色就会融成一片，那时它既不分隔也不收边（有测试锁定）。
//
// 行尾注释的 hex 是各色号在 xterm-256 色立方中的真实取值，不是近似描述。
const (
	bannerAqua  = "\x1b[38;5;51m"  // 51   #00FFFF  亮青
	bannerSky   = "\x1b[38;5;45m"  // 45   #00D7FF  天青
	bannerAzure = "\x1b[38;5;39m"  // 39   #00AFFF  湛蓝
	gopherBlue  = "\x1b[38;5;38m"  // 38   #00AFD7  Go 品牌主色（官方 #00ADD8）
	bannerSteel = "\x1b[38;5;32m"  // 32   #0087D7  钢蓝
	bannerDeep  = "\x1b[38;5;26m"  // 26   #005FD7  深蓝
	bannerRule  = "\x1b[38;5;240m" // 240  #585858  中性灰，收边线专用
	bannerReset = "\x1b[0m"        // 复位，清除前景色；缺它后续输出会继承横幅的颜色
)

// bannerPalette 是 banner 逐行的前景色，按行下标取模取用。
//
// 青→蓝渐变，色带精确穿过 Gopher Blue（第 4 行）：六格全部落在 xterm-256
// 色立方的 r=0 平面上（红分量恒为 0），因此从头到尾是同一个色相家族的明度与
// 冷暖推移，不会中途串到别的颜色去。
//
// 色阶数与字形行数**不必相等**：打印逻辑按行下标取模。换字体改了行数、或
// 这里增删色阶，都不需要动打印代码，也不会越界。
//
// 想回到单色只留一个元素即可，例如 `[]string{gopherBlue}`。
var bannerPalette = []string{
	bannerAqua,  // 51  #00FFFF  亮青
	bannerSky,   // 45  #00D7FF
	bannerAzure, // 39  #00AFFF
	gopherBlue,  // 38  #00AFD7  ← Go 品牌主色 #00ADD8
	bannerSteel, // 32  #0087D7
	bannerDeep,  // 26  #005FD7  深蓝
}

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
	b.WriteString(bannerRule)
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

			app, err := bootstrap.NewApp(ctx, bootstrap.Options{
				Shutdown:        shutdown,
				RegisterPlugins: bootstrap.RegisterDefaultPlugins,
			})
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
