package config

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/appmeta"
)

// minimalConfigYAML 返回一份能通过 Validate 的最小配置。
//
// 环境变量覆盖的用例都以它为基线：断言的是"某个键被env覆盖了"，
// 因此基线本身必须是合法的，否则失败原因会混进校验错误里分不清。
func minimalConfigYAML() string {
	return fmt.Sprintf(`
env: dev
auth:
  mode: external
application:
  host: localhost
  port: "8008"
  name: %s
  domain: "https://example.com"
  ishttps: false
  read_header_timeout: 2
  read_timeout: 30
  timezone: "Asia/Shanghai"
database:
  dbtype: mysql
  host: 127.0.0.1
  port: "3306"
  name: sleep
  username: root
  password: ""
  maxopenconn: 25
  maxidleconn: 10
  maxlifeseconds: 1800
email:
  host: smtp.example.com
  port: 587
  user: mailer
  pass: secret
  from: noreply@example.com
  from_name: gin-api
  alert_to: ["ops@example.com"]
sms:
  provider: aliyun
  access_id: access-id
  access_secret: access-secret
  sign_name: sign-name
  templates:
    alert:
      code: SMS_123
  alert_phones: ["13800000000"]
  alert_template: alert
log:
  level: debug
  path: ./logs/app_client
  sql: 1
  consolestdout: 1
  filestdout: 1
redis:
  addr: 127.0.0.1:6379
  dbs: [0,1,2]
jwt:
  active:
    kid: k2026a
    private_key_path: config/pem/k2026a.private.pem
    public_key_path: config/pem/k2026a.public.pem
  issuer: gin-api
  audiences: ["app"]
  token_ttl_hours: 720
ssl:
  key: keystring
  pem: temp/pem.pem
news:
  fsurl: ""
`, appmeta.CommandName)
}

// 以下用例一律不加 t.Parallel()：t.Setenv 与并行测试互斥（会 panic），
// 这是 Go 测试框架的硬约束，不是可以省略的写法偏好。

// TestEnvOverrideFillsEmptyValue 覆盖规划场景 1：YAML 留空、环境变量供值。
//
// 这是该能力存在的理由——密钥不必明文落在磁盘配置里。
func TestEnvOverrideFillsEmptyValue(t *testing.T) {
	t.Setenv("APP_DATABASE__PASSWORD", "from-env")

	cfg, err := loadFromReader(strings.NewReader(minimalConfigYAML()))
	if err != nil {
		t.Fatalf("loadFromReader returned error: %v", err)
	}
	if cfg.Database.Password != "from-env" {
		t.Fatalf("database.password = %q, want the environment value", cfg.Database.Password)
	}
}

// TestEnvOverrideReplacesExistingValue 覆盖规划场景 2：环境变量优先于 YAML 既有值。
func TestEnvOverrideReplacesExistingValue(t *testing.T) {
	t.Setenv("APP_DATABASE__USERNAME", "env-user")

	cfg, err := loadFromReader(strings.NewReader(minimalConfigYAML()))
	if err != nil {
		t.Fatalf("loadFromReader returned error: %v", err)
	}
	if cfg.Database.Username != "env-user" {
		t.Fatalf("database.username = %q, want the environment value to win over YAML", cfg.Database.Username)
	}
}

// TestEnvOverrideAbsentKeepsYAML 覆盖规划场景 3：不设环境变量时行为不变。
//
// 这条是整个能力的兼容性底线：既有部署不设任何 APP_ 变量，加载结果必须与从前一致。
func TestEnvOverrideAbsentKeepsYAML(t *testing.T) {
	cfg, err := loadFromReader(strings.NewReader(minimalConfigYAML()))
	if err != nil {
		t.Fatalf("loadFromReader returned error: %v", err)
	}
	if cfg.Database.Username != "root" {
		t.Fatalf("database.username = %q, want the YAML value %q", cfg.Database.Username, "root")
	}
	if cfg.Database.Password != "" {
		t.Fatalf("database.password = %q, want the YAML value (empty)", cfg.Database.Password)
	}
}

// TestUnprefixedEnvIsIgnored 覆盖规划场景 4：无 APP_ 前缀的变量不参与。
//
// 环境里叫 DATABASE__PASSWORD 的变量可能属于任何其它软件，
// 让它影响本服务的配置会造成难以追查的串扰。
func TestUnprefixedEnvIsIgnored(t *testing.T) {
	t.Setenv("DATABASE__PASSWORD", "not-mine")

	cfg, err := loadFromReader(strings.NewReader(minimalConfigYAML()))
	if err != nil {
		t.Fatalf("loadFromReader returned error: %v", err)
	}
	if cfg.Database.Password != "" {
		t.Fatalf("database.password = %q, want empty: an unprefixed variable must not apply",
			cfg.Database.Password)
	}
}

// TestEnvOverrideKeepsSingleUnderscoreInKey 钉住单下划线不被当作层级分隔。
//
// 配置键本身大量使用单下划线。若以单下划线分层，这个变量会被映射成
// database.migrate.username——一个不存在的路径，启动会以一条指向错误位置的
// 信息失败，而使用者看不出真正的问题在哪。
func TestEnvOverrideKeepsSingleUnderscoreInKey(t *testing.T) {
	t.Setenv("APP_DATABASE__MIGRATE_USERNAME", "migrator")

	cfg, err := loadFromReader(strings.NewReader(minimalConfigYAML()))
	if err != nil {
		t.Fatalf("loadFromReader returned error: %v", err)
	}
	if cfg.Database.MigrateUsername != "migrator" {
		t.Fatalf("database.migrate_username = %q, want the environment value",
			cfg.Database.MigrateUsername)
	}
}

// TestUnknownEnvOverrideFailsFast 钉住拼错的 APP_ 变量导致启动失败。
//
// 忽略它的代价：服务带着 YAML 里的空密码正常起来，直到第一次连库才报"认证失败"，
// 而那条错误与真正的原因（变量名拼错）之间隔着一整条排查路径。
func TestUnknownEnvOverrideFailsFast(t *testing.T) {
	const secretValue = "super-secret-value"
	t.Setenv("APP_DATABSE__PASSWORD", secretValue) // 故意拼错 DATABASE

	_, err := loadFromReader(strings.NewReader(minimalConfigYAML()))
	if err == nil {
		t.Fatal("loadFromReader accepted an APP_ variable that maps to no config key")
	}
	// 断言错误指向拼错的那个键，而不是只断言"有错误"——后者任何失败原因都能满足，
	// 包括这条 fail-fast 根本没生效、而配置因为别的原因没通过校验。
	if !strings.Contains(err.Error(), "databse") {
		t.Fatalf("error must name the offending key so the typo is findable, got: %v", err)
	}
	// 该层承载的正是密钥：错误信息绝不能把值带出去。
	if strings.Contains(err.Error(), secretValue) {
		t.Fatalf("error message leaked the variable value: %v", err)
	}
}

// TestAppRootDoesNotBreakStartup 钉住 APP_ROOT 不参与配置覆盖。
//
// APP_ROOT 是项目根目录变量，生产的 supervisor 配置里就有
// （SCAFFOLD.md 部署段：environment=APP_ROOT="/home/www/gin-api"）。
// 它以 APP_ 开头，若参与覆盖会被映射成配置键 root 并撞上未知键 fail-fast——
// 那将是一个让所有既有部署直接起不来的回归，且错误信息说的是"未知配置项"，
// 指不回真正的原因。
func TestAppRootDoesNotBreakStartup(t *testing.T) {
	t.Setenv("APP_ROOT", "/home/www/gin-api")

	if _, err := loadFromReader(strings.NewReader(minimalConfigYAML())); err != nil {
		t.Fatalf("APP_ROOT must not participate in config override: %v", err)
	}
	if got := envKeyToConfigPath("APP_ROOT"); got != "" {
		t.Fatalf("envKeyToConfigPath(APP_ROOT) = %q, want empty so the provider skips it", got)
	}
}

// TestDocumentedSecretOverridesAllResolve 钉住文档里列出的密钥变量确实可用。
//
// 这些变量名同时出现在 CONFIG_SECRET_MANAGEMENT_PLAN.md 与 config/env.yml.example 中。
// 没有这条测试，文档里写错一个键名不会有任何信号——直到某次上线时密钥没注入，
// 而 fail-fast 只会说"未知配置项"，看不出是文档抄错还是使用者写错。
//
// 判据是"不因未知键失败"：逐个断言具体字段会让这条测试随配置结构演进反复改动，
// 而它要防的是键名拼写，不是取值传递（后者已由前面的用例覆盖）。
func TestDocumentedSecretOverridesAllResolve(t *testing.T) {
	documented := []string{
		"APP_DATABASE__PASSWORD",
		"APP_DATABASE__MIGRATE_PASSWORD",
		"APP_REDIS__PASSWORD",
		"APP_EMAIL__PASS",
		"APP_SMS__ACCESS_SECRET",
		"APP_RATELIMIT__KEY_HMAC_SECRET",
	}

	for _, name := range documented {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "x")

			_, err := loadFromReader(strings.NewReader(minimalConfigYAML()))
			if err != nil && strings.Contains(err.Error(), "invalid keys") {
				t.Fatalf("%s maps to no config key; the documented name is wrong: %v", name, err)
			}
		})
	}
}

// TestEnvKeyToConfigPath 直接覆盖键名转换的各条分支。
func TestEnvKeyToConfigPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "single level", input: "APP_ENV", want: "env"},
		{name: "two levels", input: "APP_DATABASE__PASSWORD", want: "database.password"},
		{name: "three levels", input: "APP_JWT__ACTIVE__KID", want: "jwt.active.kid"},
		{
			name:  "single underscore stays in the key name",
			input: "APP_DATABASE__MIGRATE_USERNAME",
			want:  "database.migrate_username",
		},
		{
			name:  "mixed case is normalised",
			input: "APP_Database__Password",
			want:  "database.password",
		},
		{name: "bare prefix is ignored", input: "APP_", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := envKeyToConfigPath(tt.input); got != tt.want {
				t.Fatalf("envKeyToConfigPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
