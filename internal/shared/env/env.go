// Package env 承载运行环境这一跨包共享的语义。
//
// 放在 shared 而不是配置包内：配置、路由装配、JWT 密钥权限、日志路径校验与
// pprof 绑定限制都按环境分叉，而其中只有一处属于配置本身。谈论"我在哪个环境"
// 不应当要求先 import 配置包。
package env

// Env 表示当前运行环境，类型化以规避字符串字面量散落与拼写错误。
//
// **取值来自 config/env/env.yml 的顶层 env 字段**，由配置层做精确校验：
// 只接受 dev|test|prod，不归一化、不降级。未知值静默当作非生产会绕过 pprof
// 绑定限制、生产日志路径校验与发布模式等全部生产保护，因此宽松匹配在任何
// 一层都不允许——包括本包，它刻意不提供 Parse 之类的宽松解析入口。
type Env string

const (
	Dev  Env = "dev"
	Test Env = "test"
	Prod Env = "prod"
)

// IsDev 报告当前是否是开发环境。
func (e Env) IsDev() bool { return e == Dev }

// IsTest 报告当前是否是测试环境。
func (e Env) IsTest() bool { return e == Test }

// IsProd 报告当前是否是生产环境。
func (e Env) IsProd() bool { return e == Prod }

// String 实现 fmt.Stringer，便于日志/格式化。
func (e Env) String() string { return string(e) }

// Name 返回人类可读的环境名称，便于日志与对外展示。
func (e Env) Name() string {
	switch e {
	case Dev:
		return "⭐【开发环境】"
	case Test:
		return "⭐【测试环境】"
	case Prod:
		return "🔴【生产环境】"
	default:
		return "【未知环境】"
	}
}
