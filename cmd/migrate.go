package cmd

import (
	"strings"
	"time"

	"github.com/gtkit/gin-api/internal/bootstrap"
	"github.com/gtkit/gin-api/internal/models"

	migrate "github.com/gtkit/migratex"
	"github.com/spf13/cobra"
)

func init() {
	// 注册根命令
	RegisterRootCommands(newMigrateCmd, newMakeCmd)
}

// buildMigrationCommands 初始化运行时并构建 migratex 命令集，仅在执行迁移子命令时调用。
func buildMigrationCommands() (*migrate.Commands, func(), error) {
	rt, err := bootstrap.NewRuntime(bootstrap.Options{})
	if err != nil {
		return nil, nil, err
	}

	cmds, err := migrate.NewCommands(
		rt.DB().Mdb(),
		migrate.WithProjectName("github.com/gtkit/gin-api"),
		migrate.WithMigrationDir("database/migrations"),
		migrate.WithDDLModels(
			&models.User{},
			&models.UserProfile{},
			&models.UserDeactivation{},
		),
		migrate.WithTimeout(5*time.Minute),
	)
	if err != nil {
		rt.Close()
		return nil, nil, err
	}

	return cmds, rt.Close, nil
}

// firstToken 返回第一个非 flag 参数（用于判断子命令名，如 up/down/status/make）；
// 因 migrate/make 关闭了 cobra 的 flag 解析，需自行从原始 args 里取出该 token。
func firstToken(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

// runMigratexSubtree 把参数转发给 migratex 内层命令；帮助或无参时不连库。
func runMigratexSubtree(cmd *cobra.Command, args []string, pick func(*migrate.Commands) *cobra.Command, action string) error {
	if tok := firstToken(args); tok == "" || tok == "help" {
		return cmd.Help()
	}

	cmds, closeRuntime, err := buildMigrationCommands()
	if err != nil {
		return commandError("bootstrap runtime", err)
	}
	defer closeRuntime()

	sub := pick(cmds)
	sub.SilenceUsage = true
	sub.SilenceErrors = true
	sub.SetArgs(args)

	return commandError(action, sub.Execute())
}

// newMigrateCmd 构造 migrate 子命令：执行数据库迁移（up/down/status/lint/pretend）。
// 关闭 cobra flag 解析，把全部参数透传给内层 migratex 的 Migrate 子树；
// 仅在真正执行迁移子命令时才 bootstrap 运行时连库（help / 无参不连库）。
func newMigrateCmd(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:                "migrate",
		Short:              "Run database migrations (up/down/status/lint/pretend)",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMigratexSubtree(cmd, args,
				func(c *migrate.Commands) *cobra.Command { return c.Migrate }, "run migrate")
		},
	}
}

// newMakeCmd 构造 make 子命令：根据 GORM model 生成迁移文件（DDL 草稿）。
// 与 migrate 一样关闭 flag 解析并透传给 migratex 的 Make 子树，写入 database/migrations。
func newMakeCmd(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:                "make",
		Short:              "Generate migration files from GORM models",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMigratexSubtree(cmd, args,
				func(c *migrate.Commands) *cobra.Command { return c.Make }, "run make")
		},
	}
}
