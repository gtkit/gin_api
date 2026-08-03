package bootstrap

import (
	"context"
	"errors"
	"testing"

	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
)

// stubVIPVerifier 按用例返回固定结果，用于钉住 adapter 的三条分支。
type stubVIPVerifier struct {
	ok  bool
	err error
}

func (s stubVIPVerifier) ValidateVIP(context.Context, int64) (bool, error) {
	return s.ok, s.err
}

// TestVIPAuthorizerErrorSemantics 钉住"依赖故障 ≠ 拒绝"这条语义。
//
// 关键分支是 err != nil：adapter 必须原样上抛，中间件才能把它映射成 503。
// 若 adapter 把 error 吞成 (false, nil)，一次数据库抖动会让全体会员收到 403——
// 客户端把它当作正常的业务拒绝，既不重试，也不会触发依赖故障告警。
func TestVIPAuthorizerErrorSemantics(t *testing.T) {
	t.Parallel()

	depErr := errors.New("user store unavailable")

	cases := []struct {
		name      string
		verifier  sharedauth.VIPVerifier
		wantAllow bool
		wantErr   error
	}{
		{
			name:      "active member is allowed",
			verifier:  stubVIPVerifier{ok: true},
			wantAllow: true,
		},
		{
			name:      "non member is denied without error",
			verifier:  stubVIPVerifier{ok: false},
			wantAllow: false,
		},
		{
			name:     "dependency failure surfaces as error, not denial",
			verifier: stubVIPVerifier{ok: false, err: depErr},
			wantErr:  depErr,
		},
		{
			name: "missing verifier denies instead of allowing",
			// 装配缺失绝不能表现为"允许"：nil 依赖走拒绝分支。
			verifier:  nil,
			wantAllow: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			allowed, err := newVIPAuthorizer(tc.verifier).Authorize(
				context.Background(),
				sharedauth.Principal{UserID: 42},
				sharedauth.AuthorizationRequest{Method: "GET", RoutePattern: "/api/v1/vip"},
			)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Authorize() error = %v, want %v", err, tc.wantErr)
				}
				if allowed {
					t.Fatal("Authorize() allowed=true alongside an error; must fail closed")
				}
				return
			}
			if err != nil {
				t.Fatalf("Authorize() error = %v, want nil", err)
			}
			if allowed != tc.wantAllow {
				t.Fatalf("Authorize() allowed = %v, want %v", allowed, tc.wantAllow)
			}
		})
	}
}
