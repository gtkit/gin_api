package resp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/goerr"

	applog "github.com/gtkit/gin-api/internal/pkg/log"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}

// newCtx 返回一个可写响应的测试上下文。
func newCtx() (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/x", nil)
	return c, rec
}

// decode 解出响应信封。
func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

// TestNilDataBecomesAnEmptyArray 钉住 data 永不为 null。
//
// 客户端按 `data` 的类型写解析代码。同一个接口有时给 null、有时给对象，
// 会让弱类型客户端在成功路径上崩——而那条路径通常没有错误处理。
func TestNilDataBecomesAnEmptyArray(t *testing.T) {
	t.Parallel()

	c, rec := newCtx()
	Ok(c, nil)

	body := decode(t, rec)
	if _, isArray := body["data"].([]any); !isArray {
		t.Fatalf("data = %#v, want an empty array rather than null", body["data"])
	}
	if body["code"] != float64(0) {
		t.Errorf("code = %v, want 0", body["code"])
	}
}

// TestOkPassesDataThrough 钉住成功响应原样带上数据。
func TestOkPassesDataThrough(t *testing.T) {
	t.Parallel()

	c, rec := newCtx()
	Ok(c, map[string]any{"id": 7})

	body := decode(t, rec)
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("data = %#v, want an object", body["data"])
	}
	if data["id"] != float64(7) {
		t.Errorf("data.id = %v, want 7", data["id"])
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// TestErrUsesTheStatusHTTPCode 钉住 HTTP 状态码取自 goerr 的 Status。
//
// 这一点容易被忽略：goerr 里有一批"业务码 + HTTP 200"型的状态，
// 名字读起来像错误，实际返回 200——按名字选就会让客户端的状态码分支全部失效。
func TestErrUsesTheStatusHTTPCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     *goerr.Status
		wantHTTP   int
		wantNonNil bool
	}{
		{name: "auth is 401", status: goerr.StatusAuth(), wantHTTP: http.StatusUnauthorized},
		{name: "not found is 404", status: goerr.StatusNotFound(), wantHTTP: http.StatusNotFound},
		{name: "permission is 403", status: goerr.StatusPermission(), wantHTTP: http.StatusForbidden},
		{name: "validate params is 422", status: goerr.StatusValidateParams(), wantHTTP: http.StatusUnprocessableEntity},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, rec := newCtx()
			Err(c, goerr.New(nil, tt.status, "boom"))

			if rec.Code != tt.wantHTTP {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantHTTP)
			}
			body := decode(t, rec)
			if body["code"] == float64(0) {
				t.Error("an error response carried business code 0")
			}
		})
	}
}

// TestUnknownErrorFallsBackToInternal 钉住陌生错误不泄漏细节。
//
// 把裸 error 的文本回给客户端会泄漏内部实现（SQL 片段、文件路径、
// 依赖的主机名），而客户端拿它也做不了任何事。
func TestUnknownErrorFallsBackToInternal(t *testing.T) {
	t.Parallel()

	c, rec := newCtx()
	Err(c, errors.New("dial tcp 10.0.0.7:3306: connection refused"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); contains(body, "10.0.0.7") || contains(body, "dial tcp") {
		t.Fatalf("the response leaked internal details: %s", body)
	}
}

// TestMapErrResolvesRegisteredSentinels 钉住 mapper 的匹配与兜底。
func TestMapErrResolvesRegisteredSentinels(t *testing.T) {
	t.Parallel()

	known := errors.New("known business failure")
	mapper := NewErrMapper(ErrMapping{
		Target: known, Status: goerr.StatusNotFound(), Message: "没找到",
	})

	t.Run("registered error maps to its status", func(t *testing.T) {
		t.Parallel()
		c, rec := newCtx()
		MapErr(c, known, mapper)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
		if body := decode(t, rec); body["msg"] != "没找到" {
			t.Errorf("msg = %v, want the mapped message", body["msg"])
		}
	})

	t.Run("wrapped error still matches", func(t *testing.T) {
		t.Parallel()
		c, rec := newCtx()
		MapErr(c, wrap(known), mapper)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("a wrapped sentinel returned %d: mapping must use errors.Is, "+
				"otherwise every fmt.Errorf(\"...: %%w\") breaks it", rec.Code)
		}
	})

	t.Run("unregistered error falls back to internal", func(t *testing.T) {
		t.Parallel()
		c, rec := newCtx()
		MapErr(c, errors.New("something else"), mapper)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
	})
}

// TestMapErrHandsContextErrorsToTheFinalizer 钉住协作式超时/取消不在响应层定局。
//
// MapErr 在这里写 500 的后果：终态中间件看到"响应已写出"只能记日志，
// 504 与"客户端断开不回包"两条语义成为死代码。
func TestMapErrHandsContextErrorsToTheFinalizer(t *testing.T) {
	t.Parallel()

	mapper := NewErrMapper()
	tests := []struct {
		name string
		err  error
	}{
		{name: "deadline exceeded", err: context.DeadlineExceeded},
		{name: "canceled", err: context.Canceled},
		{name: "wrapped deadline", err: fmt.Errorf("load profile: %w", context.DeadlineExceeded)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, rec := newCtx()
			MapErr(c, tt.err, mapper)

			if c.Writer.Written() {
				t.Fatalf("MapErr wrote %d %q; unmapped ctx errors must reach the finalizer middleware",
					rec.Code, rec.Body.String())
			}
			if len(c.Errors) != 1 || c.Errors.Last().Err != tt.err {
				t.Fatalf("errors = %v, want exactly the original error recorded for the finalizer", c.Errors)
			}
			if !c.IsAborted() {
				t.Fatal("the chain was not aborted; later handlers would keep running on a dead request")
			}
		})
	}
}

// TestMapErrPrefersAnExplicitContextMapping 钉住显式登记的 ctx 错误映射仍然优先。
func TestMapErrPrefersAnExplicitContextMapping(t *testing.T) {
	t.Parallel()

	c, rec := newCtx()
	mapper := NewErrMapper(ErrMapping{
		Target: context.DeadlineExceeded, Status: goerr.StatusNotFound(), Message: "查询超时",
	})
	MapErr(c, context.DeadlineExceeded, mapper)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want the explicitly mapped 404", rec.Code)
	}
}

// TestPayloadTooLargeCarriesTheLimit 钉住 413 告诉客户端上限是多少。
//
// 只说"太大"而不说多大，客户端无从判断该切成几块。
func TestPayloadTooLargeCarriesTheLimit(t *testing.T) {
	t.Parallel()

	c, rec := newCtx()
	PayloadTooLargeErr(c, "请求体过大，最大支持 "+FormatBytes(1<<20), 1<<20)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if body := rec.Body.String(); !contains(body, "1.0 MiB") {
		t.Errorf("the 413 response does not state the limit: %s", body)
	}
}

// TestFormatBytes 覆盖各量级、IEC 单位与负值边界。
func TestFormatBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		bytes int64
		want  string
	}{
		{-1, "0 B"},
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1 << 20, "1.0 MiB"},
		{10 << 20, "10 MiB"},
		{1 << 30, "1.0 GiB"},
	}

	for _, tt := range tests {
		if got := FormatBytes(tt.bytes); got != tt.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

// TestErrorResponseHelpers 覆盖其余各状态码 helper。
func TestErrorResponseHelpers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		call     func(*gin.Context)
		wantHTTP int
	}{
		{name: "bad request", wantHTTP: http.StatusBadRequest,
			call: func(c *gin.Context) { BadRequestErr(c, "坏请求") }},
		{name: "unsupported media type", wantHTTP: http.StatusUnsupportedMediaType,
			call: func(c *gin.Context) { UnsupportedMediaTypeErr(c, "只接受 JSON") }},
		{name: "too many requests", wantHTTP: http.StatusTooManyRequests,
			call: func(c *gin.Context) { TooManyRequestsErr(c, "太快了") }},
		{name: "gateway timeout", wantHTTP: http.StatusGatewayTimeout,
			call: func(c *gin.Context) { GatewayTimeoutErr(c, "超时") }},
		{name: "not found", wantHTTP: http.StatusNotFound,
			call: func(c *gin.Context) { NotFoundError(c, "没有") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, rec := newCtx()
			tt.call(c)

			if rec.Code != tt.wantHTTP {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantHTTP)
			}
			if body := decode(t, rec); body["code"] == float64(0) {
				t.Error("an error response carried business code 0")
			}
		})
	}
}

// TestNotAllowedMethodCarriesAllow 钉住 405 与 Allow 头一致。
//
// HTTP 要求 405 必须带 Allow。此前它走的是 400，配上 Allow 头自相矛盾：
// 客户端无从区分"请求格式错误"与"这个路径不支持该方法"。
func TestNotAllowedMethodCarriesAllow(t *testing.T) {
	t.Parallel()

	c, rec := newCtx()
	c.Header("Allow", "GET, HEAD")
	NotAllowedMethod(c, "")

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Errorf("Allow = %q, want it preserved", got)
	}
}

// TestNewResponseBuildsTheEnvelope 覆盖供中间件直接使用的构造函数。
func TestNewResponseBuildsTheEnvelope(t *testing.T) {
	t.Parallel()

	t.Run("without data", func(t *testing.T) {
		t.Parallel()
		r := NewResponse(1001, "boom")
		if r.Code != 1001 || r.Message != "boom" {
			t.Fatalf("envelope = %+v", r)
		}
	})

	t.Run("with data", func(t *testing.T) {
		t.Parallel()
		r := NewResponse(0, "OK", map[string]int{"n": 1})
		if r.Data == nil {
			t.Fatal("data was dropped")
		}
	})
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func wrap(err error) error { return fmtErrorf(err) }

func fmtErrorf(err error) error {
	return errors.Join(errors.New("context: "), err)
}

// TestBindStructErrMapsValidationTo422 覆盖结构体绑定失败的映射。
//
// 422 而不是 400：客户端能区分"我发的 JSON 语法就不对"（400）与
// "语法对但字段不合法"（422），两者的修法完全不同。
func TestBindStructErrMapsValidationTo422(t *testing.T) {
	t.Parallel()

	c, rec := newCtx()
	BindStructErr(c, errors.New("Key: 'Req.Phone' Error:Field validation failed"))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
}

// TestBindErrMapsToValidation 覆盖参数绑定失败的映射。
func TestBindErrMapsToValidation(t *testing.T) {
	t.Parallel()

	c, rec := newCtx()
	BindErr(c, errors.New("invalid query"))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if body := rec.Body.String(); !contains(body, "invalid query") {
		t.Errorf("the response does not carry the validation detail: %s", body)
	}
}

// TestOkErrKeepsHTTP200 钉住"请求成功但业务被拒"的形态。
//
// 有一类业务结果（如"余额不足"）在 HTTP 层面是成功的：请求被正确理解、
// 正确处理，只是业务上不允许。用 4xx 表达会让重试逻辑与监控都把它当成故障。
func TestOkErrKeepsHTTP200(t *testing.T) {
	t.Parallel()

	c, rec := newCtx()
	OkErr(c, goerr.New(nil, goerr.StatusRequestFail(), "余额不足"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a rejected business action is not an HTTP failure", rec.Code)
	}
	body := decode(t, rec)
	if body["code"] == float64(0) {
		t.Error("the business code is 0, so the client cannot tell it was rejected")
	}
	if body["msg"] != "余额不足" {
		t.Errorf("msg = %v, want the business message", body["msg"])
	}
}

// TestErrWithDataCarriesThePayload 覆盖带数据的错误响应。
func TestErrWithDataCarriesThePayload(t *testing.T) {
	t.Parallel()

	c, rec := newCtx()
	ErrWithData(c, goerr.New(nil, goerr.StatusParams(), "字段错误"),
		map[string]any{"field": "phone"})

	body := decode(t, rec)
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("data = %#v, want the payload", body["data"])
	}
	if data["field"] != "phone" {
		t.Errorf("data.field = %v, want phone", data["field"])
	}
}

// TestErrorIsAnAliasOfErr 覆盖供中间件使用的别名。
func TestErrorIsAnAliasOfErr(t *testing.T) {
	t.Parallel()

	c, rec := newCtx()
	Error(c, goerr.New(nil, goerr.StatusNotFound(), "没有"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

type diagnosticCapture struct{ entries []applog.LogEntry }

func (d *diagnosticCapture) Log(_ context.Context, e applog.LogEntry) {
	d.entries = append(d.entries, e)
}

func TestErrorDiagnosticsPreserveTheWholeRootAndClientContract(t *testing.T) {
	sink := &diagnosticCapture{}
	release, err := applog.Install(sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	first, cleanup := errors.New("private primary"), errors.New("private cleanup")
	root := fmt.Errorf("outer operation: %w", errors.Join(goerr.New(first, goerr.StatusNotFound(), "public message"), goerr.WithStack(cleanup)))
	c, rec := newCtx()
	c.Request = c.Request.WithContext(applog.ContextWithRequestID(c.Request.Context(), "generated-id"))
	Err(c, root)
	ObserveError(c, root)
	if rec.Code != 404 || len(sink.entries) != 1 {
		t.Fatalf("status=%d entries=%d", rec.Code, len(sink.entries))
	}
	fields := map[string]any{}
	for _, field := range sink.entries[0].Fields {
		fields[field.Key] = field.Value
	}
	text, ok := fields["error"].(string)
	if !ok || !strings.Contains(text, "outer operation") || !strings.Contains(text, "private cleanup") {
		t.Fatalf("root lost: %v", fields)
	}
	causes, ok := fields["error_causes"].([]map[string]any)
	if !ok || len(causes) < 5 {
		t.Fatalf("cause tree lost: %v", fields)
	}
	frames := 0
	for i, cause := range causes {
		parent, ok := cause["parent"].(int)
		if !ok {
			t.Fatalf("parent has type %T", cause["parent"])
		}
		if parent >= i {
			t.Fatal("invalid parent")
		}
		causeFrames, ok := cause["frames"].([]map[string]any)
		if !ok {
			t.Fatalf("frames have type %T", cause["frames"])
		}
		frames += len(causeFrames)
	}
	if frames == 0 {
		t.Fatal("origin stacks lost")
	}
	body := decode(t, rec)
	if body["msg"] != "public message" || body["request_id"] != "generated-id" || len(body) != 4 || strings.Contains(rec.Body.String(), "private") {
		t.Fatalf("client contract: %s", rec.Body.String())
	}
}

func TestDiagnosticFailureDoesNotChangeTheBusinessResponse(t *testing.T) {
	sink := &diagnosticCapture{}
	release, err := applog.Install(sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	c, rec := newCtx()
	Err(c, goerr.New(errors.New(strings.Repeat("x", 17000)), goerr.StatusNotLoggedIn(), "please authenticate"))
	if rec.Code != 401 || decode(t, rec)["msg"] != "please authenticate" || len(sink.entries) != 1 {
		t.Fatalf("response changed: %s", rec.Body.String())
	}
	found := false
	for _, f := range sink.entries[0].Fields {
		if f.Key == "error_diagnostic_failure" {
			found = true
		}
		if f.Key == "error" {
			t.Fatal("failed inspection was represented as a complete snapshot")
		}
	}
	if !found {
		t.Fatal("diagnostic failure ignored")
	}
	var knownNil *goerr.Item
	c, rec = newCtx()
	Err(c, knownNil)
	if rec.Code != 500 {
		t.Fatalf("typed nil status=%d", rec.Code)
	}
	var unknownNil *diagnosticNilError
	fields := ErrorLogFields(unknownNil)
	if len(fields) != 2 || fields[1].Key != "error_diagnostic_failure" {
		t.Fatalf("typed nil diagnostic=%v", fields)
	}
}

type diagnosticNilError struct{}

func (*diagnosticNilError) Error() string { panic("typed-nil method must not run") }
