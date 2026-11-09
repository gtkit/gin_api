package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/middleware"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
	"github.com/gtkit/verify"
)

// 本文件是 internal/pkg/response/validation.go 那组校验响应收口
// （BindAllErr / FieldErr / ValidateMapErr / ValidateMapAllErr）的 **handler 级
// 端到端示范**：在真实 gin router 上下文里挂 throwaway 路由跑通它们，证明
// 「绑定 → 校验收口 → JSON 信封」整条链成立，而不只是响应层的单元测试。
//
// 刻意**不经过 example 模块的 Register、不挂生产路由**——只示范 handler 写法，
// 不扩 API 表面。派生项目要真正启用，把对应收口函数用进自己的 handler 即可
// （用法对照 docs/api-conventions.md 的「校验错误的对客消息」方法表）。

// signupDemo 是仅用于本测试的多字段 DTO：三个字段都会失败，用来证明
// BindAllErr 一次回全部字段错误（msg 聚合 + data.fields 结构化明细）。
type signupDemo struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=8"`
	Age      int    `json:"age" binding:"gte=18"`
}

// initValidation 保证翻译器就绪，与生产启动时序一致：verify.New 先于任何绑定，
// 随后 EnableValidationTranslation 置位。verify.New 幂等，可重复调用。
func initValidation(t *testing.T) {
	t.Helper()
	if err := verify.New(); err != nil {
		t.Fatalf("verify.New() = %v, want nil", err)
	}
	resp.EnableValidationTranslation()
}

// serveDemo 在带 ErrorHandler 的测试 engine 上挂一条 throwaway POST /demo 路由，
// 用 body 触发 handler，返回状态码与解出的响应信封。
func serveDemo(t *testing.T, handler gin.HandlerFunc, body string) (int, map[string]any) {
	t.Helper()
	engine := gin.New()
	engine.Group("/demo", middleware.ErrorHandler()).POST("", handler)

	request := httptest.NewRequest(http.MethodPost, "/demo", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	var envelope map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return recorder.Code, envelope
}

// asString 断言 v 是字符串并返回它：对响应信封的类型断言统一走它，既满足
// errcheck（不用空标识符丢弃 ok），又在类型不符时给出清晰失败点。
func asString(t *testing.T, v any) string {
	t.Helper()
	s, ok := v.(string)
	if !ok {
		t.Fatalf("value is not a string: %#v", v)
	}
	return s
}

func containsHan(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return unicode.Is(unicode.Han, r) })
}

// TestHandlerBindAllErrReturnsEveryField 示范表单类接口用 BindAllErr：
// 多字段同时失败时，422 的 msg 聚合全部字段、data.fields 给逐字段结构化明细
// （只含 field/message，不含 value/tag），按字段名排序。
func TestHandlerBindAllErrReturnsEveryField(t *testing.T) {
	initValidation(t)

	handler := func(c *gin.Context) {
		var req signupDemo
		if err := c.ShouldBindJSON(&req); err != nil {
			resp.BindAllErr(c, err)
			return
		}
		resp.Ok(c, nil)
	}
	status, envelope := serveDemo(t, handler, `{"email":"bad","password":"short","age":10}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", status)
	}

	msg := asString(t, envelope["msg"])
	if !containsHan(msg) {
		t.Errorf("msg = %q, want Chinese", msg)
	}
	for _, field := range []string{"age", "email", "password"} {
		if !strings.Contains(msg, field) {
			t.Errorf("msg = %q, want it to name field %q", msg, field)
		}
	}

	data, ok := envelope["data"].(map[string]any)
	if !ok {
		t.Fatalf("data = %#v, want object carrying fields", envelope["data"])
	}
	fields, ok := data["fields"].([]any)
	if !ok {
		t.Fatalf("data.fields = %#v, want array", data["fields"])
	}
	if len(fields) != 3 {
		t.Fatalf("len(data.fields) = %d, want 3", len(fields))
	}
	first, ok := fields[0].(map[string]any)
	if !ok {
		t.Fatalf("fields[0] = %#v, want object", fields[0])
	}
	// 排序后 age 在前（age < email < password）。
	if first["field"] != "age" {
		t.Errorf("fields[0].field = %v, want the first sorted field %q", first["field"], "age")
	}
	// 结构化明细不得回显敏感原值与内部规则名。
	if _, has := first["value"]; has {
		t.Errorf("fields[0] leaks value: %#v", first)
	}
	if _, has := first["tag"]; has {
		t.Errorf("fields[0] leaks tag: %#v", first)
	}
}

// TestHandlerFieldErrRendersSingleField 示范 application 层单值校验
// （verify.Field）经 FieldErr 收口：422 的 msg 由调用方给出的字段名打头。
func TestHandlerFieldErrRendersSingleField(t *testing.T) {
	initValidation(t)

	handler := func(c *gin.Context) {
		var body struct {
			Keyword string `json:"keyword"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			resp.BindStructErr(c, err)
			return
		}
		// 单值校验没有结构体上下文可提取 json tag，字段名由调用方给出。
		if err := verify.Field(body.Keyword, "required"); err != nil {
			resp.FieldErr(c, "keyword", err)
			return
		}
		resp.Ok(c, nil)
	}
	status, envelope := serveDemo(t, handler, `{"keyword":""}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", status)
	}
	msg := asString(t, envelope["msg"])
	if !strings.Contains(msg, "keyword") || !containsHan(msg) {
		t.Errorf("msg = %q, want Chinese naming field %q", msg, "keyword")
	}
}

// TestHandlerValidateMapErrRendersFirstField 示范动态 payload 经 verify.Map
// 校验后由 ValidateMapErr 收口：422 的 msg 为排序后第一条字段错误。
func TestHandlerValidateMapErrRendersFirstField(t *testing.T) {
	initValidation(t)

	handler := func(c *gin.Context) {
		var payload map[string]any
		if err := c.ShouldBindJSON(&payload); err != nil {
			resp.BindStructErr(c, err)
			return
		}
		errs := verify.Map(payload, map[string]any{"email": "required,email"})
		if len(errs) > 0 {
			resp.ValidateMapErr(c, errs)
			return
		}
		resp.Ok(c, nil)
	}
	status, envelope := serveDemo(t, handler, `{"email":"bad"}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", status)
	}
	msg := asString(t, envelope["msg"])
	if !strings.Contains(msg, "email") || !containsHan(msg) {
		t.Errorf("msg = %q, want Chinese naming field %q", msg, "email")
	}
}

// TestHandlerValidateMapAllErrAggregates 示范 ValidateMapAllErr：多字段 map
// 校验失败时 msg 聚合全部字段（对比 ValidateMapErr 只回第一条）。
func TestHandlerValidateMapAllErrAggregates(t *testing.T) {
	initValidation(t)

	handler := func(c *gin.Context) {
		var payload map[string]any
		if err := c.ShouldBindJSON(&payload); err != nil {
			resp.BindStructErr(c, err)
			return
		}
		errs := verify.Map(payload, map[string]any{
			"name":  "required,min=8",
			"email": "required,email",
		})
		if len(errs) > 0 {
			resp.ValidateMapAllErr(c, errs)
			return
		}
		resp.Ok(c, nil)
	}
	status, envelope := serveDemo(t, handler, `{"name":"abc","email":"bad"}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", status)
	}
	msg := asString(t, envelope["msg"])
	if !strings.Contains(msg, "name") || !strings.Contains(msg, "email") {
		t.Errorf("msg = %q, want both fields aggregated", msg)
	}
}
