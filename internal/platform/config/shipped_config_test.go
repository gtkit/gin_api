package config

import (
	"os"
	"path/filepath"
	"testing"
)

// shippedConfigPath 是仓库自带的示例配置，相对本包目录。
const shippedConfigPath = "../../../config/env/env.yml"

// TestShippedConfigLoadsAndValidates 钉住仓库自带的配置能被自己的加载器接受。
//
// 这条此前是一个真实的上线阻断：配置结构在密钥轮换、超时策略、连接池校验
// 几个批次里改过，而 `config/env/env.yml` 一次都没跟着改。结果是
// **照仓库自带的配置启动，进程在严格解码阶段直接退出**——而这是脚手架，
// 使用者拿到的第一份配置就是坏的。
//
// 严格解码只报第一个未知键，因此当时的表象只有一条 `jwt.legacy_laravel`；
// 修掉它才暴露出后面 8 条（废弃的 writertimeout、连接池上下限倒挂、
// 单密钥字段已被 kid 结构取代、issuer/audiences 缺失）。一次只看见一条，
// 让人误以为改一行就完事了。
//
// 放在测试里而不是单独脚本：它随 `go test` 一起跑，改配置结构的人在本地
// 就会看到红，不必等到部署。
func TestShippedConfigLoadsAndValidates(t *testing.T) {
	t.Parallel()

	file, err := os.Open(filepath.Clean(shippedConfigPath))
	if err != nil {
		t.Fatalf("open shipped config: %v", err)
	}
	defer func() { _ = file.Close() }()

	cfg, err := loadFromReader(file)
	if err != nil {
		t.Fatalf("the shipped config/env/env.yml does not decode: %v\n"+
			"strict decoding reports only the first unknown key — fix it and run again "+
			"to see whether more keys drifted.", err)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("the shipped config/env/env.yml fails validation: %v", err)
	}
}

// TestShippedConfigDeclaresEveryRequiredScope 钉住示例配置带齐必填的作用域声明。
//
// 单独断言而不是依赖 Validate：这几项是**安全相关**的必填，而 Validate 的
// 错误信息会随实现演进。一个把 issuer 校验意外放宽的改动，会让上面那条测试
// 继续通过，而这条会失败。
func TestShippedConfigDeclaresEveryRequiredScope(t *testing.T) {
	t.Parallel()

	file, err := os.Open(filepath.Clean(shippedConfigPath))
	if err != nil {
		t.Fatalf("open shipped config: %v", err)
	}
	defer func() { _ = file.Close() }()

	cfg, err := loadFromReader(file)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if cfg.JWT.Issuer == "" {
		t.Error("jwt.issuer is empty: a token that does not declare its issuer cannot be scoped")
	}
	if len(cfg.JWT.Audiences) == 0 {
		t.Error("jwt.audiences is empty: a token issued for one service could be replayed against another")
	}
	if cfg.JWT.Active.KID == "" {
		t.Error("jwt.active.kid is empty: tokens would not say which key signed them")
	}
	if cfg.JWT.Active.PrivateKeyPath == "" || cfg.JWT.Active.PublicKeyPath == "" {
		t.Error("jwt.active key paths are empty: the service could not sign or verify anything")
	}
}
