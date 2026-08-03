package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/router/routespec"
)

// runRequestBody 在一条只挂了本中间件的链上跑一个请求。
func runRequestBody(
	t *testing.T, policy routespec.RequestBodyPolicy, globalMax int64, req *http.Request,
) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(RequestBody(policy, globalMax))
	engine.Handle(req.Method, "/x", func(c *gin.Context) {
		// 真实 handler 会读 body；读到这里才会撞上 MaxBytesReader，
		// 这正是 chunked 请求唯一的暴露点。
		if _, err := io.ReadAll(c.Request.Body); err != nil {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		c.Status(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func jsonRequest(method, body string) *http.Request {
	req := httptest.NewRequest(method, "/x", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// TestRequestBodyEnforcesMode 钉住模式与 Content-Type 的匹配。
//
// 不在这里拒绝的话，一个只接受 JSON 的接口收到 multipart 时，错误要到
// handler 绑定失败才浮现，且此时 body 已经被完整读进内存。
func TestRequestBodyEnforcesMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		mode        routespec.BodyMode
		contentType string
		body        string
		wantStatus  int
	}{
		{
			name: "json accepts json", mode: routespec.BodyModeJSON,
			contentType: "application/json", body: `{"a":1}`, wantStatus: http.StatusOK,
		},
		{
			// 真实请求几乎总是带 charset 参数；直接相等比较会把它拒掉。
			name: "json accepts json with parameters", mode: routespec.BodyModeJSON,
			contentType: "application/json; charset=utf-8", body: `{"a":1}`, wantStatus: http.StatusOK,
		},
		{
			name: "json rejects form", mode: routespec.BodyModeJSON,
			contentType: "application/x-www-form-urlencoded", body: "a=1",
			wantStatus: http.StatusUnsupportedMediaType,
		},
		{
			name: "json rejects a missing content type", mode: routespec.BodyModeJSON,
			contentType: "", body: `{"a":1}`, wantStatus: http.StatusUnsupportedMediaType,
		},
		{
			name: "multipart accepts multipart", mode: routespec.BodyModeMultipart,
			contentType: "multipart/form-data; boundary=xyz", body: "--xyz--",
			wantStatus: http.StatusOK,
		},
		{
			name: "form accepts form", mode: routespec.BodyModeForm,
			contentType: "application/x-www-form-urlencoded", body: "a=1", wantStatus: http.StatusOK,
		},
		{
			name: "none rejects any body", mode: routespec.BodyModeNone,
			contentType: "application/json", body: `{"a":1}`, wantStatus: http.StatusBadRequest,
		},
		{
			name: "none accepts an empty body", mode: routespec.BodyModeNone,
			contentType: "", body: "", wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(tt.body))
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}

			rec := runRequestBody(t, routespec.RequestBodyPolicy{Mode: tt.mode}, 1<<20, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestRequestBodyEnforcesLimit 钉住声明长度与 chunked 两条路径都覆盖。
//
// 只做声明长度预检的话，一个 chunked 请求可以无声地灌进任意大小——
// 而攻击者恰恰可以自由选择用不用 chunked。
func TestRequestBodyEnforcesLimit(t *testing.T) {
	t.Parallel()

	policy := routespec.RequestBodyPolicy{Mode: routespec.BodyModeJSON, MaxBytes: 16}

	t.Run("declared length over the limit is rejected", func(t *testing.T) {
		t.Parallel()

		rec := runRequestBody(t, policy, 1<<20, jsonRequest(http.MethodPost, strings.Repeat("x", 64)))
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", rec.Code)
		}
	})

	t.Run("chunked over the limit is caught while reading", func(t *testing.T) {
		t.Parallel()

		req := jsonRequest(http.MethodPost, strings.Repeat("x", 64))
		// -1 表示 chunked：长度未知，声明长度预检这条路径完全不经过。
		req.ContentLength = -1
		rec := runRequestBody(t, policy, 1<<20, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", rec.Code)
		}
	})

	t.Run("within the limit passes", func(t *testing.T) {
		t.Parallel()

		rec := runRequestBody(t, policy, 1<<20, jsonRequest(http.MethodPost, `{"a":1}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
		}
	})
}

// TestRouteLimitCanOnlyTighten 钉住路由级上限只能收紧。
//
// 允许放宽的话，全局上限就形同虚设——而它正是那道不需要逐个路由审查
// 就能成立的兜底。
func TestRouteLimitCanOnlyTighten(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		routeMax  int64
		globalMax int64
		want      int64
	}{
		{name: "route limit tightens", routeMax: 100, globalMax: 1000, want: 100},
		{name: "route limit cannot loosen", routeMax: 5000, globalMax: 1000, want: 1000},
		{name: "zero inherits the global limit", routeMax: 0, globalMax: 1000, want: 1000},
		{
			name:     "a missing global limit falls back to the default",
			routeMax: 0, globalMax: 0, want: DefaultMaxBytesLimitConfig.MaxBytes,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := effectiveLimit(tt.routeMax, tt.globalMax); got != tt.want {
				t.Fatalf("effectiveLimit(%d, %d) = %d, want %d",
					tt.routeMax, tt.globalMax, got, tt.want)
			}
		})
	}
}

// TestBodylessMethodsSkipTheCheck 验证读方法不受影响。
//
// GET 的零值策略视为"不接受请求体"，而一个不带 body 的 GET 必须原样通过——
// 否则每一次普通读请求都会被 400。
func TestBodylessMethodsSkipTheCheck(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := runRequestBody(t, routespec.RequestBodyPolicy{Mode: routespec.BodyModeNone}, 1<<20, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
