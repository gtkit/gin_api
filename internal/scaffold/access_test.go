package scaffold

import (
	"strings"
	"testing"
)

// TestResolveAccessChoiceIsStrictlyExclusive 锁住 --access 与 --public 的关系。
//
// 两个都不给必须报错，而不是落一个默认值：访问策略是安全决策，
// 允许默认值就等于允许"没想过"这个状态存在，而最可能的默认（公开）
// 恰好是最危险的那个。
func TestResolveAccessChoiceIsStrictlyExclusive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		access  string
		public  bool
		wantErr string
	}{
		{name: "neither given", wantErr: "access policy is required"},
		{name: "both given", access: AccessOptionAuthenticated, public: true, wantErr: "conflicts"},
		{name: "unknown value", access: "superuser", wantErr: "unknown --access"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ResolveAccessChoice(tt.access, tt.public, "demo")
			if err == nil {
				t.Fatalf("expected an error for access=%q public=%v", tt.access, tt.public)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q should mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestResolveAccessChoiceMapsEveryMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		access   string
		public   bool
		wantExpr string
	}{
		{
			name: "public", public: true,
			wantExpr: "routespec.AccessPolicy{Mode: routespec.AccessPublic}",
		},
		{
			name: "authenticated", access: AccessOptionAuthenticated,
			wantExpr: "routespec.AccessPolicy{Mode: routespec.AccessAuthenticated}",
		},
		{
			name: "optional", access: AccessOptionOptional,
			wantExpr: "routespec.AccessPolicy{Mode: routespec.AccessOptionalAuthenticated}",
		},
		{
			name: "admin", access: AccessOptionAdmin,
			wantExpr: `routespec.AccessPolicy{Mode: routespec.AccessAuthorized, AuthorizerID: "role.admin"}`,
		},
		{
			// custom 的 ID 必须由模块名确定性派生：它同时出现在 RouteSpec、
			// registry 注册点与 metrics label 上，随机 ID 会让三者对不上。
			name: "custom", access: AccessOptionCustom,
			wantExpr: `routespec.AccessPolicy{Mode: routespec.AccessAuthorized, AuthorizerID: "module.demo.custom"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveAccessChoice(tt.access, tt.public, "demo")
			if err != nil {
				t.Fatalf("ResolveAccessChoice: %v", err)
			}
			if got.Expr != tt.wantExpr {
				t.Fatalf("Expr = %q, want %q", got.Expr, tt.wantExpr)
			}
			if got.Note == "" {
				t.Fatal("every choice must carry a note describing its consequences")
			}
		})
	}
}

// TestAdminNoteWarnsAboutPlaceholder 守住一条容易被忽略的提示：
// role.admin 当前是恒拒绝的占位实现。不说明的话，使用者会以为它已经能用，
// 然后在遇到持续 403 时无从下手——更糟的是可能"顺手"改成凭 JWT role claim 放行。
func TestAdminNoteWarnsAboutPlaceholder(t *testing.T) {
	t.Parallel()

	got, err := ResolveAccessChoice(AccessOptionAdmin, false, "demo")
	if err != nil {
		t.Fatalf("ResolveAccessChoice: %v", err)
	}
	for _, want := range []string{"权威角色源", "403", "claim"} {
		if !strings.Contains(got.Note, want) {
			t.Fatalf("admin note must mention %q, got %q", want, got.Note)
		}
	}
}

func TestCustomAuthorizerIDIsDeterministic(t *testing.T) {
	t.Parallel()

	if got := customAuthorizerID("order"); got != "module.order.custom" {
		t.Fatalf("customAuthorizerID = %q, want module.order.custom", got)
	}
	if customAuthorizerID("a") == customAuthorizerID("b") {
		t.Fatal("different modules must get different authorizer ids")
	}
}

// TestGeneratedModuleCarriesChosenAccessPolicy 端到端验证选择确实落进了生成代码。
func TestGeneratedModuleCarriesChosenAccessPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		access string
		public bool
		want   string
	}{
		{name: "public", public: true, want: "routespec.AccessPublic}"},
		{name: "authenticated", access: AccessOptionAuthenticated, want: "routespec.AccessAuthenticated}"},
		{name: "admin", access: AccessOptionAdmin, want: `AuthorizerID: "role.admin"`},
		{name: "custom", access: AccessOptionCustom, want: `AuthorizerID: "module.admin.custom"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := createProjectFixture(t)
			generator, err := NewModuleGenerator(root)
			if err != nil {
				t.Fatalf("NewModuleGenerator: %v", err)
			}
			if _, err := generator.GenerateWithOptions("admin", GenerateOptions{
				Bootstrap: true, Access: tt.access, Public: tt.public,
			}); err != nil {
				t.Fatalf("GenerateWithOptions: %v", err)
			}

			assertFileContains(t,
				root+"/internal/module/admin/transport/http/router.go", tt.want)
		})
	}
}
