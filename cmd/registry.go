package cmd

import "github.com/spf13/cobra"

// commandFactory 按需构造一个 root 级别子命令；入参为共享的 rootOptions。
type commandFactory func(*rootOptions) *cobra.Command

// commandSetFactory 按需构造 db 命令组下的一组子命令。
type commandSetFactory func(*rootOptions) []*cobra.Command

// rootCommandFactories / dbCommandFactories 是全局注册表：各命令文件在 init() 里
// 把自己的工厂追加进来，NewRootCmd 构造时统一取出装配，实现命令的解耦注册。
var (
	rootCommandFactories []commandFactory
	dbCommandFactories   []commandSetFactory
)

// RegisterRootCommand 注册单个 root 级别命令扩展，通常在各命令文件的 init() 中调用。
func RegisterRootCommand(factory commandFactory) {
	rootCommandFactories = append(rootCommandFactories, factory)
}

// RegisterRootCommands 批量注册 root 级别命令扩展，等价于逐个调用 RegisterRootCommand。
func RegisterRootCommands(factories ...commandFactory) {
	for _, factory := range factories {
		RegisterRootCommand(factory)
	}
}

// RegisterDBCommands 注册 db 命令组下的一组子命令扩展。
func RegisterDBCommands(factory commandSetFactory) {
	dbCommandFactories = append(dbCommandFactories, factory)
}

// buildRootCommands 遍历已注册的 root 命令工厂，构造出全部 root 子命令；
// 跳过 nil 工厂，供 NewRootCmd 一次性 AddCommand。
func buildRootCommands(opts *rootOptions) []*cobra.Command {
	commands := make([]*cobra.Command, 0, len(rootCommandFactories))
	for _, factory := range rootCommandFactories {
		if factory == nil {
			continue
		}

		commands = append(commands, factory(opts))
	}

	return commands
}

// buildDBCommands 遍历已注册的 db 命令组工厂，展开为 db 命令下的全部子命令；
// 目前无内置注册者，保留为经 RegisterDBCommands 接入 db 扩展命令的挂载点。
func buildDBCommands(opts *rootOptions) []*cobra.Command { //nolint:unused // kept for DB command extensions registered via RegisterDBCommands.
	var commands []*cobra.Command
	for _, factory := range dbCommandFactories {
		if factory == nil {
			continue
		}

		commands = append(commands, factory(opts)...)
	}

	return commands
}
