package bootstrap

import (
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/platform/config"
)

// TestResolveDBCredentials 覆盖凭据选择的三种情形。
//
// 关键契约：server（UseMigrationCredentials=false）**永远**拿运行凭据，
// 即使配置里有迁移账号；否则运行进程会意外获得 DDL 权限。
func TestResolveDBCredentials(t *testing.T) {
	t.Parallel()

	withBoth := config.DatabaseConfig{
		Username: "app_rw", Password: "app_pw",
		MigrateUsername: "ddl_user", MigratePassword: "ddl_pw",
	}
	runtimeOnly := config.DatabaseConfig{Username: "app_rw", Password: "app_pw"}
	migrateNoPassword := config.DatabaseConfig{
		Username: "app_rw", Password: "app_pw",
		MigrateUsername: "socket_user",
	}

	tests := []struct {
		name     string
		db       config.DatabaseConfig
		opts     Options
		wantUser string
		wantPass string
	}{
		{
			name: "server ignores migration credentials",
			db:   withBoth, opts: Options{},
			wantUser: "app_rw", wantPass: "app_pw",
		},
		{
			name: "migrate uses migration credentials",
			db:   withBoth, opts: Options{UseMigrationCredentials: true},
			wantUser: "ddl_user", wantPass: "ddl_pw",
		},
		{
			name: "migrate falls back when not configured",
			db:   runtimeOnly, opts: Options{UseMigrationCredentials: true},
			wantUser: "app_rw", wantPass: "app_pw",
		},
		{
			// 密码允许为空：Socket 认证与受控本地环境是真实场景，
			// 因此密码不能当启用开关。
			name: "migrate credentials with empty password stay enabled",
			db:   migrateNoPassword, opts: Options{UseMigrationCredentials: true},
			wantUser: "socket_user", wantPass: "",
		},
		{
			name: "server with empty-password migration credentials still uses runtime",
			db:   migrateNoPassword, opts: Options{},
			wantUser: "app_rw", wantPass: "app_pw",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := resolveDBCredentials(&config.Config{Database: tt.db}, tt.opts)
			if got.Username != tt.wantUser || got.Password != tt.wantPass {
				t.Fatalf("resolveDBCredentials() = %q/%q, want %q/%q",
					got.Username, got.Password, tt.wantUser, tt.wantPass)
			}
		})
	}
}

// TestMigrationFallbackWarningHasNoCredentials 守住脱敏契约：
// 回退告警绝不能带用户名或密码。
func TestMigrationFallbackWarningHasNoCredentials(t *testing.T) {
	t.Parallel()

	const (
		user = "very_secret_user_name"
		pass = "very_secret_password"
	)
	cfg := &config.Config{Database: config.DatabaseConfig{Username: user, Password: pass}}

	// 告警经 logger 输出，这里直接断言告警文本常量不含凭据字段值。
	// 文本是固定字符串（无格式化占位符），因此不可能拼进凭据——用例锁住这一点，
	// 防止后人"顺手"把用户名加进去帮助排障。
	got := resolveDBCredentials(cfg, Options{UseMigrationCredentials: true})
	if got.Username != user {
		t.Fatalf("fallback should use runtime credentials, got %q", got.Username)
	}
	for _, secret := range []string{user, pass} {
		if strings.Contains(migrationFallbackWarning, secret) {
			t.Fatalf("fallback warning must not contain credentials: %q", migrationFallbackWarning)
		}
	}
	if strings.ContainsAny(migrationFallbackWarning, "%") {
		t.Fatalf("fallback warning must be a fixed string without format verbs: %q", migrationFallbackWarning)
	}
}
