package bootstrap

import (
	"context"
	"errors"
	"testing"

	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
)

// stubRoleVerifier 是可控的角色源替身。
type stubRoleVerifier struct {
	has  bool
	err  error
	seen struct {
		userID int64
		role   string
	}
}

func (s *stubRoleVerifier) HasRole(_ context.Context, userID int64, role string) (bool, error) {
	s.seen.userID = userID
	s.seen.role = role
	return s.has, s.err
}

// TestRoleAuthorizerAllowsWhenAuthoritativeSourceHasIt 覆盖放行路径。
func TestRoleAuthorizerAllowsWhenAuthoritativeSourceHasIt(t *testing.T) {
	t.Parallel()

	stub := &stubRoleVerifier{has: true}
	a := newRoleAuthorizer(stub, adminRoleName)

	allowed, err := a.Authorize(context.Background(),
		sharedauth.Principal{UserID: 42}, sharedauth.AuthorizationRequest{})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !allowed {
		t.Fatal("权威源里有该角色却被拒绝")
	}
	if stub.seen.userID != 42 || stub.seen.role != adminRoleName {
		t.Fatalf("查询参数 = (%d, %q)，want (42, %q)", stub.seen.userID, stub.seen.role, adminRoleName)
	}
}

// TestRoleAuthorizerDeniesWhenSourceHasNoRecord 钉住"未接入角色数据时默认拒绝"。
//
// 这是"替换恒拒绝占位没有放宽默认行为"的证明：角色表为空或未建时，
// 行为与此前的 DenyAll 一致。默认允许在权限判定上是不可接受的失效方向。
func TestRoleAuthorizerDeniesWhenSourceHasNoRecord(t *testing.T) {
	t.Parallel()

	a := newRoleAuthorizer(&stubRoleVerifier{has: false}, adminRoleName)

	allowed, err := a.Authorize(context.Background(),
		sharedauth.Principal{UserID: 42}, sharedauth.AuthorizationRequest{})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if allowed {
		t.Fatal("权威源里没有该角色却放行了")
	}
}

// TestRoleAuthorizerDoesNotTrustTokenClaims 钉住"不采信令牌里的角色声明"。
//
// 令牌寿命可达数十天，其中的角色是签发那一刻的事实。仅凭它放行，
// 一个被撤销权限的账号在令牌过期前仍然畅通无阻，而系统里没有任何地方能看出这件事。
func TestRoleAuthorizerDoesNotTrustTokenClaims(t *testing.T) {
	t.Parallel()

	// 令牌声称自己是 admin，但权威源里没有记录。
	principal := sharedauth.Principal{UserID: 42, Roles: []string{adminRoleName, "superuser"}}
	a := newRoleAuthorizer(&stubRoleVerifier{has: false}, adminRoleName)

	allowed, err := a.Authorize(context.Background(), principal, sharedauth.AuthorizationRequest{})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if allowed {
		t.Fatal("仅凭令牌里的角色声明就放行了 —— 那等于把一次签发当成持续有效的高权限")
	}
}

// TestRoleAuthorizerSurfacesSourceFailure 钉住"角色源故障不得表现为拒绝"。
//
// **反证**：把 HasRole 的 error 吞掉改成 return false, nil，本用例必须失败——
// 那会让一次基础设施故障表现为全体管理员突然失去权限，
// 而客户端收到的是一个正常的 403，既不重试也不告警。
func TestRoleAuthorizerSurfacesSourceFailure(t *testing.T) {
	t.Parallel()

	sourceErr := errors.New("role source is down")
	a := newRoleAuthorizer(&stubRoleVerifier{err: sourceErr}, adminRoleName)

	allowed, err := a.Authorize(context.Background(),
		sharedauth.Principal{UserID: 42}, sharedauth.AuthorizationRequest{})
	if !errors.Is(err, sourceErr) {
		t.Fatalf("err = %v, want %v —— 故障必须上抛以映射 503，而不是折叠成 403", err, sourceErr)
	}
	if allowed {
		t.Fatal("查询失败却放行了")
	}
}

// TestRoleAuthorizerDeniesWhenVerifierMissing 钉住"装配缺失时拒绝"。
//
// 装配缺失绝不能表现为"允许"——那是一个安装漏洞会静默变成放行的形状。
func TestRoleAuthorizerDeniesWhenVerifierMissing(t *testing.T) {
	t.Parallel()

	a := newRoleAuthorizer(nil, adminRoleName)

	allowed, err := a.Authorize(context.Background(),
		sharedauth.Principal{UserID: 42}, sharedauth.AuthorizationRequest{})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if allowed {
		t.Fatal("未注入角色源却放行了")
	}
}
