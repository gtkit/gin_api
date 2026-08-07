package auth

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// okAuthorizer 是一个恒放行的实现，仅用于验证注册表本身的行为。
func okAuthorizer() Authorizer {
	return AuthorizerFunc(func(context.Context, Principal, AuthorizationRequest) (bool, error) {
		return true, nil
	})
}

// TestRegisterRejectsUnsafeEntries 钉住登记期的全部拒绝条件。
//
// 这是安全边界：注册表决定"这条路由的授权由谁判定"。任何一条拒绝规则失效，
// 后果都不是报错，而是**某条路由在运行期用了错误的授权实现**——而那种错误
// 在日志里看起来一切正常。
func TestRegisterRejectsUnsafeEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		id      string
		auth    Authorizer
		wantErr string
	}{
		{name: "a valid id is accepted", id: "entitlement.vip", auth: okAuthorizer()},
		{name: "a dotted and dashed id is accepted", id: "role.admin-v2", auth: okAuthorizer()},
		{
			// nil 实现若被登记，Lookup 会成功而调用会 panic——
			// 一个"存在但不可用"的授权实现比不存在更危险。
			name: "a nil authorizer is rejected", id: "role.admin", auth: nil,
			wantErr: "must not be nil",
		},
		{
			name: "an empty id is rejected", id: "", auth: okAuthorizer(),
			wantErr: "must match",
		},
		{
			// ID 会进日志与 metrics label，必须是低基数、不含用户输入的字面量。
			name: "an id with uppercase is rejected", id: "Role.Admin", auth: okAuthorizer(),
			wantErr: "must match",
		},
		{
			name: "an id with spaces is rejected", id: "role admin", auth: okAuthorizer(),
			wantErr: "must match",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry := NewAuthorizerRegistry()

			err := registry.Register(tt.id, tt.auth)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Register: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Register(%q) succeeded, want an error containing %q", tt.id, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// TestRegisterRejectsDuplicates 钉住重复 ID 不被静默覆盖。
//
// 覆盖会让"最后一个注册的赢"，而注册顺序取决于装配代码的书写顺序——
// 于是一次无关的代码移动就能悄悄换掉某条路由的授权实现。
func TestRegisterRejectsDuplicates(t *testing.T) {
	t.Parallel()

	registry := NewAuthorizerRegistry()
	if err := registry.Register("role.admin", DenyAll("placeholder")); err != nil {
		t.Fatalf("first Register: %v", err)
	}

	err := registry.Register("role.admin", okAuthorizer())
	if err == nil {
		t.Fatal("a duplicate id was accepted; the later registration would silently win")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("error = %q, want it to say the id is already registered", err)
	}

	// 第一个登记必须原样保留——被覆盖的话这里会放行。
	authorizer, ok := registry.Lookup("role.admin")
	if !ok {
		t.Fatal("the first registration is gone")
	}
	allowed, authErr := authorizer.Authorize(context.Background(), Principal{}, AuthorizationRequest{})
	if allowed || authErr != nil {
		t.Fatal("the deny-all placeholder was replaced by the rejected duplicate")
	}
}

// TestIDsAreSorted 钉住 ID 顺序稳定。
//
// 错误信息里的顺序若随 map 遍历抖动，同一个装配错误每次报出的文本都不同，
// 无法用于告警去重，也让"和上次一样吗"这个问题无法回答。
func TestIDsAreSorted(t *testing.T) {
	t.Parallel()

	registry := NewAuthorizerRegistry()
	for _, id := range []string{"z.last", "a.first", "m.middle"} {
		if err := registry.Register(id, okAuthorizer()); err != nil {
			t.Fatalf("Register(%q): %v", id, err)
		}
	}

	want := []string{"a.first", "m.middle", "z.last"}
	for range 10 {
		if got := registry.IDs(); !slices.Equal(got, want) {
			t.Fatalf("IDs() = %v, want %v", got, want)
		}
	}
}

// TestNilRegistryIsSafeToRead 验证零值注册表的读取不 panic。
//
// 装配失败时调用方可能持有 nil 注册表；此时读取必须返回"未找到"，
// 而不是 panic——panic 会绕过已建立资源的清理。
func TestNilRegistryIsSafeToRead(t *testing.T) {
	t.Parallel()

	var registry *AuthorizerRegistry

	if _, ok := registry.Lookup("anything"); ok {
		t.Fatal("a nil registry must not resolve anything")
	}
	if ids := registry.IDs(); ids != nil {
		t.Fatalf("IDs() = %v, want nil", ids)
	}
}

// TestDenyAllAlwaysDenies 钉住占位实现的行为。
//
// `role.admin` 在没有权威角色源之前用它兜底。它若在任何情况下放行，
// 一个本该拒绝的请求就会通过——而占位实现恰恰是最容易被忽略的地方。
//
// 它必须返回 (false, nil) 而不是 (false, err)：按 Authorizer 契约，
// 非 nil error 表示**依赖故障**并映射 503。占位拒绝是一个确定的策略结论，
// 不是故障——报成 503 会让客户端不断重试一个永远不会成功的请求。
func TestDenyAllAlwaysDenies(t *testing.T) {
	t.Parallel()

	const reason = "no authoritative role source yet"
	authorizer := DenyAll(reason)

	// Reason 必须被保留：它出现在启动日志与拒绝说明里，
	// 使"为什么这个接口一直 403"能被立刻定位，而不是变成一桩悬案。
	if authorizer.Reason != reason {
		t.Fatalf("Reason = %q, want %q", authorizer.Reason, reason)
	}

	cases := []struct {
		name      string
		principal Principal
		request   AuthorizationRequest
	}{
		{name: "an anonymous principal"},
		{name: "an authenticated principal", principal: Principal{UserID: 1}},
		{
			name:      "a principal with route context",
			principal: Principal{UserID: 42, Roles: []string{"admin"}},
			request: AuthorizationRequest{
				Method: "GET", RoutePattern: "/api/v1/admin/:id",
				PathParams: map[string]string{"id": "1"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			allowed, err := authorizer.Authorize(context.Background(), tc.principal, tc.request)
			if allowed {
				t.Fatal("the deny-all placeholder allowed a request")
			}
			if err != nil {
				t.Fatalf("a placeholder denial must be (false, nil), not a dependency failure: %v", err)
			}
		})
	}
}

// TestAuthorizerFuncPropagatesErrors 验证函数适配器不吞错误。
//
// 授权依赖故障必须能传到调用方并映射成 503——被吞掉的话它会变成 403，
// 而 403 告诉客户端"你没权限"，那是一个错误且不可重试的结论。
func TestAuthorizerFuncPropagatesErrors(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("entitlement service unavailable")
	authorizer := AuthorizerFunc(
		func(context.Context, Principal, AuthorizationRequest) (bool, error) {
			return false, sentinel
		})

	allowed, err := authorizer.Authorize(context.Background(), Principal{}, AuthorizationRequest{})
	if allowed {
		t.Fatal("a failing authorizer must not allow the request")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want the underlying failure to survive", err)
	}
}
