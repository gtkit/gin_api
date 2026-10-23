package config

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gtkit/gin-api/internal/shared/env"

	"github.com/knadh/koanf/parsers/yaml"
	koanfenv "github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// LoadJWTKeyConfigForCommand 只读取生成密钥需要的 jwt.active，不初始化应用或外部服务。
// env 文件和相对密钥路径均基于 APP_ROOT，未设置时基于工作目录；不依赖可执行文件位置。
// 两条路径必须显式非空，返回绝对路径；调用者负责 kid 等具体命令约束。
func LoadJWTKeyConfigForCommand() (JWTKeyConfig, error) {
	root, err := filepath.Abs(cmp.Or(os.Getenv("APP_ROOT"), "."))
	if err != nil {
		return JWTKeyConfig{}, fmt.Errorf("resolve JWT command root: %w", err)
	}
	k := koanf.New(".")
	if loadErr := k.Load(file.Provider(filepath.Join(root, configFile)), yaml.Parser()); loadErr != nil {
		return JWTKeyConfig{}, fmt.Errorf("read JWT command config: %w", loadErr)
	}
	// 与运行时保持 APP_ 覆盖及生产配置文件安全约束一致。
	if loadErr := k.Load(koanfenv.Provider(envPrefix, ".", envKeyToConfigPath), nil); loadErr != nil {
		return JWTKeyConfig{}, fmt.Errorf("load JWT environment overrides: %w", loadErr)
	}
	if k.String("env") == string(env.Prod) {
		if safetyErr := checkConfigFileSafety(filepath.Join(root, configFile)); safetyErr != nil {
			return JWTKeyConfig{}, fmt.Errorf("check JWT command config: %w", safetyErr)
		}
	}
	var keys JWTKeyConfig
	if decodeErr := k.UnmarshalWithConf("jwt.active", &keys, koanf.UnmarshalConf{Tag: "mapstructure"}); decodeErr != nil {
		return JWTKeyConfig{}, fmt.Errorf("decode jwt.active: %w", decodeErr)
	}
	keys.PrivateKeyPath = strings.TrimSpace(keys.PrivateKeyPath)
	keys.PublicKeyPath = strings.TrimSpace(keys.PublicKeyPath)
	if keys.PrivateKeyPath == "" || keys.PublicKeyPath == "" {
		return JWTKeyConfig{}, errors.New("jwt.active.private_key_path and public_key_path are required when no output directory is supplied")
	}
	if !filepath.IsAbs(keys.PrivateKeyPath) {
		keys.PrivateKeyPath = filepath.Join(root, keys.PrivateKeyPath)
	}
	if !filepath.IsAbs(keys.PublicKeyPath) {
		keys.PublicKeyPath = filepath.Join(root, keys.PublicKeyPath)
	}
	if filepath.Clean(keys.PrivateKeyPath) == filepath.Clean(keys.PublicKeyPath) {
		return JWTKeyConfig{}, errors.New("JWT private and public key paths must differ")
	}
	return keys, nil
}
