package config

import (
	"fmt"
	"io"
	"os"

	"github.com/gtkit/gin-api/internal/runtime/resource"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/knadh/koanf/v2"
)

// configFile 是外置配置的固定相对路径。配置一律从磁盘读取，不进二进制、不进 git；
// 路径解析见 resource.ResolvePath（可执行文件同级的 config/env/ 优先）。
const configFile = "config/env/env.yml"

// Load 从磁盘固定路径 config/env/env.yml 读取 typed config。
// 找不到文件直接报错（fail-fast）：部署须显式提供该文件（参照 config/env.yml.example）。
func Load() (*Config, error) {
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
	if cfg.Env == EnvProd {
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
func loadFromReader(r io.Reader) (*Config, error) {
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

func decode(k *koanf.Koanf) (*Config, error) {
	var cfg Config
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
