package ginc

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
)

func newCtx() *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/x", nil)
	return c
}

// TestUserIDRoundTrip 覆盖用户 ID 的写入与读取。
func TestUserIDRoundTrip(t *testing.T) {
	t.Parallel()

	c := newCtx()

	// 未认证时必须是 0，而不是某个可能被当成合法用户的值。
	if got := UserIDFromContext(c); got != 0 {
		t.Fatalf("UserIDFromContext on a fresh context = %d, want 0", got)
	}

	SetUserID(c, 42)
	if got := UserIDFromContext(c); got != 42 {
		t.Fatalf("UserIDFromContext = %d, want 42", got)
	}
}

// TestPrincipalRoundTrip 覆盖已认证主体的写入与读取。
//
// **ok=false 绝不能被当成"允许"**：它表示本次请求尚未通过认证。
func TestPrincipalRoundTrip(t *testing.T) {
	t.Parallel()

	c := newCtx()

	if _, ok := PrincipalFromContext(c); ok {
		t.Fatal("a fresh context reported an authenticated principal")
	}

	want := sharedauth.Principal{UserID: 7, Roles: []string{"admin"}}
	SetPrincipal(c, want)

	got, ok := PrincipalFromContext(c)
	if !ok {
		t.Fatal("PrincipalFromContext did not find the principal that was just set")
	}
	if got.UserID != want.UserID || len(got.Roles) != 1 || got.Roles[0] != "admin" {
		t.Fatalf("principal = %+v, want %+v", got, want)
	}
}

// TestPrincipalFromNilContext 钉住 nil 上下文不 panic。
//
// 返回"未认证"而不是崩：授权中间件在异常路径上可能拿到 nil，
// 而那时正确的行为是拒绝，不是让进程挂掉。
func TestPrincipalFromNilContext(t *testing.T) {
	t.Parallel()

	if _, ok := PrincipalFromContext(nil); ok {
		t.Fatal("a nil context reported an authenticated principal")
	}
	if _, ok := TokenIdentityFromContext(nil); ok {
		t.Fatal("a nil context reported a token identity")
	}
}

// TestTokenIdentityRoundTrip 覆盖令牌标识的写入与读取。
//
// 它与 Principal 分开：Principal 回答"你是谁"，供授权判定消费；
// 这里回答"你这次用的是哪个令牌、它什么时候过期"，只有登出与撤销需要。
func TestTokenIdentityRoundTrip(t *testing.T) {
	t.Parallel()

	c := newCtx()

	if _, ok := TokenIdentityFromContext(c); ok {
		t.Fatal("a fresh context reported a token identity")
	}

	expiry := time.Unix(1754000000, 0)
	SetTokenIdentity(c, TokenIdentity{TokenID: "jti-1", ExpiresAt: expiry})

	got, ok := TokenIdentityFromContext(c)
	if !ok {
		t.Fatal("TokenIdentityFromContext did not find the identity that was just set")
	}
	if got.TokenID != "jti-1" || !got.ExpiresAt.Equal(expiry) {
		t.Fatalf("token identity = %+v, want jti-1 @ %s", got, expiry)
	}
}

// TestPrincipalAndTokenIdentityAreIndependent 钉住两者互不影响。
//
// 混用会让授权实现看到令牌 ID——而授权不该关心凭据的身份，
// 那是两个会各自演进的关注点。
func TestPrincipalAndTokenIdentityAreIndependent(t *testing.T) {
	t.Parallel()

	c := newCtx()
	SetPrincipal(c, sharedauth.Principal{UserID: 1})

	if _, ok := TokenIdentityFromContext(c); ok {
		t.Fatal("setting a principal also produced a token identity")
	}

	c2 := newCtx()
	SetTokenIdentity(c2, TokenIdentity{TokenID: "jti"})
	if _, ok := PrincipalFromContext(c2); ok {
		t.Fatal("setting a token identity also produced a principal")
	}
}
