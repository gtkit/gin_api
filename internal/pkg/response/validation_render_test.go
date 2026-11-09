package resp

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gtkit/goerr"
	"github.com/gtkit/verify"
)

// twoFieldReq 有两个必失败字段：age（gte=18）与 phone（required）。
// 排序后 age 在前（age < phone），用于钉住确定性顺序。
type twoFieldReq struct {
	Phone string `binding:"required" json:"phone"`
	Age   int    `binding:"gte=18"   json:"age"`
}

// TestBindAllErrReturnsEveryFieldError 钉住 BindAllErr 的对客契约：
// 422、msg 聚合全部字段、data.fields 给出逐字段结构化明细（json 字段名 +
// 中文消息）、按字段名排序、不泄漏 Go 结构体名与导出字段名。
func TestBindAllErrReturnsEveryFieldError(t *testing.T) {
	initZhValidation(t)

	verr := verify.Struct(twoFieldReq{Age: 10})
	if verr == nil {
		t.Fatal("verify.Struct() = nil, want two field failures")
	}

	c, rec := newCtx()
	BindAllErr(c, verr)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}

	body := decode(t, rec)
	msg := mustString(t, body["msg"])
	if !strings.Contains(msg, "age") || !strings.Contains(msg, "phone") {
		t.Errorf("msg = %q, want both fields aggregated", msg)
	}
	if !containsHan(msg) {
		t.Errorf("msg = %q, want Chinese", msg)
	}

	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("data = %#v, want object carrying fields", body["data"])
	}
	rawFields, ok := data["fields"].([]any)
	if !ok {
		t.Fatalf("data.fields = %#v, want array", data["fields"])
	}
	if len(rawFields) != 2 {
		t.Fatalf("len(data.fields) = %d, want 2", len(rawFields))
	}
	first, ok := rawFields[0].(map[string]any)
	if !ok {
		t.Fatalf("fields[0] = %#v, want object", rawFields[0])
	}
	if first["field"] != "age" {
		t.Errorf("fields[0].field = %v, want the first sorted field %q", first["field"], "age")
	}
	if m := mustString(t, first["message"]); !containsHan(m) {
		t.Errorf("fields[0].message = %q, want Chinese", m)
	}
	// 结构化明细里不得出现 Value / Tag（敏感原值与内部规则名）。
	for _, leaked := range []string{"value", "tag", "Key:", "twoFieldReq", "Phone", "Age"} {
		if strings.Contains(rec.Body.String(), leaked) {
			t.Errorf("body leaks %q: %s", leaked, rec.Body.String())
		}
	}
}

// TestBindAllErrPayloadTooLargeSkipsTranslation 钉住 413 优先级高于翻译路径。
func TestBindAllErrPayloadTooLargeSkipsTranslation(t *testing.T) {
	initZhValidation(t)

	c, rec := newCtx()
	BindAllErr(c, &http.MaxBytesError{Limit: 1 << 20})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

// TestBindAllErrBeforeTranslatorReadyUsesSafeMessage 钉住未就绪兜底：
// 固定文案 + 保留原始 cause 供诊断，绝不触发 verify 翻译。
func TestBindAllErrBeforeTranslatorReadyUsesSafeMessage(t *testing.T) {
	initZhValidation(t)
	previous := validationTranslationReady.Swap(false)
	t.Cleanup(func() { validationTranslationReady.Store(previous) })

	verr := verify.Struct(twoFieldReq{Age: 10})
	if verr == nil {
		t.Fatal("missing validation error")
	}
	c, rec := newCtx()
	BindAllErr(c, verr)
	body := decode(t, rec)
	if rec.Code != http.StatusUnprocessableEntity || body["msg"] != "请求参数格式错误" {
		t.Fatalf("status=%d body=%v", rec.Code, body)
	}
	if body["code"] != float64(goerr.StatusValidateParams().Code()) {
		t.Fatalf("business code changed: %v", body)
	}
}

// TestFieldErrRendersSingleField 覆盖单值校验的对客收口。
func TestFieldErrRendersSingleField(t *testing.T) {
	initZhValidation(t)

	verr := verify.Field("", "required")
	if verr == nil {
		t.Fatal("verify.Field() = nil, want required failure")
	}
	c, rec := newCtx()
	FieldErr(c, "keyword", verr)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	msg := mustString(t, decode(t, rec)["msg"])
	if !strings.Contains(msg, "keyword") || !containsHan(msg) {
		t.Errorf("msg = %q, want Chinese naming field %q", msg, "keyword")
	}
}

// TestFieldErrPassesThroughNonValidationError 钉住透传约定：非校验错误
// 不被强行套 422，交统一错误处理（此处退化为 500）。
func TestFieldErrPassesThroughNonValidationError(t *testing.T) {
	initZhValidation(t)

	c, rec := newCtx()
	FieldErr(c, "x", errors.New("db connection lost"))
	if rec.Code == http.StatusUnprocessableEntity {
		t.Fatalf("non-validation error forced to 422: %v", decode(t, rec))
	}
}

// TestValidateMapErrRendersFirstField 覆盖 map 校验的第一条收口。
func TestValidateMapErrRendersFirstField(t *testing.T) {
	initZhValidation(t)

	errs := verify.Map(
		map[string]any{"email": "not-an-email"},
		map[string]any{"email": "required,email"},
	)
	if len(errs) == 0 {
		t.Fatal("verify.Map() = empty, want email failure")
	}
	c, rec := newCtx()
	ValidateMapErr(c, errs)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	msg := mustString(t, decode(t, rec)["msg"])
	if !strings.Contains(msg, "email") || !containsHan(msg) {
		t.Errorf("msg = %q, want Chinese naming field %q", msg, "email")
	}
}

// TestValidateMapAllErrAggregates 覆盖 map 校验的聚合收口：多字段全回。
func TestValidateMapAllErrAggregates(t *testing.T) {
	initZhValidation(t)

	errs := verify.Map(
		map[string]any{"name": "abc", "email": "not-an-email"},
		map[string]any{"name": "required,min=8", "email": "required,email"},
	)
	if len(errs) == 0 {
		t.Fatal("verify.Map() = empty, want failures")
	}
	c, rec := newCtx()
	ValidateMapAllErr(c, errs)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	msg := mustString(t, decode(t, rec)["msg"])
	if !strings.Contains(msg, "name") || !strings.Contains(msg, "email") {
		t.Errorf("msg = %q, want both fields aggregated", msg)
	}
}

// TestValidateMapEmptyWritesNothing 钉住空结果不写响应（无错误语义）。
func TestValidateMapEmptyWritesNothing(t *testing.T) {
	initZhValidation(t)

	c, rec := newCtx()
	ValidateMapErr(c, nil)
	ValidateMapAllErr(c, map[string]any{})
	if rec.Body.Len() != 0 {
		t.Fatalf("empty map errors wrote a body: %s", rec.Body.String())
	}
}

// mustString 断言 v 是字符串并返回它。测试里对响应信封的类型断言统一走它，
// 既满足 errcheck（不用空标识符丢弃 ok），又在类型不符时给出清晰失败点。
func mustString(t *testing.T, v any) string {
	t.Helper()
	s, ok := v.(string)
	if !ok {
		t.Fatalf("value is not a string: %#v", v)
	}
	return s
}
