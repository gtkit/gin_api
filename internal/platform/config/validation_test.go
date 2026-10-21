package config

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/appmeta"
	"github.com/gtkit/gin-api/internal/shared/env"
)

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
		env         env.Env
		appRoot     string
		logPath     string
		wantErrText string
	}{
		{
			name:    "prod relative path allowed without app root",
			env:     env.Prod,
			logPath: "./logs/app_client",
		},
		{
			name:    "dev absolute path allowed without app root",
			env:     env.Dev,
			logPath: filepath.Join(outsideRoot, "logs", "app_client"),
		},
		{
			name:    "prod absolute path under app root allowed",
			env:     env.Prod,
			appRoot: appRoot,
			logPath: filepath.Join(appRoot, "logs", "app_client"),
		},
		{
			name:        "prod absolute path without app root rejected",
			env:         env.Prod,
			logPath:     filepath.Join(appRoot, "logs", "app_client"),
			wantErrText: "APP_ROOT",
		},
		{
			name:        "prod absolute path with relative app root rejected",
			env:         env.Prod,
			appRoot:     "relative-root",
			logPath:     filepath.Join(appRoot, "logs", "app_client"),
			wantErrText: "APP_ROOT must be absolute",
		},
		{
			name:        "prod absolute path outside app root rejected",
			env:         env.Prod,
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
			Provider: "aliyun",
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate expected sms config error, got nil")
	}
	for _, want := range []string{"sms.access_id", "sms.access_secret", "sms.sign_name"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

// TestValidateOutboundChannelShapes 钉住出站通道的配置门与告警配对约束。
func TestValidateOutboundChannelShapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		email   EmailConfig
		sms     SMSConfig
		wantErr string
	}{
		{
			name:    "email 半配置被拒绝",
			email:   EmailConfig{From: "a@b.c"},
			wantErr: "email.host is empty",
		},
		{
			name:    "email from 非法地址被拒绝",
			email:   EmailConfig{Host: "smtp.example.com", From: "not-an-address"},
			wantErr: "email.from",
		},
		{
			name:    "sms 半配置被拒绝",
			sms:     SMSConfig{SignName: "sign"},
			wantErr: "sms.provider is empty",
		},
		{
			name: "sms 告警键必须成对",
			sms: SMSConfig{
				Provider: "aliyun", AccessID: "a", AccessSecret: "s", SignName: "n",
				AlertPhones: []string{"13800000000"},
			},
			wantErr: "must be set together",
		},
		{
			name: "sms 告警模板必须已定义",
			sms: SMSConfig{
				Provider: "aliyun", AccessID: "a", AccessSecret: "s", SignName: "n",
				AlertPhones: []string{"13800000000"}, AlertTemplate: "missing",
			},
			wantErr: "is not defined in sms.templates",
		},
		{
			name: "合法配置通过",
			email: EmailConfig{
				Host: "smtp.example.com", From: "noreply@example.com",
				AlertTo: []string{"ops@example.com"},
			},
			sms: SMSConfig{
				Provider: "aliyun", AccessID: "a", AccessSecret: "s", SignName: "n",
				Templates:   map[string]SMSTemplateConfig{"alert": {Code: "SMS_1"}},
				AlertPhones: []string{"13800000000"}, AlertTemplate: "alert",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := errors.Join(validateEmailConfig(tc.email), validateSMSConfig(tc.sms))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate returned %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %v does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateEnvWhitelist(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		env         env.Env
		wantErrText string
	}{
		{name: "dev passes", env: env.Dev},
		{name: "test passes", env: env.Test},
		{name: "prod passes", env: env.Prod},
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
			laravel: LaravelJWTConfig{Enabled: true, Secret: strings.Repeat("s", 32)},
		},
		{
			name:    "enabled with uppercase hs512 passes",
			laravel: LaravelJWTConfig{Enabled: true, Secret: strings.Repeat("s", 64), Algorithm: "HS512"},
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
			laravel:     LaravelJWTConfig{Enabled: true, Secret: strings.Repeat("s", 32), Algorithm: "rs256"},
			wantErrText: "jwt.laravel.algorithm",
		},
		{
			name:        "enabled with none algorithm rejected",
			laravel:     LaravelJWTConfig{Enabled: true, Secret: strings.Repeat("s", 32), Algorithm: "none"},
			wantErrText: "jwt.laravel.algorithm",
		},
		// 密钥长度下限按算法（laravel-legacy-token-verify spec）：
		// 短于哈希输出长度的密钥让暴力搜索密钥比伪造签名更便宜。
		{
			name:        "hs256 short secret rejected",
			laravel:     LaravelJWTConfig{Enabled: true, Secret: strings.Repeat("s", 31)},
			wantErrText: "requires at least 32 bytes",
		},
		{
			name:        "hs384 short secret rejected",
			laravel:     LaravelJWTConfig{Enabled: true, Secret: strings.Repeat("s", 47), Algorithm: "hs384"},
			wantErrText: "requires at least 48 bytes",
		},
		{
			name:        "hs512 short secret rejected",
			laravel:     LaravelJWTConfig{Enabled: true, Secret: strings.Repeat("s", 63), Algorithm: "hs512"},
			wantErrText: "requires at least 64 bytes",
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
		Env: env.Dev,
		// 认证模式没有默认值，合法配置必须显式选一个。fixture 取 external：
		// 它不要求 Redis，因此不会给无关的用例强加一个依赖。
		Auth: AuthConfig{Mode: AuthModeExternal},
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

// TestValidateNewsConfig 钉住告警通道的启动校验：配置错误不得延迟到首次告警。
func TestValidateNewsConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		news        NewsConfig
		wantErrText string
	}{
		{name: "空配置合法（通道关闭）", news: NewsConfig{}},
		{name: "留空平台走飞书默认", news: NewsConfig{FSURL: "https://open.feishu.cn/hook/x"}},
		{name: "dingtalk 合法", news: NewsConfig{Platform: "dingtalk"}},
		{name: "平台大小写不敏感", news: NewsConfig{Platform: "WeCom"}},
		{
			name:        "未知平台拒绝",
			news:        NewsConfig{Platform: "slack"},
			wantErrText: "news.platform",
		},
		{
			name:        "相对 URL 拒绝",
			news:        NewsConfig{FSURL: "open.feishu.cn/hook/x"},
			wantErrText: "news.fsurl",
		},
		{
			name:        "非 http(s) scheme 拒绝",
			news:        NewsConfig{FSURL: "ftp://example.com/hook"},
			wantErrText: "news.fsurl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateNewsConfig(tt.news)
			if tt.wantErrText == "" {
				if err != nil {
					t.Fatalf("validateNewsConfig() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateNewsConfig() = nil, want error containing %q", tt.wantErrText)
			}
			if !strings.Contains(err.Error(), tt.wantErrText) {
				t.Fatalf("error %q does not contain %q", err, tt.wantErrText)
			}
		})
	}
}

// TestNegativeConfigValuesAreRejected 钉住负值不静默回退（P2 回归）。
//
// 回退让"配了一个非法值"与"没配"变成同一种结果，而使用者以为自己配的
// 那个值正在生效——排查时没有任何一处会提示真相。
func TestNegativeConfigValuesAreRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "outbound client 负超时",
			mutate: func(c *Config) {
				c.Outbound.Clients = map[string]OutboundClientConfig{
					"billing": {BaseURL: "https://billing.example.test", Timeout: -time.Second},
				}
			},
			wantErr: "negative values are not allowed",
		},
		{
			name:    "outbound defaults 负响应体上限",
			mutate:  func(c *Config) { c.Outbound.Defaults.MaxResponseBytes = -1 },
			wantErr: "outbound.defaults",
		},
		{
			name: "base_url 非绝对 URL",
			mutate: func(c *Config) {
				c.Outbound.Clients = map[string]OutboundClientConfig{
					"billing": {BaseURL: "billing.example.test/api"},
				}
			},
			wantErr: "not an absolute http(s) URL",
		},
		{
			name: "tasks 负并发",
			mutate: func(c *Config) {
				c.Tasks = TasksConfig{Enabled: true, Concurrency: -1, RedisDB: 0}
			},
			wantErr: "negative concurrency",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfigForTest()
			tt.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}
