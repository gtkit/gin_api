package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/goerr"
)

// clientEnvelope 是对客响应信封中与本组用例相关的两个字段。
type clientEnvelope struct {
	Code int32  `json:"code"`
	Msg  string `json:"msg"`
}

func decodeEnvelope(t *testing.T, body []byte) clientEnvelope {
	t.Helper()
	var env clientEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("解析响应信封失败: %v (body=%s)", err, body)
	}
	return env
}

// authFailureCases 构造三种**原因不同**的认证失败。
//
// 它们的共同点是都属于"令牌不被接受"，因此对客文案必须一致——
// 区分原因等于告诉调用方他手里的令牌处于哪种状态。
func authFailureCases(t *testing.T) []struct {
	name   string
	build  func() (gin.HandlerFunc, string)
	detail string
} {
	t.Helper()

	return []struct {
		name   string
		build  func() (gin.HandlerFunc, string)
		detail string
	}{
		{
			name: "令牌无法解析",
			build: func() (gin.HandlerFunc, string) {
				jwt := newTestJWT(t)
				return mustAuthenticate(t, fakeUserVerifier{valid: true}, jwt.Parser, false),
					"this-is-not-a-token"
			},
			detail: "token parse failed",
		},
		{
			name: "主体不可用",
			build: func() (gin.HandlerFunc, string) {
				jwt := newTestJWT(t)
				token, err := jwt.Issuer.Issue(context.Background(), 42, "user")
				if err != nil {
					t.Fatalf("Issue: %v", err)
				}
				// valid=false → 用户校验不通过
				return mustAuthenticate(t, fakeUserVerifier{valid: false}, jwt.Parser, false), token
			},
			detail: "principal is unavailable",
		},
	}
}

// TestAuthFailuresShareOneClientMessage 钉住"不同的认证失败返回相同对客文案"。
//
// **反证**：把任一处的技术描述改回 goerr.New 的 msgs 参数，本用例必须失败——
// 那正是泄漏令牌状态的写法。
func TestAuthFailuresShareOneClientMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var messages []string
	for _, tt := range authFailureCases(t) {
		t.Run(tt.name, func(t *testing.T) {
			mw, token := tt.build()

			engine := gin.New()
			engine.Use(mw)
			engine.GET("/protected", func(c *gin.Context) { c.Status(http.StatusNoContent) })

			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			req.Header.Set("App-Token", token)
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}

			env := decodeEnvelope(t, rec.Body.Bytes())
			messages = append(messages, env.Msg)

			// 对客文案不得包含技术原因。
			if strings.Contains(env.Msg, tt.detail) {
				t.Fatalf("对客文案 %q 含技术原因 %q —— 它告诉了调用方令牌处于哪种状态", env.Msg, tt.detail)
			}
			// 也不得是英文技术描述的其它形态。
			for _, leaked := range []string{"token", "principal", "revoked", "invalid", "unavailable"} {
				if strings.Contains(strings.ToLower(env.Msg), leaked) {
					t.Fatalf("对客文案 %q 含技术词 %q", env.Msg, leaked)
				}
			}
		})
	}

	for i := 1; i < len(messages); i++ {
		if messages[i] != messages[0] {
			t.Fatalf("不同的认证失败返回了不同文案: %q vs %q —— 差异本身就是信息",
				messages[0], messages[i])
		}
	}
}

// TestAuthFailureKeepsStatusAndCode 钉住"统一文案不改变错误码与状态码"。
//
// 文案统一是为了不泄漏原因，不是让客户端无法区分失败大类——
// 后者由 HTTP 状态码与业务 code 承担，它们是客户端分支依赖的契约。
func TestAuthFailureKeepsStatusAndCode(t *testing.T) {
	gin.SetMode(gin.TestMode)

	jwt := newTestJWT(t)
	engine := gin.New()
	engine.Use(mustAuthenticate(t, fakeUserVerifier{valid: true}, jwt.Parser, false))
	engine.GET("/protected", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("App-Token", "bad-token")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	env := decodeEnvelope(t, rec.Body.Bytes())
	if env.Code == 0 {
		t.Fatal("业务 code 为 0：认证失败必须带非零业务码，否则客户端无从区分失败大类")
	}
	if env.Msg == "" {
		t.Fatal("对客文案为空：统一文案不等于不给文案")
	}
}

// TestMissingTokenIsAlsoUnauthorized 保证上面的用例不是"任何请求都 401"。
//
// 只断言失败不足以证明判据有效——一条永远失败的认证也能通过那些断言。
func TestValidTokenStillPasses(t *testing.T) {
	gin.SetMode(gin.TestMode)

	jwt := newTestJWT(t)
	token, err := jwt.Issuer.Issue(context.Background(), 42, "user")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	engine := gin.New()
	engine.Use(mustAuthenticate(t, fakeUserVerifier{valid: true}, jwt.Parser, false))
	engine.GET("/protected", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("App-Token", token)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("有效令牌被拒: status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// TestTechnicalReasonReachesTheLogPath 钉住"细节被挪进日志，而不是被丢弃"。
//
// 只断言对客文案不含技术原因是不够的——那无法区分两种情况：
// 细节被正确地挪进了日志，还是细节被整个删掉了。后者没有安全收益，
// 只是把排查线索也一起扔了：出事时只能看到"未登录"，
// 无从判断是配置错了还是有人在试探。
//
// response.logError 记录的是 Item.Error()，对客用的是 Message()——
// 因此断言这两者的分工，等价于断言"日志有、响应没有"。
func TestTechnicalReasonReachesTheLogPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		cause  error
		detail string
	}{
		{name: "subject 非法", cause: errTokenSubjectInvalid, detail: "token subject is invalid"},
		{name: "主体不可用", cause: errPrincipalUnavailable, detail: "principal is unavailable"},
		{name: "令牌已撤销", cause: errTokenRevoked, detail: "token has been revoked"},
		{name: "无身份即授权", cause: errAuthorizeWithoutPrincipal, detail: "authorization requires an authenticated principal"},
		{name: "权限不足", cause: errPermissionDenied, detail: "permission denied"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			item := goerr.New(tt.cause, goerr.StatusAuth())

			// 进日志的那一个：必须含技术原因。
			if !strings.Contains(item.Error(), tt.detail) {
				t.Fatalf("日志文案 %q 不含技术原因 %q —— 细节被丢弃了，排查时无从判断失败缘由",
					item.Error(), tt.detail)
			}
			// 对客的那一个：必须不含。
			if strings.Contains(item.Message(), tt.detail) {
				t.Fatalf("对客文案 %q 含技术原因 %q", item.Message(), tt.detail)
			}
			if item.Message() == "" {
				t.Fatal("对客文案为空")
			}
		})
	}
}
