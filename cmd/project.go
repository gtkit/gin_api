//go:build scaffold

package cmd

import (
	"github.com/gtkit/gin-api/internal/appmeta"
	"github.com/gtkit/gin-api/internal/projectinit"

	"github.com/spf13/cobra"
)

func init() {
	RegisterRootCommand(newProjectCmd)
}

// newProjectCmd 构造 project 命令组：脚手架母版专用的项目级工具集合。
// 自身不做事，仅挂载子命令（当前为 init）。
func newProjectCmd(_ *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Scaffold-mother project tooling",
	}
	cmd.AddCommand(newProjectInitCmd())
	return cmd
}

// newProjectInitCmd 构造 project init 子命令：把由脚手架母版（经 gonew 复制）实例化出的
// 新项目落地为独立项目。动作包括：按 --name 写入应用身份的唯一来源 project.mk、
// 重新生成 appmeta 身份常量、删除母版专属部署入口、重新初始化 git。
//
// 有安全护栏：项目名与分支名先于任何文件改动被校验；已有 Git 仓库默认拒绝执行；
// 工作区不干净时拒绝执行；拒绝在脚手架母版自身上运行；--dry-run 只预览不落盘。
// --name 为必填 flag。
func newProjectInitCmd() *cobra.Command {
	var (
		name           string
		branch         string
		dryRun         bool
		forceReinitGit bool
		noCommit       bool
	)

	cmd := &cobra.Command{
		Use:   "init --name <name>",
		Short: "Finalize a project instantiated from the scaffold mother (run after gonew)",
		Long: "Write the project identity into project.mk, regenerate appmeta from it, drop the\n" +
			"scaffold mother's environment-specific deployment entrypoints, and initialize git.\n" +
			"Guarded: validates the project and branch names before touching any file, refuses an\n" +
			"existing git repository unless --force-reinit-git is given, refuses a dirty tree and\n" +
			"the scaffold mother itself; --dry-run previews without writing.",
		Example: "" +
			"  " + appmeta.CommandName + " project init --name my_service\n" +
			"  " + appmeta.CommandName + " project init --name my_service --branch develop\n" +
			"  " + appmeta.CommandName + " project init --name my_service --dry-run",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			initializer, err := projectinit.New("")
			if err != nil {
				return commandError("create project initializer", err)
			}

			result, err := initializer.Run(projectinit.Options{
				Name:           name,
				Branch:         branch,
				DryRun:         dryRun,
				ForceReinitGit: forceReinitGit,
				NoCommit:       noCommit,
			})
			if err != nil {
				return commandError("run project init", err)
			}

			printProjectInitResult(cmd, result)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "",
		"new project identity name; must match ^[a-z][a-z0-9_]{0,62}$")
	cmd.Flags().StringVar(&branch, "branch", "",
		"initial branch name for the new repository (default \"main\"; validated by git check-ref-format)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"preview changes without writing files or reinitializing git")
	cmd.Flags().BoolVar(&forceReinitGit, "force-reinit-git", false,
		"allow running inside an existing git repository; PERMANENTLY DISCARDS the existing git history")
	cmd.Flags().BoolVar(&noCommit, "no-commit", false,
		"initialize and stage everything but do not create the initial commit (no git identity required)")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}

// printProjectInitResult 打印 project init 的执行结果：逐条列出被重写/删除的文件与提示信息，
// dry-run 模式下动词改为 "would rewrite/delete" 并说明未落盘、未重建 git。
func printProjectInitResult(cmd *cobra.Command, result *projectinit.Result) {
	rewriteAction := "rewrote"
	deleteAction := "deleted"
	if result.DryRun {
		rewriteAction = "would rewrite"
		deleteAction = "would delete"
	}

	for _, path := range result.Rewritten {
		cmd.Printf("%s %s\n", rewriteAction, path)
	}
	for _, path := range result.Deleted {
		cmd.Printf("%s %s\n", deleteAction, path)
	}
	for _, note := range result.Notes {
		cmd.Printf("note: %s\n", note)
	}

	if result.DryRun {
		cmd.Println("dry-run mode: no files were written and git was not reinitialized")
	}
}
