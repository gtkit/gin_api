package config

import (
	"fmt"
	"io"
	"os"
	"strings"

	schema "github.com/gtkit/gin-api/internal/platform/config"

	"github.com/gtkit/gin-api/internal/runtime/resource"
	"github.com/gtkit/gin-api/internal/shared/env"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/yaml"
	koanfenv "github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/knadh/koanf/v2"
)

// configFile 是外置配置的固定相对路径。配置一律从磁盘读取，不进二进制、不进 git；
// 路径解析见 resource.ResolvePath（可执行文件同级的 config/env/ 优先）。
const configFile = "config/env/env.yml"

// envPrefix 是配置覆盖层的环境变量前缀。
//
// 该前缀是本服务**独占**的命名空间：以它开头却映射不到任何配置键的变量会让启动
// 失败（decode 的 ErrorUnused）。这是刻意的——这类变量最常见的成因是密钥变量名
// 拼错，忽略它会让服务带着 YAML 里的空密码正常起来，直到第一次连库才报"认证失败"，
// 而那条错误与真正的原因之间隔着一整条排查路径。
const envPrefix = "APP_"

// envReservedNames 是 `APP_` 前缀下已被本项目占用、不参与配置覆盖的变量名。
//
// `APP_ROOT` 指定项目根目录，`resource.ResolvePath`、`resource.WriteRoot` 与
// `validateLogConfig` 都读它，生产的 supervisor 配置里就有这一条
// （见 SCAFFOLD.md 的部署段）。不排除的话它会被映射成配置键 `root`，
// 撞上未知键 fail-fast——一个由本层引入、让所有既有部署直接起不来的回归。
//
// 新增 `APP_` 开头的进程级变量时必须同步登记到这里，否则它会以同样的方式
// 让启动失败，而错误信息指向的是"未知配置项"，与真正的原因隔着一层。
var envReservedNames = map[string]struct{}{
	"APP_ROOT": {},
}

// envKeyToConfigPath 把环境变量名转成配置键路径。
//
// 双下划线表示层级，单下划线保留为键名自身的一部分：
//
//	APP_DATABASE__PASSWORD         -> database.password
//	APP_DATABASE__MIGRATE_USERNAME -> database.migrate_username
//
// 不用单下划线分层：配置键本身大量使用它（read_header_timeout、migrate_username、
// key_hmac_secret），单下划线分层会把这些键切成不存在的路径，且启动失败时报出的
// 位置与真正的错误无关。
//
// 返回空串表示忽略该变量——koanf 的 env provider 据此跳过它。裸 `APP_` 映射不到
// 任何键，让它使整个进程起不来，与它可能只是环境里一个无关残留不相称。
func envKeyToConfigPath(name string) string {
	if _, reserved := envReservedNames[name]; reserved {
		return ""
	}
	trimmed := strings.TrimPrefix(name, envPrefix)
	if trimmed == "" {
		return ""
	}
	return strings.ReplaceAll(strings.ToLower(trimmed), "__", ".")
}

// Load 从磁盘固定路径 config/env/env.yml 读取 typed config。
// 找不到文件直接报错（fail-fast）：部署须显式提供该文件（参照 config/env.yml.example）。
func Load() (*schema.Config, error) {
	configPath := resource.ResolvePath(configFile)
	k := koanf.New(".")
	if err := k.Load(file.Provider(configPath), yaml.Parser()); err != nil {
		return nil, fmt.Errorf("read config file %s: %w", configPath, err)
	}

	cfg, err := decode(k)
	if err != nil {
		return nil, err
	}
	// 环境写在文件里，只能读完才知道是不是生产——校验因此后置：
	// 发现问题时文件确实已被读过一次，但启动失败仍然把问题拦在服务对外之前。
	if cfg.Env == env.Prod {
		if err := checkConfigFileSafety(configPath); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

// checkConfigFileSafety 校验生产配置文件的形态（config-validation spec）：
// 拒绝组/其他用户可写的权限位（任何本机进程都能改写安全开关与密钥引用），
// 拒绝符号链接（文件可在校验与读取之间被替换指向）。
func checkConfigFileSafety(path string) error {
	// #nosec G703 -- path 仅由部署方的 APP_ROOT/固定 configFile 或运行时资源解析器构造；不接受请求输入，允许部署到任意绝对目录。
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect config file %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("config file %s is a symlink; in production the config must be "+
			"a regular file so its target cannot be swapped underneath the process", path)
	}
	if perm := info.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("config file %s is writable by group or others (mode %04o); "+
			"in production tighten it (e.g. chmod 0600) so no other local process can "+
			"rewrite credentials or security switches", path, perm)
	}
	return nil
}

// loadFromReader 从内存字节流加载配置，供测试以内联 YAML 驱动（不依赖磁盘文件）。
func loadFromReader(r io.Reader) (*schema.Config, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read config bytes: %w", err)
	}

	k := koanf.New(".")
	if err := k.Load(rawbytes.Provider(raw), yaml.Parser()); err != nil {
		return nil, fmt.Errorf("parse config bytes: %w", err)
	}

	return decode(k)
}

func decode(k *koanf.Koanf) (*schema.Config, error) {
	// 环境变量覆盖层：在 YAML 之后加载，同名键以环境变量为准。
	//
	// 放在 decode 里而不是各调用点：Load（读磁盘）与 loadFromReader（读字节流）
	// 都汇合到这里，两条路径因此拿到同一套覆盖语义，测试覆盖的也就是生产走的那条。
	//
	// parser 传 nil：env provider 通过 Read() 直接产出 map，不经字节解析
	// （见 koanf.Load 对 nil parser 的分支）。
	// koanfenv 是取了别名的 koanf provider：它的包名也是 env，与本仓的 shared/env
	// 撞名，而后者是本包 schema.Config.Env 的类型来源，不为一个只用一次的 provider 让路。
	if err := k.Load(koanfenv.Provider(envPrefix, ".", envKeyToConfigPath), nil); err != nil {
		// 错误里只提前缀，不带任何变量值——这一层承载的正是密钥。
		return nil, fmt.Errorf("load %s environment overrides: %w", envPrefix, err)
	}

	var cfg schema.Config
	// 配置结构体沿用 mapstructure tag，koanf 默认读 koanf tag，必须显式指定。
	// cfg.Env 由顶层 env 字段（mapstructure:"env"）直接读入。
	//
	// DecoderConfig 复刻 koanf 缺省行为（WeaklyTypedInput + duration/text hooks，
	// 见 koanf v2 UnmarshalWithConf 源码），仅追加 ErrorUnused：yaml 中存在而
	// 结构体不认识的键（拼写错误、遗留死配置）直接启动失败，不再静默用默认值。
	if err := k.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{
		Tag: "mapstructure",
		DecoderConfig: &mapstructure.DecoderConfig{
			DecodeHook: mapstructure.ComposeDecodeHookFunc(
				mapstructure.StringToTimeDurationHookFunc(),
				mapstructure.TextUnmarshallerHookFunc(),
			),
			WeaklyTypedInput: true,
			ErrorUnused:      true,
		},
	}); err != nil {
		return nil, fmt.Errorf("decode config (存在未知配置项，检查拼写或删除): %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return &cfg, nil
}
