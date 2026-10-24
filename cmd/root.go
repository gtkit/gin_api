package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/gtkit/gin-api/internal/appmeta"

	"github.com/spf13/cobra"
)

// rootOptions 承载在所有命令间共享的根级选项，传给各子命令构造函数。
type rootOptions struct {
	// signals 是进程唯一的信号监督者；为 nil 表示未安装（测试直接构造命令树）。
	signals *signalSupervisor
}

// Execute 是 CLI 的顶层入口：构造 root 命令树并执行，供 main 调用。
// 返回的 error 交由 main 决定退出码；命令自身已 SilenceErrors，不重复打印。
//
// 信号监督者在**这里**安装，而且是进程内唯一的一处（见 signalSupervisor）：
// 装配阶段（数据库、Redis、限流探针）同样做 I/O，监听迟到会让 SIGTERM 按
// 默认处置直接终止进程、装配回滚一行都不执行；而两处监听同一信号则让
// "第一次/第二次"的判定变成竞态。监督者把两次信号翻译成协调器动作，
// 经 rootOptions 交给需要它的命令。
func Execute() error {
	return ExecuteWithBuildInfo(BuildInfo{})
}

// BuildInfo supplies immutable executable metadata from the process entrypoint.
// Missing build inputs remain explicitly unknown.
type BuildInfo struct {
	Version   string
	Commit    string
	BuildTime string
}

// ExecuteWithBuildInfo runs the existing command tree with supplied build inputs.
func ExecuteWithBuildInfo(info BuildInfo) error {
	sup := newSignalSupervisor(context.Background())
	defer sup.Close()

	return newRootCmd(sup, info).ExecuteContext(sup.ctx)
}

// NewRootCmd 构造 root cobra 命令，显式挂载服务、迁移、密钥和诊断命令。
// 项目初始化命令由 scaffold 构建标签控制。
// SilenceUsage/SilenceErrors 关闭 cobra 默认的用法与错误输出，错误统一交给调用方处理。
func NewRootCmd(signals *signalSupervisor) *cobra.Command {
	return newRootCmd(signals, BuildInfo{})
}

func newRootCmd(signals *signalSupervisor, info BuildInfo) *cobra.Command {
	opts := &rootOptions{signals: signals}
	values := []*string{&info.Version, &info.Commit, &info.BuildTime}
	for _, value := range values {
		if strings.TrimSpace(*value) == "" {
			*value = "unknown"
		}
	}

	rootCmd := &cobra.Command{
		Use:           appmeta.CommandName,
		Short:         appmeta.RuntimeName + " runtime",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       fmt.Sprintf("%s commit=%s build_time=%s", info.Version, info.Commit, info.BuildTime),
	}

	rootCmd.AddCommand(
		newServerCmd(opts),
		newMigrateCmd(opts),
		newMakeCmd(opts),
		newJWTCmd(opts),
		newDoctorCmd(opts),
	)
	addProjectCommand(rootCmd)

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
