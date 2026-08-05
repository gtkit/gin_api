package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/module/auth/application"
	"github.com/gtkit/gin-api/internal/pkg/ginc"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}

// TestLogoutWithoutTokenIdentityFails 钉住"撤销不了就不能报成功"。
//
// 拿不到令牌标识时若回 200，用户会以为自己已经登出，而那个令牌仍然可用
// 直到自然过期——一个只在真正需要时才暴露的失效。
//
// 这条不经过 application：它测的正是 handler 在依赖缺位时的行为，
// 因此 service 为 nil——若实现改成先调 service，这个测试会以 panic 失败，
// 那也是正确的信号。
func TestLogoutWithoutTokenIdentityFails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		identity *ginc.TokenIdentity
	}{
		{name: "no identity at all", identity: nil},
		{
			name:     "identity without a token id",
			identity: &ginc.TokenIdentity{ExpiresAt: time.Now().Add(time.Hour)},
		},
		{
			// 没有过期时刻就无法给撤销记录一个正确的存活时间：
			// 短了令牌提前复活，长了撤销表白白占空间。
			name:     "identity without an expiry",
			identity: &ginc.TokenIdentity{TokenID: "jti-1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewHandler(nil)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/auth/logout",
				strings.NewReader(`{}`))
			c.Request.Header.Set("Content-Type", "application/json")
			if tt.identity != nil {
				ginc.SetTokenIdentity(c, *tt.identity)
			}

			handler.Logout(c)

			if rec.Code == http.StatusOK {
				t.Fatalf("logout returned 200 while it could not revoke anything: "+
					"the user would believe they are logged out (body: %s)", rec.Body.String())
			}
		})
	}
}

// TestBindingRejectsBadPayloads 钉住入口校验。
//
// 这些请求根本到不了 application：绑定失败就返回，因此 service 为 nil
// 是安全的——若哪天实现改成先调 service 再校验，这个测试会 panic，
// 而那正是需要被发现的改动。
func TestBindingRejectsBadPayloads(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		route   func(*Handler) gin.HandlerFunc
		payload string
	}{
		{
			name:    "register without a phone",
			route:   func(h *Handler) gin.HandlerFunc { return h.Register },
			payload: `{"password":"long-enough-password"}`,
		},
		{
			// 注册对口令有下限：这是入口处的最低要求。
			name:    "register with a short password",
			route:   func(h *Handler) gin.HandlerFunc { return h.Register },
			payload: `{"phone":"13800000000","password":"short"}`,
		},
		{
			name:    "login without a password",
			route:   func(h *Handler) gin.HandlerFunc { return h.Login },
			payload: `{"phone":"13800000000"}`,
		},
		{
			name:    "refresh without a token",
			route:   func(h *Handler) gin.HandlerFunc { return h.Refresh },
			payload: `{}`,
		},
		{
			name:    "malformed json",
			route:   func(h *Handler) gin.HandlerFunc { return h.Login },
			payload: `{"phone":`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewHandler(nil)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/auth/x", strings.NewReader(tt.payload))
			c.Request.Header.Set("Content-Type", "application/json")

			tt.route(handler)(c)

			if rec.Code == http.StatusOK {
				t.Fatalf("an invalid payload was accepted (body: %s)", rec.Body.String())
			}
		})
	}
}

// TestLoginAcceptsShortLegacyPasswords 钉住登录不套用注册的长度下限。
//
// 存量用户的口令可能是在旧规则下设置的。给登录加 min=8 会把他们挡在门外，
// 而那与凭据是否正确无关——它们只会看到"参数错误"，无从理解。
//
// 断言的是"没有被绑定层拦下"：service 为 nil，因此走到业务层会 panic，
// 测试用 recover 把它翻译成"通过了校验"这一结论。
func TestLoginAcceptsShortLegacyPasswords(t *testing.T) {
	t.Parallel()

	handler := NewHandler(nil)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/auth/login",
		strings.NewReader(`{"phone":"13800000000","password":"old"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	passedBinding := false
	func() {
		defer func() {
			// 走到 service 就说明绑定放行了——这正是本测试要确认的。
			if recover() != nil {
				passedBinding = true
			}
		}()
		handler.Login(c)
	}()

	if !passedBinding && rec.Code != http.StatusOK {
		t.Fatalf("a short legacy password was rejected at the binding layer (code=%d, body=%s): "+
			"existing users would be locked out with a validation error they cannot act on",
			rec.Code, rec.Body.String())
	}
}

// pairFixture 是一份用于形状断言的令牌对。
func pairFixture() application.TokenPair {
	return application.TokenPair{
		AccessToken:  "access",
		RefreshToken: "refresh",
		ExpiresIn:    900,
	}
}

// TestTokenResponseShape 钉住对外契约的形状。
//
// 客户端按这些字段名解析。ExpiresIn 给秒数而不是绝对时刻：客户端与服务端
// 的时钟未必一致，而"还能用多久"不受时钟偏差影响。
func TestTokenResponseShape(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(tokenResponse(pairFixture()))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, field := range []string{"access_token", "refresh_token", "expires_in", "token_type"} {
		if _, ok := decoded[field]; !ok {
			t.Errorf("the token response has no %q field", field)
		}
	}
	if decoded["token_type"] != "Bearer" {
		t.Errorf("token_type = %v, want Bearer", decoded["token_type"])
	}
	if decoded["expires_in"] != float64(900) {
		t.Errorf("expires_in = %v, want 900 (seconds, not an absolute instant)", decoded["expires_in"])
	}
}
