package config

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/appmeta"
)

func TestLoadFromReader(t *testing.T) {
	t.Parallel()

	cfg, err := loadFromReader(strings.NewReader(fmt.Sprintf(`
env: dev
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
sms:
  enabled: true
pay:
  wechat:
    enabled: true
    app_id: wx-app-id
    mch_id: mch-id
    mch_cert_serial_number: cert-serial
    mch_apiv3_key: apiv3-key
    mch_private_key_pem: |
      -----BEGIN PRIVATE KEY-----
      test
      -----END PRIVATE KEY-----
    wechatpay_certificate_pem: |
      -----BEGIN CERTIFICATE-----
      test
      -----END CERTIFICATE-----
  alipay:
    enabled: true
    app_id: alipay-app-id
    private_key: test-private-key
    alipay_public_key: test-public-key
    is_production: false
aliyun:
  sms_access_id: access-id
  sms_access_secret: access-secret
  sms_sign_name: sign-name
  sms_template_code: template-code
baidu:
  voice:
    appid: voice-app-id
    apikey: voice-api-key
    secretkey: voice-secret-key
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
llm:
  providers:
    - name: qianfan
      api_key: test-api-key
      models:
        - model-a
        - model-b
  redis_db: 1
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
	if !cfg.SMS.Enabled {
		t.Fatal("cfg.SMS.Enabled = false, want true")
	}
	if !cfg.Pay.Wechat.Enabled || cfg.Pay.Wechat.AppID != "wx-app-id" {
		t.Fatalf("cfg.Pay.Wechat = %+v", cfg.Pay.Wechat)
	}
	if !cfg.Pay.Alipay.Enabled || cfg.Pay.Alipay.AppID != "alipay-app-id" {
		t.Fatalf("cfg.Pay.Alipay = %+v", cfg.Pay.Alipay)
	}
	if cfg.Aliyun.SMS.AccessID != "access-id" || cfg.Aliyun.SMS.TemplateCode != "template-code" {
		t.Fatalf("cfg.Aliyun.SMS = %+v", cfg.Aliyun.SMS)
	}
	if cfg.Baidu.Voice.APIKey != "voice-api-key" || cfg.Baidu.Voice.SecretKey != "voice-secret-key" {
		t.Fatalf("cfg.Baidu.Voice = %+v", cfg.Baidu.Voice)
	}
	if len(cfg.LLM.Providers) != 1 {
		t.Fatalf("cfg.LLM.Providers len = %d, want 1", len(cfg.LLM.Providers))
	}
	if p := cfg.LLM.Providers[0]; p.Name != "qianfan" || p.APIKey != "test-api-key" ||
		!slices.Equal(p.Models, []string{"model-a", "model-b"}) {
		t.Fatalf("cfg.LLM.Providers[0] = %+v", p)
	}
	if cfg.LLM.RedisDB != 1 {
		t.Fatalf("cfg.LLM.RedisDB = %d, want 1", cfg.LLM.RedisDB)
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
			name:    "typo inside aliyun rejected",
			extra:   "aliyun:\n  sms_acess_id: oops\n",
			wantKey: "sms_acess_id",
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

	// 合法的 aliyun 扁平键不受严格模式影响（白名单先读后删）。
	cfg, err := loadFromReader(strings.NewReader(base + jwtBlock + `aliyun:
  sms_access_id: access-id
  sms_access_secret: access-secret
  sms_sign_name: sign-name
  sms_template_code: template-code
`))
	if err != nil {
		t.Fatalf("loadFromReader returned error for legal aliyun flat keys: %v", err)
	}
	if cfg.Aliyun.SMS.AccessID != "access-id" || cfg.Aliyun.SMS.TemplateCode != "template-code" {
		t.Fatalf("cfg.Aliyun.SMS = %+v", cfg.Aliyun.SMS)
	}
}

func TestValidateRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Env: "dev",
		App: AppConfig{
			Name:                     appmeta.CommandName,
			Host:                     "localhost",
			Port:                     "not-a-port",
			ReadHeaderTimeoutSeconds: -1,
			WriteTimeoutSeconds:      -1,
			Timezone:                 "Mars/Nowhere",
		},
		Database: DatabaseConfig{
			DBType: "",
		},
		Redis: RedisConfig{},
		Log:   LogConfig{},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate expected error, got nil")
	}
}

func TestValidateProdLogPathLocation(t *testing.T) {
	appRoot := t.TempDir()
	outsideRoot := t.TempDir()

	tests := []struct {
		name        string
		env         Env
		appRoot     string
		logPath     string
		wantErrText string
	}{
		{
			name:    "prod relative path allowed without app root",
			env:     EnvProd,
			logPath: "./logs/app_client",
		},
		{
			name:    "dev absolute path allowed without app root",
			env:     EnvDev,
			logPath: filepath.Join(outsideRoot, "logs", "app_client"),
		},
		{
			name:    "prod absolute path under app root allowed",
			env:     EnvProd,
			appRoot: appRoot,
			logPath: filepath.Join(appRoot, "logs", "app_client"),
		},
		{
			name:        "prod absolute path without app root rejected",
			env:         EnvProd,
			logPath:     filepath.Join(appRoot, "logs", "app_client"),
			wantErrText: "APP_ROOT",
		},
		{
			name:        "prod absolute path with relative app root rejected",
			env:         EnvProd,
			appRoot:     "relative-root",
			logPath:     filepath.Join(appRoot, "logs", "app_client"),
			wantErrText: "APP_ROOT must be absolute",
		},
		{
			name:        "prod absolute path outside app root rejected",
			env:         EnvProd,
			appRoot:     appRoot,
			logPath:     filepath.Join(outsideRoot, "logs", "app_client"),
			wantErrText: "must be under APP_ROOT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("APP_ROOT", tt.appRoot)

			cfg := validConfigForTest()
			cfg.Env = tt.env
			cfg.Log.Path = tt.logPath

			err := cfg.Validate()
			if tt.wantErrText == "" {
				if err != nil {
					t.Fatalf("Validate returned error: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("Validate expected error containing %q, got nil", tt.wantErrText)
			}
			if !strings.Contains(err.Error(), tt.wantErrText) {
				t.Fatalf("Validate error = %q, want containing %q", err.Error(), tt.wantErrText)
			}
		})
	}
}

func TestValidateRequiresAliyunSMSConfigWhenSMSEnabled(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Env: "prod",
		App: AppConfig{
			Name: "app_client",
			Host: "0.0.0.0",
			Port: "8802",
		},
		Database: DatabaseConfig{
			DBType:   "mysql",
			Host:     "127.0.0.1",
			Port:     "3306",
			Name:     "sleep",
			Username: "root",
		},
		Redis: RedisConfig{
			Addr: "127.0.0.1:6379",
			DBs:  []int{0, 1, 2},
		},
		Log: LogConfig{
			Level: "info",
			Path:  "./logs/app_client",
		},
		SMS: SMSConfig{
			Enabled: true,
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate expected sms config error, got nil")
	}
}

func TestValidateRequiresPayConfigWhenEnabled(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Env: "dev",
		App: AppConfig{
			Name: "app_client",
			Host: "localhost",
			Port: "8008",
		},
		Database: DatabaseConfig{
			DBType:   "mysql",
			Host:     "127.0.0.1",
			Port:     "3306",
			Name:     "sleep",
			Username: "root",
		},
		Redis: RedisConfig{
			Addr: "127.0.0.1:6379",
			DBs:  []int{0, 1, 2},
		},
		Log: LogConfig{
			Level: "info",
			Path:  "./logs/app_client",
		},
		Pay: PayConfig{
			Wechat: WechatPayConfig{Enabled: true},
			Alipay: AlipayConfig{Enabled: true},
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate expected pay config error, got nil")
	}
}

func TestValidateEnvWhitelist(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		env         Env
		wantErrText string
	}{
		{name: "dev passes", env: EnvDev},
		{name: "test passes", env: EnvTest},
		{name: "prod passes", env: EnvProd},
		{name: "empty rejected", env: "", wantErrText: "env is required"},
		{name: "production rejected", env: "production", wantErrText: "must be exactly one of dev|test|prod"},
		{name: "prd rejected", env: "prd", wantErrText: "must be exactly one of dev|test|prod"},
		{name: "capitalized rejected", env: "Prod", wantErrText: "must be exactly one of dev|test|prod"},
		{name: "whitespace-padded rejected", env: " prod ", wantErrText: "must be exactly one of dev|test|prod"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfigForTest()
			cfg.Env = tt.env

			err := cfg.Validate()
			if tt.wantErrText == "" {
				if err != nil {
					t.Fatalf("Validate returned error: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("Validate expected error containing %q, got nil", tt.wantErrText)
			}
			if !strings.Contains(err.Error(), tt.wantErrText) {
				t.Fatalf("Validate error = %q, want containing %q", err.Error(), tt.wantErrText)
			}
		})
	}
}

func TestValidateLaravelJWTConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		laravel     LaravelJWTConfig
		wantErrText string
	}{
		{
			name:    "disabled empty block passes",
			laravel: LaravelJWTConfig{},
		},
		{
			name:    "disabled ignores invalid algorithm",
			laravel: LaravelJWTConfig{Algorithm: "rs256"},
		},
		{
			name:    "enabled with secret and default algorithm passes",
			laravel: LaravelJWTConfig{Enabled: true, Secret: "laravel-secret"},
		},
		{
			name:    "enabled with uppercase hs512 passes",
			laravel: LaravelJWTConfig{Enabled: true, Secret: "laravel-secret", Algorithm: "HS512"},
		},
		{
			name:        "enabled without secret rejected",
			laravel:     LaravelJWTConfig{Enabled: true},
			wantErrText: "jwt.laravel.secret",
		},
		{
			name:        "enabled with blank secret rejected",
			laravel:     LaravelJWTConfig{Enabled: true, Secret: "   "},
			wantErrText: "jwt.laravel.secret",
		},
		{
			name:        "enabled with asymmetric algorithm rejected",
			laravel:     LaravelJWTConfig{Enabled: true, Secret: "laravel-secret", Algorithm: "rs256"},
			wantErrText: "jwt.laravel.algorithm",
		},
		{
			name:        "enabled with none algorithm rejected",
			laravel:     LaravelJWTConfig{Enabled: true, Secret: "laravel-secret", Algorithm: "none"},
			wantErrText: "jwt.laravel.algorithm",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfigForTest()
			cfg.JWT.Laravel = tt.laravel
			// 启用回退必须给出终点，否则会先被那条规则挡下，
			// 本用例想验证的字段就永远测不到。
			if tt.laravel.Enabled && cfg.JWT.LaravelAcceptUntil == "" {
				cfg.JWT.LaravelAcceptUntil = time.Now().Add(24 * time.Hour).Format(time.RFC3339)
			}

			err := cfg.Validate()
			if tt.wantErrText == "" {
				if err != nil {
					t.Fatalf("Validate returned error: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("Validate expected error containing %q, got nil", tt.wantErrText)
			}
			if !strings.Contains(err.Error(), tt.wantErrText) {
				t.Fatalf("Validate error = %q, want containing %q", err.Error(), tt.wantErrText)
			}
		})
	}
}

func validConfigForTest() *Config {
	return &Config{
		Env: EnvDev,
		App: AppConfig{
			Name: "app_client",
			Host: "localhost",
			Port: "8008",
		},
		Database: DatabaseConfig{
			DBType:   "mysql",
			Host:     "127.0.0.1",
			Port:     "3306",
			Name:     "sleep",
			Username: "root",
		},
		Redis: RedisConfig{
			Addr: "127.0.0.1:6379",
			DBs:  []int{0, 1, 2},
		},
		Log: LogConfig{
			Level: "info",
			Path:  "./logs/app_client",
		},
		JWT: JWTConfig{
			Active: JWTKeyConfig{
				KID:            "k2026a",
				PrivateKeyPath: "config/pem/k2026a.private.pem",
				PublicKeyPath:  "config/pem/k2026a.public.pem",
			},
			Issuer:    "gin-api",
			Audiences: []string{"app"},
		},
	}
}
