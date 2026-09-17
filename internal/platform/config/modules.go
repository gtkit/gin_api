package config

import "errors"

// ModuleConfigs 承载各业务模块的强类型配置子树。
//
// 为什么字段是**强类型**而不是「根存 raw subtree、各模块自解码」：
// 根层已经开启严格解码（未知键即启动失败）。raw subtree 方案会让子树内部
// 退回宽松解码——配置里写错一个键名不再报错，而是静默取默认值。
// 那正是严格解码要消灭的形态，不能因为"模块化"就在子树里放弃它。
//
// 为什么类型定义放在 config 包**自身**的文件里，而不是 import 各模块：
// platform/config 是基础设施层，反向依赖 internal/module/* 会让依赖方向倒转，
// 且任何一个模块的编译错误都会连带让配置加载不可用。
// 生成器把类型写进 internal/platform/config/module_<name>.go，
// 字段与 mapstructure tag 都留在本包内。
type ModuleConfigs struct {
	AI AIModuleConfig `mapstructure:"ai"`
	// scaffold:module-config-fields

	// User 是 v0.4.0 删除的 user 示例模块留下的**墓碑字段**。
	//
	// 它存在的唯一目的是让残留 modules.user 段的旧配置通过严格解码，从而拿到
	// 下面那条写明迁移动作的错误，而不是"未知键 modules.user"——后者说不出
	// 这个键曾经是什么、为什么没了、该往哪改，而读到它的人恰恰是刚升级、
	// 还不知道模块被删了的那个。同一个理由已经在 types.go 的 readtimeout /
	// writertimeout / private_key_path 上用过三次，这里是第四次。
	//
	// 类型取 map 而不是删除前的结构体：派生项目可能增删过子键，强类型只覆盖
	// 本仓当初定义的那些，子键一对不上，错误又退回未知键。map 吸收整棵子树。
	//
	// 运行时不读它，唯一消费者是 validateModuleConfigs。所有派生项目都越过
	// v0.4.0 之后，整个字段连同校验分支一起删掉。
	//
	// 覆盖边界（实测，非推断）：`modules.user: {}` 解出非 nil 空 map，能拦；
	// 而 `modules.user:`（yaml null）在 koanf 层就不产生这个键，解出的是 nil，
	// 与"整段不存在"无法区分，拦不住。指针类型也救不了——缺的是键本身。
	// 一个只剩空值的 user 段不携带任何被忽略的配置，漏掉它不会让人误以为
	// 某项设置仍在生效，因此不为这一种形态另加机制。
	User map[string]any `mapstructure:"user"`
}

// validateModuleConfigs 汇总各模块配置的语义校验。
//
// 严格解码只保证"字段名认识"，不保证取值有意义——`rate: 0`、
// 一个不存在的目录路径、互相矛盾的开关组合，在解码层面全都合法。
// 这些判定必须由模块自己给出，并在此汇聚，否则它们会散落到各模块的
// 首次使用处，变成运行期才暴露的错误。
//
// 全部错误聚合返回，不遇错即停：配置问题通常成串出现，
// 逐个暴露意味着改一次重启一次。
func validateModuleConfigs(m ModuleConfigs) error {
	var errs []error

	errs = append(errs, m.AI.Validate()...)
	// scaffold:module-config-validators

	// 判据是"键存在"而不是"有内容"：`user: {}` 同样是没迁移完的配置。
	if m.User != nil {
		errs = append(errs, errors.New(
			"modules.user has been removed in v0.4.0 along with the user example module "+
				"(GET|PUT /api/v1/user, POST /api/v1/user/deactivate, internal/module/user, "+
				"the UserProfile / UserDeactivation models and the deactivation cleanup worker). "+
				"Delete the modules.user section from your config. If you still need those "+
				"capabilities, take internal/module/user, internal/runtime/module/user and "+
				"internal/repository/{user,user_deactivation} from the v0.3.0 tag, then restore "+
				"this field as a typed module config"))
	}

	return errors.Join(errs...)
}
