package dto

import (
	"strings"
	"testing"
	"unicode"

	"github.com/gtkit/goerr"
	"github.com/gtkit/verify"
)

func containsHan(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return unicode.Is(unicode.Han, r) })
}

// TestValidationShowcaseTranslatesBuiltinTags 证明 ValidationShowcaseRequest
// 上的内置 tag 触发失败时，verify 会渲染成中文、按对外 json 字段名呈现的
// 聚合消息——即脚手架承诺的「校验错误对客消息」契约在真实 tag 组合下成立。
//
// 只断言内置 tag（verify.New() 基线即配好中文）；http_url 的中文消息依赖
// bootstrap 的自定义注册，不在本包测试范围内（见 validation_showcase.go 注释）。
func TestValidationShowcaseTranslatesBuiltinTags(t *testing.T) {
	// verify.New() 幂等：配置 gin 共享 validator 的 binding tag、json 字段名
	// 与内置 tag 的中文翻译。与生产启动时序一致。
	if err := verify.New(); err != nil {
		t.Fatalf("verify.New() = %v, want nil", err)
	}

	bad := ValidationShowcaseRequest{
		Order:    "sideways",     // oneof=asc desc 失败
		Email:    "not-an-email", // email 失败
		Password: "short",        // min=8 失败
		Confirm:  "mismatch",     // eqfield=Password 失败
		Birthday: "1990/01/01",   // datetime=2006-01-02 失败
		Age:      200,            // lte=150 失败
		Tags:     []string{""},   // dive,required 失败（空元素）
	}
	verr := verify.Struct(bad)
	if verr == nil {
		t.Fatal("verify.Struct() = nil, want multiple field failures")
	}

	// AllStructErr 把所有字段错误聚合成一条消息（对比 StructErr 只回第一条）。
	all := verify.AllStructErr(verr)
	if all == nil {
		t.Fatal("verify.AllStructErr() = nil, want aggregated error")
	}
	item, ok := goerr.AsItem(all)
	if !ok {
		t.Fatalf("AllStructErr() = %T, want *goerr.Item", all)
	}
	msg := item.Message()
	if !containsHan(msg) {
		t.Errorf("msg = %q, want Chinese", msg)
	}
	// 每个出错字段的对外 json 名都必须出现在聚合消息里。
	for _, field := range []string{"order", "email", "password", "confirm", "birthday", "age"} {
		if !strings.Contains(msg, field) {
			t.Errorf("msg = %q, want it to name field %q", msg, field)
		}
	}
	// 不得泄漏 Go 结构体名与导出字段名（对外只用 json tag 名，均为小写）。
	for _, leaked := range []string{"ValidationShowcaseRequest", "Key:", "Confirm", "Birthday", "Email", "Order", "Tags", "Age"} {
		if strings.Contains(msg, leaked) {
			t.Errorf("msg = %q leaks internal naming %q", msg, leaked)
		}
	}
	// 已知副作用（见 ValidationShowcaseRequest.Confirm 注释）：eqfield 的 param
	// 是 Go 字段名 "Password"，validator 的中文翻译会把它原样插进消息。这里把它
	// 钉成显式契约——不是测试疏漏，而是提醒派生项目：用 eqfield 就会带出这条
	// 泄漏，要干净就改走 WithStructValidation 或自定义 eqfield 消息。
	if !strings.Contains(msg, "等于Password") {
		t.Errorf("msg = %q, want the documented eqfield param interpolation", msg)
	}

	// StructFieldErrors 给出结构化明细，字段数应与失败字段一致（>= 6）。
	fields := verify.StructFieldErrors(verr)
	if len(fields) < 6 {
		t.Errorf("StructFieldErrors() returned %d fields, want >= 6", len(fields))
	}
	for _, fe := range fields {
		if fe.Field == "" || fe.Message == "" {
			t.Errorf("field error has empty Field/Message: %+v", fe)
		}
	}
}
