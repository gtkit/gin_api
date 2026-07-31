package config

// Env 表示当前运行环境，类型化以规避字符串字面量散落与拼写错误。
// 取值由 config 文件基名决定（dev.yml / test.yml / pro.yml）。
type Env string

const (
	EnvDev  Env = "dev"
	EnvTest Env = "test"
	EnvProd Env = "prod"
)

// IsDev 报告当前是否是开发环境。
func (e Env) IsDev() bool { return e == EnvDev }

// IsTest 报告当前是否是测试环境。
func (e Env) IsTest() bool { return e == EnvTest }

// IsProd 报告当前是否是生产环境。
func (e Env) IsProd() bool { return e == EnvProd }

// String 实现 fmt.Stringer，便于日志/格式化。
func (e Env) String() string { return string(e) }

// Name 返回人类可读的环境名称，便于日志与对外展示。
func (e Env) Name() string {
	switch e {
	case EnvDev:
		return "⭐【开发环境】"
	case EnvTest:
		return "⭐【测试环境】"
	case EnvProd:
		return "🔴【生产环境】"
	default:
		return "【未知环境】"
	}
}
