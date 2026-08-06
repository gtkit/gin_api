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
			wantExpr: "",
		},
		{
			name: "authenticated", access: AccessOptionAuthenticated,
			wantExpr: "g.Use(deps.Auth)",
		},
		{
			name: "optional", access: AccessOptionOptional,
			wantExpr: "g.Use(deps.OptionalAuth)",
		},
		{
			name: "admin", access: AccessOptionAdmin,
			wantExpr: `g.Use(deps.Auth, deps.Authorize("role.admin"))`,
		},
		{
			// custom 的 ID 必须由模块名确定性派生：它同时出现在 RouteSpec、
			// registry 注册点与 metrics label 上，随机 ID 会让三者对不上。
			name: "custom", access: AccessOptionCustom,
			wantExpr: `g.Use(deps.Auth, deps.Authorize("module.demo.custom"))`,
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
		{name: "public", public: true, want: "g.GET(\"\", handler.List)"},
		{name: "authenticated", access: AccessOptionAuthenticated, want: "g.Use(deps.Auth)"},
		{name: "admin", access: AccessOptionAdmin, want: `deps.Authorize("role.admin")`},
		{name: "custom", access: AccessOptionCustom, want: `deps.Authorize("module.admin.custom")`},
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

// TestGeneratedWiringFillsEveryAccessDependency 钉住注入的 Deps 不含空值字段。
//
// 缺陷：注入行此前只填 Service/Auth/RateLimit，而路由模板的 Deps 还声明了
// OptionalAuth 与 Authorize。于是 `--access=admin/custom` 生成的模块启动即
// panic（nil 函数调用），`--access=optional` 每请求 500（nil handler 挂进链）——
// 四种访问策略里只有 authenticated 与 public 真正可用。
//
// 两者都能通过编译，因此编译型门禁抓不到；断言注入行本身才抓得到。
func TestGeneratedWiringFillsEveryAccessDependency(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		access string
		public bool
	}{
		{name: "public", public: true},
		{name: "authenticated", access: AccessOptionAuthenticated},
		{name: "optional", access: AccessOptionOptional},
		{name: "admin", access: AccessOptionAdmin},
		{name: "custom", access: AccessOptionCustom},
	} {
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

			providers := root + "/internal/bootstrap/providers.go"
			// 模板 Deps 声明的每个依赖都必须被填上，与所选策略无关：
			// 留空能编译，却在启动或首个请求上以空值调用崩溃。
			for _, field := range []string{
				"Service: svc.admin",
				"Auth: mw.auth",
				"OptionalAuth: mw.optionalAuth",
				"Authorize: mw.authorizeByID",
				"RateLimit: rateLimitStager",
			} {
				assertFileContains(t, providers, field)
			}
		})
	}
}

// TestAccessChoiceRateLimitScopeMatchesIdentity 钉住限流策略集合随访问策略变化。
//
// 没有身份的路由不能引用身份作用域的策略：那类策略取不到主体，中间件把它判为
// 装配错误并拒绝，于是该路由的每个请求都是 429。模板此前硬编码 default.user，
// 因此公开/可选认证模块一旦配上该策略就全量瘫痪。
func TestAccessChoiceRateLimitScopeMatchesIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		access    string
		public    bool
		wantsUser bool
	}{
		{name: "public 只按来源", public: true, wantsUser: false},
		{name: "optional 只按来源", access: AccessOptionOptional, wantsUser: false},
		{name: "authenticated 含身份", access: AccessOptionAuthenticated, wantsUser: true},
		{name: "admin 含身份", access: AccessOptionAdmin, wantsUser: true},
		{name: "custom 含身份", access: AccessOptionCustom, wantsUser: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ResolveAccessChoice(tt.access, tt.public, "admin")
			if err != nil {
				t.Fatalf("ResolveAccessChoice: %v", err)
			}
			hasUser := strings.Contains(got.RateLimitPolicies, "default.user")
			if hasUser != tt.wantsUser {
				t.Fatalf("RateLimitPolicies = %q, 含 default.user = %v, want %v",
					got.RateLimitPolicies, hasUser, tt.wantsUser)
			}
			if !strings.Contains(got.RateLimitPolicies, "default.ip") {
				t.Errorf("RateLimitPolicies = %q, 必须始终包含来源作用域策略",
					got.RateLimitPolicies)
			}
		})
	}
}
