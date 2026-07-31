package bootstrap

import (
	"testing"

	"github.com/gtkit/gin-api/internal/platform/config"
)

// TestResolveInfraRequiresDao 覆盖 spec 场景「基础设施未就绪时中止装配」：
// Runtime 的 legacy DAO 为 nil 时，resolveInfra 必须返回错误且不继续后续阶段。
func TestResolveInfraRequiresDao(t *testing.T) {
	t.Parallel()

	rt := &Runtime{config: &config.Config{}}
	if _, err := resolveInfra(rt); err == nil {
		t.Fatal("resolveInfra expected error when legacy dao is nil, got nil")
	}
}

// TestBuildMiddlewares 覆盖 spec 场景「token parser 在中间件阶段装配」：
// legacy 关闭时正常装配 auth；legacy 开启但密钥为空时错误在本阶段被透传。
func TestBuildMiddlewares(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		legacy  config.LegacyLaravelJWTConfig
		wantErr bool
	}{
		{
			name:   "legacy disabled wires auth middleware",
			legacy: config.LegacyLaravelJWTConfig{},
		},
		{
			name:    "legacy enabled with empty secret propagates error",
			legacy:  config.LegacyLaravelJWTConfig{Enabled: true},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{}
			cfg.JWT.LegacyLaravel = tt.legacy
			rt := &Runtime{config: cfg}

			mw, err := buildMiddlewares(services{}, rt)
			if tt.wantErr {
				if err == nil {
					t.Fatal("buildMiddlewares expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("buildMiddlewares returned error: %v", err)
			}
			if mw.auth == nil {
				t.Fatal("buildMiddlewares returned nil auth middleware")
			}
		})
	}
}
