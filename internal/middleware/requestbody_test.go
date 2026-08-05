package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// newBodyEngine 装配一条挂了指定 body 中间件的路由。
func newBodyEngine(t *testing.T, mw gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(ErrorHandler())
	engine.POST("/probe", mw, func(c *gin.Context) { c.Status(http.StatusOK) })
	engine.GET("/probe", mw, func(c *gin.Context) { c.Status(http.StatusOK) })
	return engine
}

// do 发一次带 body 的请求。
func do(engine *gin.Engine, method, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/probe", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

// TestJSONBodyEnforcesContentType 钉住类型不符回 415。
//
// 不校验类型的话，一个发 form 编码的客户端会得到"字段缺失"的 422——
// 而真实原因是编码方式不对，那个错误信息会把人引向错误的方向。
func TestJSONBodyEnforcesContentType(t *testing.T) {
	t.Parallel()

	engine := newBodyEngine(t, JSONBody(0))

	tests := []struct {
		name        string
		contentType string
		wantStatus  int
	}{
		{name: "json accepted", contentType: "application/json", wantStatus: http.StatusOK},
		{name: "json with charset accepted", contentType: "application/json; charset=utf-8", wantStatus: http.StatusOK},
		{name: "form rejected", contentType: "application/x-www-form-urlencoded", wantStatus: http.StatusUnsupportedMediaType},
		{name: "missing type rejected", contentType: "", wantStatus: http.StatusUnsupportedMediaType},
		{name: "text rejected", contentType: "text/plain", wantStatus: http.StatusUnsupportedMediaType},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := do(engine, http.MethodPost, tt.contentType, `{"a":1}`)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestFormAndMultipartAcceptTheirOwnTypes 覆盖另外两种 body 中间件。
func TestFormAndMultipartAcceptTheirOwnTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		mw          gin.HandlerFunc
		contentType string
		wantStatus  int
	}{
		{name: "form accepts form", mw: FormBody(0),
			contentType: "application/x-www-form-urlencoded", wantStatus: http.StatusOK},
		{name: "form rejects json", mw: FormBody(0),
			contentType: "application/json", wantStatus: http.StatusUnsupportedMediaType},
		{name: "multipart accepts multipart", mw: MultipartBody(0),
			contentType: "multipart/form-data; boundary=x", wantStatus: http.StatusOK},
		{name: "multipart rejects json", mw: MultipartBody(0),
			contentType: "application/json", wantStatus: http.StatusUnsupportedMediaType},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := do(newBodyEngine(t, tt.mw), http.MethodPost, tt.contentType, "a=1")
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

// TestNoBodyRejectsAPayload 钉住"不接受请求体"是显式拒绝。
//
// 静默忽略的话，客户端以为自己传了参数，而服务端从未读过它——
// 那种不一致只有在业务结果不对时才会被发现。
func TestNoBodyRejectsAPayload(t *testing.T) {
	t.Parallel()

	engine := newBodyEngine(t, NoBody())

	if rec := do(engine, http.MethodPost, "application/json", `{"a":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("a request with a body returned %d, want 400", rec.Code)
	}
	if rec := do(engine, http.MethodPost, "", ""); rec.Code != http.StatusOK {
		t.Fatalf("a request without a body returned %d, want 200", rec.Code)
	}
}

// TestBodyLimitReturns413 钉住超限回 413。
func TestBodyLimitReturns413(t *testing.T) {
	t.Parallel()

	const limit = 16
	engine := newBodyEngine(t, JSONBody(limit))

	if rec := do(engine, http.MethodPost, "application/json", `{"a":1}`); rec.Code != http.StatusOK {
		t.Fatalf("a small body returned %d, want 200", rec.Code)
	}

	big := `{"a":"` + strings.Repeat("x", 64) + `"}`
	rec := do(engine, http.MethodPost, "application/json", big)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("an oversized body returned %d, want 413", rec.Code)
	}
	// 超限必须关连接：继续读一个已经判定过大的 body 只是白白占着带宽。
	if got := rec.Header().Get("Connection"); got != "close" {
		t.Errorf("Connection = %q, want close", got)
	}
}

// TestEmptyBodyPassesTypeCheck 钉住无 body 时不做类型校验。
//
// Content-Length 为 0 的写请求交给 handler 的绑定去报"字段缺失"，
// 那个错误比 415 更贴近真实问题。
func TestEmptyBodyPassesTypeCheck(t *testing.T) {
	t.Parallel()

	engine := newBodyEngine(t, JSONBody(0))
	if rec := do(engine, http.MethodPost, "", ""); rec.Code != http.StatusOK {
		t.Fatalf("an empty body returned %d, want 200: the binding should report the "+
			"missing field, which is closer to the real problem than a 415", rec.Code)
	}
}
