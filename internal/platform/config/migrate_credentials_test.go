package config

import (
	"strings"
	"testing"
)

func TestMigrationCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		db          DatabaseConfig
		wantEnabled bool
		wantUser    string
		wantPass    string
	}{
		{
			name:        "not configured",
			db:          DatabaseConfig{Username: "app", Password: "pw"},
			wantEnabled: false,
		},
		{
			name:        "username and password",
			db:          DatabaseConfig{Username: "app", MigrateUsername: "ddl", MigratePassword: "ddlpw"},
			wantEnabled: true, wantUser: "ddl", wantPass: "ddlpw",
		},
		{
			// 密码为空仍算启用：Socket 认证与受控本地环境是真实场景，
			// 所以密码不能当启用开关。
			name:        "username without password stays enabled",
			db:          DatabaseConfig{Username: "app", MigrateUsername: "socket_user"},
			wantEnabled: true, wantUser: "socket_user", wantPass: "",
		},
		{
			name:        "whitespace-only username is not configured",
			db:          DatabaseConfig{Username: "app", MigrateUsername: "   "},
			wantEnabled: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := tt.db.MigrationCredentials()
			if ok != tt.wantEnabled {
				t.Fatalf("MigrationCredentials() enabled = %v, want %v", ok, tt.wantEnabled)
			}
			if !tt.wantEnabled {
				return
			}
			if got.Username != tt.wantUser || got.Password != tt.wantPass {
				t.Fatalf("MigrationCredentials() = %q/%q, want %q/%q",
					got.Username, got.Password, tt.wantUser, tt.wantPass)
			}
		})
	}
}

func TestRuntimeCredentialsIgnoreMigrationFields(t *testing.T) {
	t.Parallel()

	db := DatabaseConfig{
		Username: "app", Password: "pw",
		MigrateUsername: "ddl", MigratePassword: "ddlpw",
	}
	got := db.RuntimeCredentials()
	if got.Username != "app" || got.Password != "pw" {
		t.Fatalf("RuntimeCredentials() = %q/%q, want app/pw", got.Username, got.Password)
	}
}

// TestValidateDatabaseMigrationCredentialCombination 覆盖非法组合：
// 只配 migrate_password 而 migrate_username 为空必须 fail-fast，
// 不能静默忽略密码后回退运行凭据。
func TestValidateDatabaseMigrationCredentialCombination(t *testing.T) {
	t.Parallel()

	base := DatabaseConfig{
		DBType: "mysql", Host: "127.0.0.1", Port: "3306", Name: "db", Username: "app",
	}

	tests := []struct {
		name    string
		mutate  func(*DatabaseConfig)
		wantErr bool
	}{
		{name: "neither set", mutate: func(*DatabaseConfig) {}, wantErr: false},
		{name: "both set", mutate: func(d *DatabaseConfig) { d.MigrateUsername = "ddl"; d.MigratePassword = "pw" }, wantErr: false},
		{name: "username only", mutate: func(d *DatabaseConfig) { d.MigrateUsername = "ddl" }, wantErr: false},
		{name: "password only", mutate: func(d *DatabaseConfig) { d.MigratePassword = "pw" }, wantErr: true},
		{name: "blank username with password", mutate: func(d *DatabaseConfig) { d.MigrateUsername = "  "; d.MigratePassword = "pw" }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db := base
			tt.mutate(&db)
			err := validateDatabaseConfig(db)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateDatabaseConfig() err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(err.Error(), "migrate_username") {
				t.Fatalf("error must name the offending key, got %v", err)
			}
		})
	}
}
