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

	return errors.Join(errs...)
}
