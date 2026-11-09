package bootstrap

import (
	"regexp"

	"github.com/go-playground/validator/v10"
	"github.com/gtkit/verify"
)

// 本文件是**编译校验过**的校验注册示例模板，供派生项目复制进各自的
// validationOptions（见 validation.go）。
//
// 这些 Example 函数刻意不写 "// Output:" 注释——go test 只编译、不执行它们。
// 因此示例不会改写全局 validator、不与其他用例争用注册状态；它的价值在于
// 「编译器保证这段注册代码永远和 verify 的真实签名一致」，文档里的片段做不到
// 这一点。要真正启用某条规则：把对应 verify.Option 追加进 validationOptions
// 返回的切片，再在 DTO 的 binding tag 上用上新 tag。
//
// 三条注册路径的分工：
//   - WithTranslation：全新 tag（内置 tag 覆盖不到的格式），需同时给校验函数。
//   - WithValidationTranslation：给**既有** tag 补/改中文消息，不写校验函数。
//   - WithStructValidation：跨字段 / 结构体级规则（tag 表达不了的约束）。

// mobileRe 是示例用的大陆手机号正则。生产项目应把它与号段更新解耦，
// 或改用成熟的号码库；这里只为演示校验函数的写法。
var mobileRe = regexp.MustCompile(`^1[3-9]\d{9}$`)

// Example_customFieldValidation 演示注册一个全新的校验 tag（校验函数 + 中文消息）。
//
// info 里的 {0} 是字段名占位符：verify 内部经 ut.T(tag, field) 替换，
// 渲染成「phone必须是有效的大陆手机号」。**必须带 {0}**，否则消息里没有
// 字段名，客户端不知道是哪个字段错了（docs 里早期示例漏了 {0}，已修正）。
//
// DTO 用法：`Phone string \`json:"phone" binding:"required,mobile_cn"\“
func Example_customFieldValidation() {
	options := []verify.Option{
		verify.WithTranslation("mobile_cn", "{0}必须是有效的大陆手机号", func(fl validator.FieldLevel) bool {
			return mobileRe.MatchString(fl.Field().String())
		}),
	}
	// 派生项目把 options 里的项追加进 validationOptions 的返回切片。
	_ = options
}

// Example_validationTranslation 演示给一个**既有** tag 补中文消息，不写校验函数。
//
// 典型场景：validator 内置了 http_url 校验，但中文翻译包没收录它，缺注册时
// 该字段错误会退回英文（"avatar http_url"）。母版的 validationOptions 已经
// 注册了 http_url，这里再举 datetime 一例。
//
// DTO 用法：`Birthday string \`json:"birthday" binding:"omitempty,datetime=2006-01-02"\“
func Example_validationTranslation() {
	options := []verify.Option{
		verify.WithValidationTranslation("datetime", "{0}的日期格式不正确"),
	}
	_ = options
}

// signupForm 是结构体级校验的示例载体：注册时传它的**零值实例**
// （WithStructValidation 的 types 参数用于把校验函数绑定到具体类型）。
type signupForm struct {
	Password string `json:"password" binding:"required,min=8,max=72"`
	Confirm  string `json:"confirm" binding:"required"`
}

// Example_structLevelValidation 演示跨字段 / 结构体级校验。
//
// 当约束跨越多个字段、单个 tag 表达不了时（如「两次输入的口令必须一致」），
// 用 WithStructValidation 注册一个 StructLevelFunc，在校验函数里通过
// sl.ReportError(field, fieldName, structFieldName, tag, param) 报告错误。
//
// 注意：简单的「等于另一字段」其实用内置 tag `eqfield=Password` 就够了
// （见 example 模块的 ValidationShowcaseRequest）；WithStructValidation 留给
// 真正需要读多个字段、做条件判断的复杂规则。
func Example_structLevelValidation() {
	options := []verify.Option{
		verify.WithStructValidation(func(sl validator.StructLevel) {
			form, ok := sl.Current().Interface().(signupForm)
			if !ok {
				return
			}
			if form.Password != form.Confirm {
				// ReportError(field, fieldName, structFieldName, tag, param)：
				// fieldName 是对外 json 名（错误消息里呈现的就是它），
				// structFieldName 是 Go 字段名（仅内部命名空间用）。
				sl.ReportError(form.Confirm, "confirm", "Confirm", "eqfield", "Password")
			}
		}, signupForm{}),
	}
	_ = options
}
