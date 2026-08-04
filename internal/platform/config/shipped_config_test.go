package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shippedExamplePath 是**随仓库分发**的配置模板，相对本包目录。
//
// 刻意不是 `config/env/env.yml`：那份在 .gitignore 里，是每台机器自己的运行
// 配置，装着本机的密钥路径。测试读它会让门禁在任何新克隆、CI 与派生项目上
// 直接失败——文件根本不存在。
//
// 交付给使用者的是这份 example，因此它才是需要被门禁盯住的那个。
const shippedExamplePath = "../../../config/env.yml.example"

// deploymentPlaceholders 是 example 里刻意留空、必须由部署方填写的字段。
//
// 留空是对的：密钥标识、密钥路径与令牌作用域都是每个部署各不相同的事实，
// 模板替使用者填一个假值反而会被照抄上线。测试在**内存里**补上它们，
// 只为让配置能走完校验，不触碰磁盘上的模板。
//
// 这张表本身也是断言：example 里这些行一旦改名或消失，替换会落空，
// 测试立刻报出模板与代码已经漂移。
var deploymentPlaceholders = []struct{ from, to string }{
	{`kid: ""`, `kid: "test-kid"`},
	{`private_key_path: ""           # 【文件挂载】0600`, `private_key_path: "config/pem/t.private.pem"`},
	{`public_key_path: ""            # 【文件挂载】`, `public_key_path: "config/pem/t.public.pem"`},
	{`issuer: ""`, `issuer: "gin-api-test"`},
	{`audiences: []`, `audiences: ["gin-api-client"]`},
}

// TestShippedExampleStaysInSyncWithTheCode 钉住分发的配置模板不与代码漂移。
//
// 严格解码是核心：结构体不认识的键（改名后的遗留项、拼写错误）在解码阶段
// 直接失败。漂移恰恰以这种形式出现——配置结构改了名而模板没跟上，
// 使用者拿到手第一次启动就退出。
//
// 严格解码只报**第一个**未知键，一次只暴露一处；修掉一处后必须重跑，
// 而不是以为改一行就完事。
func TestShippedExampleStaysInSyncWithTheCode(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Clean(shippedExamplePath))
	if err != nil {
		t.Fatalf("read shipped example: %v", err)
	}

	filled := string(raw)
	for _, p := range deploymentPlaceholders {
		if !strings.Contains(filled, p.from) {
			t.Fatalf("the example no longer contains %q: the field was renamed (update this "+
				"table) or removed (the template drifted from the code)", p.from)
		}
		filled = strings.Replace(filled, p.from, p.to, 1)
	}

	cfg, err := loadFromReader(strings.NewReader(filled))
	if err != nil {
		t.Fatalf("config/env.yml.example does not load: %v\n"+
			"strict decoding reports only the first unknown key — fix it and run again "+
			"to see whether more keys drifted.", err)
	}

	// 单独查这几项而不是只靠上一步：它们是安全相关的必填，而校验规则会随
	// 实现演进。一个意外放宽了 issuer 校验的改动会让上一步继续通过，
	// 这里则会失败。
	if cfg.JWT.Issuer == "" {
		t.Error("jwt.issuer did not survive decoding")
	}
	if len(cfg.JWT.Audiences) == 0 {
		t.Error("jwt.audiences did not survive decoding")
	}
	if cfg.JWT.Active.KID == "" {
		t.Error("jwt.active.kid did not survive decoding")
	}
}

// TestShippedExampleShipsNoUsableCredentials 钉住模板不携带可直接使用的凭据。
//
// 模板里预填一个"能跑起来"的密钥，最可能的结果是它被原样带上生产。留空则会在
// 启动期失败，逼使用者显式提供自己的值——而那正是希望发生的事。
func TestShippedExampleShipsNoUsableCredentials(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Clean(shippedExamplePath))
	if err != nil {
		t.Fatalf("read shipped example: %v", err)
	}

	_, err = loadFromReader(strings.NewReader(string(raw)))
	if err == nil {
		t.Fatal("the example passed validation as-is: it must leave deployment-specific " +
			"credentials empty so that starting without filling them fails loudly")
	}

	// 错误必须点名待填项，否则使用者不知道该补什么。
	for _, want := range []string{"jwt.active.kid", "jwt.issuer", "jwt.audiences"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the startup error does not tell the operator to fill %q", want)
		}
	}
}
