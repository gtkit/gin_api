package config

import (
	"fmt"
	"io"

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

	return decode(k)
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

// aliyunSMSFlatKeys 是 aliyun 段下允许的扁平 key 白名单：
// 结构体侧标了 mapstructure:"-"，严格解码前先读出并移除、解码后手动组装。
var aliyunSMSFlatKeys = []string{
	"aliyun.sms_access_id",
	"aliyun.sms_access_secret",
	"aliyun.sms_sign_name",
	"aliyun.sms_template_code",
}

func decode(k *koanf.Koanf) (*Config, error) {
	// aliyun.SMS 在 yml 里是扁平 key（aliyun.sms_access_id 等），与嵌套 struct 不匹配；
	// 白名单内的键先读出再从 koanf 删除，这样 aliyun 段下其余拼写错误仍会被严格模式抓住。
	aliyunSMS := AliyunSMSConfig{
		AccessID:     k.String("aliyun.sms_access_id"),
		AccessSecret: k.String("aliyun.sms_access_secret"),
		SignName:     k.String("aliyun.sms_sign_name"),
		TemplateCode: k.String("aliyun.sms_template_code"),
	}
	for _, key := range aliyunSMSFlatKeys {
		k.Delete(key)
	}

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

	cfg.Aliyun.SMS = aliyunSMS

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return &cfg, nil
}
