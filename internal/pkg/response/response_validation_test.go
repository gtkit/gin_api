package resp

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/gtkit/goerr"
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
		msg, ok := decode(t, rec)["msg"].(string)
		if !ok {
			t.Fatal("response msg is not a string")
		}
		return rec.Code, msg
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
	msg, ok := decode(t, rec)["msg"].(string)
	if !ok {
		t.Fatal("response msg is not a string")
	}
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

func TestBindingFallbackKeepsCauseAndSafeMessage(t *testing.T) {
	for _, input := range []string{`{"count":`, `{"count":"private-type"}`, `{"private_field":1}`, ``} {
		var target struct {
			Count int `json:"count"`
		}
		decoder := json.NewDecoder(strings.NewReader(input))
		decoder.DisallowUnknownFields()
		cause := decoder.Decode(&target)
		if cause == nil {
			t.Fatalf("input %q did not produce a binding error", input)
		}
		assertSafeBindingError(t, cause)
	}
	assertSafeBindingError(t, errors.New("private-type /srv/config user=private-user token=private-token"))
	assertSafeBindingError(t, nil)
}

func TestBindingBeforeTranslatorReadyUsesSafeMessage(t *testing.T) {
	initZhValidation(t)
	previous := validationTranslationReady.Swap(false)
	t.Cleanup(func() { validationTranslationReady.Store(previous) })
	cause := verify.Struct(struct {
		InternalName string `binding:"required" json:"name"`
	}{})
	if cause == nil {
		t.Fatal("missing validation error")
	}
	assertSafeBindingError(t, cause)
}

func assertSafeBindingError(t *testing.T, cause error) {
	t.Helper()
	for _, bind := range []struct {
		name string
		call func(*gin.Context, error)
	}{{"BindErr", BindErr}, {"BindStructErr", BindStructErr}} {
		t.Run(bind.name, func(t *testing.T) {
			c, rec := newCtx()
			bind.call(c, cause)
			body := decode(t, rec)
			if rec.Code != http.StatusUnprocessableEntity || body["msg"] != "请求参数格式错误" {
				t.Fatalf("status=%d body=%v", rec.Code, body)
			}
			if body["code"] != float64(goerr.StatusValidateParams().Code()) {
				t.Fatalf("business code changed: %v", body)
			}
			if cause != nil {
				logged, exists := c.Get(loggedErrorKey)
				root, ok := logged.(error)
				causeKept := errors.Is(root, cause)
				// ValidationErrors is a slice, so errors.Is cannot compare it.
				// Match the original FieldError objects after unwrapping instead.
				if expected, validation := errors.AsType[validator.ValidationErrors](cause); validation {
					actual, found := errors.AsType[validator.ValidationErrors](root)
					causeKept = found && slices.Equal(actual, expected)
				}
				if !exists || !ok || !causeKept {
					t.Fatalf("diagnostic cause lost: %v", logged)
				}
			}
		})
	}
}
