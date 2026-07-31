package bootstrap

import (
	"testing"

	"github.com/gtkit/gin-api/internal/platform/config"
)

func TestNewMailClientFromConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     *config.Config
		wantNil bool
		wantErr bool
	}{
		{name: "nil 配置视为未启用", cfg: nil, wantNil: true},
		{name: "host 为空视为未启用", cfg: &config.Config{}, wantNil: true},
		{
			name: "host 空白串视为未启用",
			cfg: func() *config.Config {
				c := &config.Config{}
				c.Email.Host = "   "
				return c
			}(),
			wantNil: true,
		},
		{
			name: "合法配置构造客户端",
			cfg: func() *config.Config {
				c := &config.Config{}
				c.Email.Host = "smtp.example.com"
				c.Email.Port = 465
				c.Email.User = "noreply@example.com"
				c.Email.Pass = "secret"
				c.Email.Alias = "Example App"
				return c
			}(),
		},
		{
			name: "from 非法地址 fail-fast",
			cfg: func() *config.Config {
				c := &config.Config{}
				c.Email.Host = "smtp.example.com"
				c.Email.User = "not-an-address"
				return c
			}(),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, err := newMailClientFromConfig(tt.cfg)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("newMailClientFromConfig() expected error, got client %v", client)
				}
				return
			}
			if err != nil {
				t.Fatalf("newMailClientFromConfig() unexpected error = %v", err)
			}
			if tt.wantNil && client != nil {
				t.Fatalf("newMailClientFromConfig() = %v, want nil (mail disabled)", client)
			}
			if !tt.wantNil && client == nil {
				t.Fatal("newMailClientFromConfig() = nil, want client")
			}
		})
	}
}
