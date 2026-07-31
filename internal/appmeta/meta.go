package appmeta

const (
	// CommandName 是对外暴露的命令名。
	CommandName = "app_client"
	// RuntimeName 是运行时展示名称。
	RuntimeName = " Client"
	// ProjectName 是项目内部标识。
	ProjectName = "go-gin-api"
	// MigrateLockName 是数据库迁移使用的分布式锁名。
	MigrateLockName = "app_client_migrate"
	// DefaultAvatarURL 是默认头像地址。母版不内置任何具体业务的外部资源；
	// 由各项目按需在配置或此处填入自己的默认头像。
	DefaultAvatarURL = ""
)
