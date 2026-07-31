package cmd

import (
	"fmt"
	"net"

	"github.com/gtkit/gin-api/internal/platform/config"

	"github.com/spf13/cobra"
)

// configLoader 抽象配置加载动作，便于测试注入桩替换 config.Load。
type configLoader func() (*config.Config, error)

func init() {
	RegisterRootCommand(newDoctorCmd)
}

// newDoctorCmd 构造 doctor 子命令，默认使用 config.Load 作为配置加载器。
func newDoctorCmd(_ *rootOptions) *cobra.Command {
	return newDoctorCmdWithLoader(config.Load)
}

// newDoctorCmdWithLoader 构造 doctor 子命令：不启动任何服务，只做部署前的配置自检。
// 行为：加载并严格校验配置（失败即 fail-fast 报错），成功后打印环境、应用名、监听地址、
// pprof 开关等关键项。仅验证配置文件本身，不代表 MySQL/Redis/第三方已连通。
// load 参数化便于单测注入桩，不触真实磁盘配置。
func newDoctorCmdWithLoader(load configLoader) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Validate runtime configuration without starting services",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := load()
			if err != nil {
				return commandError("validate runtime configuration", err)
			}
			if cfg == nil {
				return fmt.Errorf("validate runtime configuration: config loader returned nil")
			}

			cmd.Println("config: ok")
			cmd.Printf("env: %s\n", cfg.Env)
			cmd.Printf("application: %s\n", cfg.App.Name)
			cmd.Printf("listen: %s\n", net.JoinHostPort(cfg.App.Host, cfg.App.Port))
			cmd.Printf("pprof: %s\n", enabledLabel(cfg.Pprof.Enabled))

			return nil
		},
	}
}

// enabledLabel 把布尔开关渲染为人类可读的 "enabled"/"disabled" 文案，用于自检输出。
func enabledLabel(enabled bool) string {
	if enabled {
		return "enabled"
	}

	return "disabled"
}
