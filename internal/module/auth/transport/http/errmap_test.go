package http

import (
	"context"
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

// 本文件走**完整的 handler → application 链路**，端口用替身注入。
//
// 只测绑定层是不够的：错误映射表是这一层的核心产出，而它只有在真的收到
// 领域错误时才会被执行。一条写错的映射（比如把"手机号已注册"映射成 500）
// 在绑定层测试里完全看不出来。

type stubCreds struct {
	cred      application.Credential
	found     bool
	createErr error
	createID  int64
}

func (s stubCreds) FindByPhone(context.Context, string) (application.Credential, bool, error) {
	return s.cred, s.found, nil
}

func (s stubCreds) Create(context.Context, string, string, string) (int64, error) {
	if s.createErr != nil {
		return 0, s.createErr
	}
	return s.createID, nil
}

// stubHasher 把 "ok" 当作唯一正确的口令。
type stubHasher struct{}

func (stubHasher) Hash(string) (string, error) { return "hashed", nil }

func (stubHasher) Compare(_, password string) error {
	if password == "ok" {
		return nil
	}
	return application.ErrInvalidCredentials
}

func (stubHasher) CompareWithDummy(string) error { return application.ErrInvalidCredentials }

type stubIssuer struct{}

func (stubIssuer) Issue(context.Context, int64, ...string) (string, error) { return "access", nil }

type stubRefresh struct {
	consumeErr error
}

func (stubRefresh) Issue(context.Context, int64) (string, error) { return "refresh", nil }

func (s stubRefresh) Consume(context.Context, string) (int64, string, error) {
	if s.consumeErr != nil {
		return 0, "", s.consumeErr
	}
	return 7, "rotated", nil
}

func (stubRefresh) RevokeFamilyOfToken(context.Context, string) error { return nil }

type stubRevocation struct{}

func (stubRevocation) Revoke(context.Context, string, time.Time) error { return nil }

// newTestHandler 装配一个端口全是替身的 handler。
func newTestHandler(t *testing.T, creds stubCreds, refresh stubRefresh) *Handler {
	t.Helper()

	svc, err := application.NewService(application.Options{
		Credentials:    creds,
		Hasher:         stubHasher{},
		AccessIssuer:   stubIssuer{},
		RefreshStore:   refresh,
		RevocationList: stubRevocation{},
		AccessTTL:      15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return NewHandler(svc)
}

// doRequest 执行一次 handler 调用并返回响应。
func doRequest(t *testing.T, h gin.HandlerFunc, body string, setup func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/auth/x", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if setup != nil {
		setup(c)
	}
	h(c)
	return rec
}

// TestLoginErrorMapping 钉住登录的错误映射。
//
// **凭据错误与账号不存在必须落在同一个响应上**：区分它们等于提供一个不需要
// 口令就能枚举已注册账号的接口。这条在 application 层已有覆盖，
// 这里确认映射表没有在最后一步把它们拆开。
func TestLoginErrorMapping(t *testing.T) {
	t.Parallel()

	activeUser := application.Credential{UserID: 7, PasswordHash: "hashed", Active: true}
	disabled := application.Credential{UserID: 7, PasswordHash: "hashed", Active: false}

	tests := []struct {
		name     string
		creds    stubCreds
		password string
		wantCode int
	}{
		{
			name: "success", creds: stubCreds{cred: activeUser, found: true},
			password: "ok", wantCode: http.StatusOK,
		},
		{
			name: "wrong password", creds: stubCreds{cred: activeUser, found: true},
			password: "nope", wantCode: http.StatusUnauthorized,
		},
		{
			name: "unknown account", creds: stubCreds{found: false},
			password: "ok", wantCode: http.StatusUnauthorized,
		},
		{
			name: "disabled account", creds: stubCreds{cred: disabled, found: true},
			password: "ok", wantCode: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTestHandler(t, tt.creds, stubRefresh{})
			rec := doRequest(t, h.Login,
				`{"phone":"13800000000","password":"`+tt.password+`"}`, nil)

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tt.wantCode, rec.Body.String())
			}
		})
	}
}

// TestUnknownAccountAndWrongPasswordShareOneResponse 钉住两种失败的响应逐字节相同。
//
// 状态码相同还不够：响应体里任何差异（不同的提示文案、不同的错误码）
// 同样是一条可用的枚举信道。
func TestUnknownAccountAndWrongPasswordShareOneResponse(t *testing.T) {
	t.Parallel()

	active := application.Credential{UserID: 7, PasswordHash: "hashed", Active: true}

	unknown := doRequest(t, newTestHandler(t, stubCreds{found: false}, stubRefresh{}).Login,
		`{"phone":"13900000000","password":"ok"}`, nil)
	wrong := doRequest(t, newTestHandler(t, stubCreds{cred: active, found: true}, stubRefresh{}).Login,
		`{"phone":"13800000000","password":"nope"}`, nil)

	if unknown.Code != wrong.Code || unknown.Body.String() != wrong.Body.String() {
		t.Fatalf("the two failures differ:\n  unknown account: %d %s\n  wrong password:  %d %s\n"+
			"an attacker could enumerate registered phones from the difference",
			unknown.Code, unknown.Body.String(), wrong.Code, wrong.Body.String())
	}
}

// TestRegisterErrorMapping 钉住重复注册是 409 而不是 500。
//
// 重复注册是正常的业务结果。映射成 500 会让它进错误告警，也会让客户端
// 以为是服务端故障而重试——而重试同样会失败。
func TestRegisterErrorMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		creds    stubCreds
		wantCode int
	}{
		{name: "success", creds: stubCreds{createID: 7}, wantCode: http.StatusOK},
		{
			name:     "phone taken",
			creds:    stubCreds{createErr: application.ErrPhoneTaken},
			wantCode: http.StatusConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTestHandler(t, tt.creds, stubRefresh{})
			rec := doRequest(t, h.Register,
				`{"phone":"13800000000","password":"long-enough","nickname":"n"}`, nil)

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tt.wantCode, rec.Body.String())
			}
		})
	}
}

// TestRefreshErrorMapping 钉住刷新失败一律指向重新登录。
//
// 重用与过期对客户端必须同义：区分它们会告诉攻击者他手里的令牌处于哪种状态。
func TestRefreshErrorMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		refresh  stubRefresh
		wantCode int
	}{
		{name: "success", refresh: stubRefresh{}, wantCode: http.StatusOK},
		{
			name:     "rejected",
			refresh:  stubRefresh{consumeErr: application.ErrRefreshRejected},
			wantCode: http.StatusUnauthorized,
		},
		{
			name:     "reused",
			refresh:  stubRefresh{consumeErr: application.ErrRefreshReused},
			wantCode: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTestHandler(t, stubCreds{}, tt.refresh)
			rec := doRequest(t, h.Refresh, `{"refresh_token":"fam.secret"}`, nil)

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tt.wantCode, rec.Body.String())
			}
		})
	}
}

// TestSuccessfulLoginReturnsAUsableTokenPair 钉住成功响应的内容。
func TestSuccessfulLoginReturnsAUsableTokenPair(t *testing.T) {
	t.Parallel()

	h := newTestHandler(t, stubCreds{
		cred:  application.Credential{UserID: 7, PasswordHash: "hashed", Active: true},
		found: true,
	}, stubRefresh{})
	rec := doRequest(t, h.Login, `{"phone":"13800000000","password":"ok"}`, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body: %s)", rec.Code, rec.Body.String())
	}

	var envelope struct {
		Data struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
			TokenType    string `json:"token_type"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v (body: %s)", err, rec.Body.String())
	}

	if envelope.Data.AccessToken == "" || envelope.Data.RefreshToken == "" {
		t.Fatalf("the response carries an incomplete token pair: %+v", envelope.Data)
	}
	if envelope.Data.TokenType != "Bearer" {
		t.Errorf("token_type = %q, want Bearer", envelope.Data.TokenType)
	}
	if envelope.Data.ExpiresIn != int64((15 * time.Minute).Seconds()) {
		t.Errorf("expires_in = %d, want %d", envelope.Data.ExpiresIn, int64((15 * time.Minute).Seconds()))
	}
}

// TestLogoutSucceedsWithATokenIdentity 覆盖登出的正常路径。
func TestLogoutSucceedsWithATokenIdentity(t *testing.T) {
	t.Parallel()

	h := newTestHandler(t, stubCreds{}, stubRefresh{})
	rec := doRequest(t, h.Logout, `{"refresh_token":"fam.secret"}`, func(c *gin.Context) {
		ginc.SetTokenIdentity(c, ginc.TokenIdentity{
			TokenID:   "jti-1",
			ExpiresAt: time.Now().Add(10 * time.Minute),
		})
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
}
