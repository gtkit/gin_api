package resp

import (
	"net/http"
	"strings"
	"testing"
	"unicode"

	"github.com/gtkit/verify"
)

// initZhValidation 保证本文件的用例在翻译器就绪后运行——与生产时序一致：
// bootstrap 先 verify.New 再 EnableValidationTranslation，之后才有请求绑定。
// verify.New 幂等，可重复调用。
func initZhValidation(t *testing.T) {
	t.Helper()
	if err := verify.New(); err != nil {
		t.Fatalf("verify.New() = %v, want nil", err)
	}
	EnableValidationTranslation()
}

func containsHan(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return unicode.Is(unicode.Han, r) })
}

// TestBindStructErrTranslatesValidationErrors 钉住校验错误的对客消息契约：
// 中文、按对外 json 字段名呈现、多字段失败时取字段名排序后的第一条、
// 不泄漏 Go 结构体名与导出字段名（validation-error-messages spec）。
func TestBindStructErrTranslatesValidationErrors(t *testing.T) {
	initZhValidation(t)

	type internalNamedReq struct {
		PhoneNumber string `binding:"required" json:"phone"`
		AgeYears    int    `binding:"gte=18"   json:"age"`
	}
	verr := verify.Struct(internalNamedReq{AgeYears: 10})
	if verr == nil {
		t.Fatal("verify.Struct() = nil, want two field failures")
	}

	render := func() (int, string) {
		c, rec := newCtx()
		BindStructErr(c, verr)
		return rec.Code, decode(t, rec)["msg"].(string)
	}

	code, msg := render()
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", code)
	}
	if !containsHan(msg) {
		t.Errorf("msg = %q, want Chinese validation message", msg)
	}
	// 字段名排序后第一条是 age（age < phone），且必须用 json tag 名。
	if !strings.Contains(msg, "age") {
		t.Errorf("msg = %q, want the first sorted field %q", msg, "age")
	}
	for _, leaked := range []string{"Key:", "internalNamedReq", "PhoneNumber", "AgeYears"} {
		if strings.Contains(msg, leaked) {
			t.Errorf("msg = %q leaks internal naming %q", msg, leaked)
		}
	}

	// 同一输入重复渲染结果一致（确定性契约）。
	if _, again := render(); again != msg {
		t.Errorf("msg not deterministic: %q vs %q", msg, again)
	}
}

// TestBindErrTranslatesValidationErrors 覆盖 BindErr 同一收口。
func TestBindErrTranslatesValidationErrors(t *testing.T) {
	initZhValidation(t)

	type queryReq struct {
		Keyword string `binding:"required" json:"keyword"`
	}
	verr := verify.Struct(queryReq{})
	if verr == nil {
		t.Fatal("verify.Struct() = nil, want required failure")
	}

	c, rec := newCtx()
	BindErr(c, verr)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	msg := decode(t, rec)["msg"].(string)
	if !strings.Contains(msg, "keyword") || !containsHan(msg) {
		t.Errorf("msg = %q, want Chinese message naming field %q", msg, "keyword")
	}
}

// TestBindErrPayloadTooLargeSkipsTranslation 钉住超限优先级：
// 请求体超限必须仍走 413，不进翻译路径。
func TestBindErrPayloadTooLargeSkipsTranslation(t *testing.T) {
	initZhValidation(t)

	c, rec := newCtx()
	BindErr(c, &http.MaxBytesError{Limit: 1 << 20})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}
