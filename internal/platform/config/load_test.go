package config

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/appmeta"
)

func TestLoadFromDisk(t *testing.T) {
	clearConfigEnv(t)
	root := t.TempDir()
	t.Setenv("APP_ROOT", root)
	writeJWTCommandConfig(t, root, minimalConfigYAML())
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.App.Port != "8008" {
		t.Fatalf("application.port = %q, want disk configuration 8008", cfg.App.Port)
	}
}

func TestLoadFromReader(t *testing.T) {
	t.Parallel()

	cfg, err := loadFromReader(strings.NewReader(fmt.Sprintf(`
env: dev
auth:
  mode: external
application:
  host: localhost
  port: "8008"
  name: %s
  domain: "https://anshuiroom.com"
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
`, appmeta.CommandName)))
	if err != nil {
		t.Fatalf("loadFromReader returned error: %v", err)
	}

	if cfg.Env != "dev" {
		t.Fatalf("cfg.Env = %q, want dev", cfg.Env)
	}
	if cfg.App.Name != appmeta.CommandName {
		t.Fatalf("cfg.App.Name = %q", cfg.App.Name)
	}
	if cfg.Database.DBType != "mysql" {
		t.Fatalf("cfg.Database.DBType = %q", cfg.Database.DBType)
	}
	if len(cfg.Redis.DBs) != 3 {
		t.Fatalf("cfg.Redis.DBs len = %d, want 3", len(cfg.Redis.DBs))
	}
	if !cfg.Email.Enabled() || cfg.Email.From != "noreply@example.com" || cfg.Email.Port != 587 {
		t.Fatalf("cfg.Email = %+v", cfg.Email)
	}
	if !slices.Equal(cfg.Email.AlertTo, []string{"ops@example.com"}) {
		t.Fatalf("cfg.Email.AlertTo = %v", cfg.Email.AlertTo)
	}
	if !cfg.SMS.Enabled() || cfg.SMS.Provider != "aliyun" || cfg.SMS.AccessID != "access-id" {
		t.Fatalf("cfg.SMS = %+v", cfg.SMS)
	}
	if tpl, ok := cfg.SMS.Templates["alert"]; !ok || tpl.Code != "SMS_123" {
		t.Fatalf("cfg.SMS.Templates = %+v", cfg.SMS.Templates)
	}
	if cfg.SMS.AlertTemplate != "alert" || !slices.Equal(cfg.SMS.AlertPhones, []string{"13800000000"}) {
		t.Fatalf("cfg.SMS alert channel = %+v", cfg.SMS)
	}
}

func TestLoadFromReaderRequiresEnv(t *testing.T) {
	t.Parallel()

	// 缺少顶层 env 字段时 Validate 应直接报错（fail-fast）。
	_, err := loadFromReader(strings.NewReader(`
application:
  name: app_client
  host: localhost
  port: "8008"
`))
	if err == nil {
		t.Fatal("loadFromReader expected error when env is missing, got nil")
	}
}

func TestLoadFromReaderRejectsUnknownKeys(t *testing.T) {
	t.Parallel()

	// 最小合法基座；每个用例往里注入一个未知键。
	base := `
env: dev
auth:
  mode: external
application:
  name: app_client
  host: localhost
  port: "8008"
database:
  dbtype: mysql
  host: 127.0.0.1
  port: "3306"
  name: sleep
  username: root
redis:
  addr: 127.0.0.1:6379
  dbs: [0,1,2]
log:
  level: info
  path: ./logs/app_client
`

	// jwtBlock 是通过校验所需的最小 jwt 段。
	//
	// 它不放进 base：有两个用例要在 jwt 段内部注入拼写错误的键，
	// 而 YAML 不允许同一层出现两个 `jwt:`。让这些用例自带完整段落，
	// 比在 base 里塞一个"可被覆盖"的占位更清楚。
	jwtBlock := `jwt:
  active:
    kid: k2026a
    private_key_path: config/pem/k2026a.private.pem
    public_key_path: config/pem/k2026a.public.pem
  issuer: gin-api
  audiences: ["app"]
`

	tests := []struct {
		name    string
		extra   string
		wantKey string
	}{
		{
			name:    "unknown top-level section rejected",
			extra:   "unknown_section:\n  foo: bar\n",
			wantKey: "unknown_section",
		},
		{
			name:    "laravel jwt.secret rejected",
			extra:   "jwt:\n  active:\n    kid: k\n    private_key_path: a\n    public_key_path: b\n  issuer: i\n  audiences: [\"app\"]\n  secret: laravel\n",
			wantKey: "secret",
		},
		{
			name:    "typo token_ttl_hour rejected",
			extra:   "jwt:\n  active:\n    kid: k\n    private_key_path: a\n    public_key_path: b\n  issuer: i\n  audiences: [\"app\"]\n  token_ttl_hour: 24\n",
			wantKey: "token_ttl_hour",
		},
		{
			// 已移除的死配置段（pay/llm/baidu/aliyun）必须以未知键失败，
			// 而不是静默接受：fail-closed 迁移的负向证明。
			name:    "removed pay section rejected",
			extra:   "pay:\n  wechat:\n    enabled: true\n",
			wantKey: "pay",
		},
		{
			name:    "removed aliyun flat keys rejected",
			extra:   "aliyun:\n  sms_access_id: access-id\n",
			wantKey: "aliyun",
		},
		{
			name:    "removed ai module rejected",
			extra:   "modules:\n  ai:\n    default_provider: deepseek\n",
			wantKey: "modules",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			input := base + tt.extra
			// 未在 extra 里自带 jwt 段的用例，补上最小合法段。
			if !strings.Contains(tt.extra, "jwt:") {
				input = base + jwtBlock + tt.extra
			}
			_, err := loadFromReader(strings.NewReader(input))
			if err == nil {
				t.Fatalf("loadFromReader expected unknown-key error for %q, got nil", tt.wantKey)
			}
			if !strings.Contains(err.Error(), tt.wantKey) {
				t.Fatalf("error = %q, want containing key %q", err.Error(), tt.wantKey)
			}
		})
	}
}

func TestLoadFromReaderRejectsLegacyConfigKeys(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, section, key, value string
	}{
		{"readtimeout", "application", "readtimeout", "2"},
		{"zero readtimeout", "application", "readtimeout", "0"},
		{"writertimeout", "application", "writertimeout", "2"},
		{"zero writertimeout", "application", "writertimeout", "0"},
		{"private key", "jwt", "private_key_path", "retired.private.pem"},
		{"empty private key", "jwt", "private_key_path", `""`},
		{"public key", "jwt", "public_key_path", "retired.public.pem"},
		{"empty public key", "jwt", "public_key_path", `""`},
		{"user module", "modules", "user", "{page_size: 20}"},
		{"empty user module", "modules", "user", "{}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			section := tc.section + ":\n"
			input := minimalConfigYAML()
			if !strings.Contains(input, section) {
				input += "\n" + section
			}
			input = strings.Replace(input, section, section+"  "+tc.key+": "+tc.value+"\n", 1)

			wantKey := tc.key
			if tc.section == "modules" {
				wantKey = tc.section
			}
			_, err := loadFromReader(strings.NewReader(input))
			if err == nil || !strings.Contains(err.Error(), "decode config") || !strings.Contains(err.Error(), wantKey) {
				t.Fatalf("expected unknown-key decode error for %s.%s, got %v", tc.section, tc.key, err)
			}
		})
	}
}

func TestLoadRejectsRemovedAIEnvironmentOverride(t *testing.T) {
	clearConfigEnv(t)
	root := t.TempDir()
	t.Setenv("APP_ROOT", root)
	writeJWTCommandConfig(t, root, minimalConfigYAML())
	t.Setenv("APP_MODULES__AI__PROVIDERS__DEEPSEEK__API_KEY", "removed-provider-key")

	if _, err := Load(); err == nil {
		t.Fatal("removed AI environment override accepted")
	}
}
