package dto

// ValidationShowcaseRequest 是**校验参数示范**，不是真实接口契约。
//
// 它把脚手架常用的 validator 内置 tag 集中在一处，供派生项目复制到自己的
// DTO 上；每个字段的注释说明该 tag 的语义与取值理由。全部使用 validator
// **内置** tag（无需自定义注册），verify.New() 已为它们配好中文消息——
// 唯一例外是 http_url，其中文消息由 internal/bootstrap/validation.go 的
// WithValidationTranslation 补齐（见该文件注释）。
//
// 对应的可运行校验见 validation_showcase_test.go：它证明这些 tag 触发失败时
// 会渲染成中文、按对外 json 字段名呈现的消息。
//
// 需要**自定义** tag（如大陆手机号）或**跨字段结构体级**规则时，注册方式见
// internal/bootstrap/validation_example_test.go 的三个 Example 模板。
type ValidationShowcaseRequest struct {
	// 枚举白名单：只接受 asc / desc。omitempty 让「没传」跳过校验，
	// 非法值在绑定层直接 422 拒绝（配合 repository 侧的列名白名单防注入）。
	Order string `json:"order" binding:"omitempty,oneof=asc desc"`

	// 邮箱：内置 email 规则 + 长度上限与列宽对齐。
	Email string `json:"email" binding:"required,email,max=100"`

	// 口令：下限 8 是入口最低要求，上限 72 来自 bcrypt 处理上限。
	Password string `json:"password" binding:"required,min=8,max=72"`

	// 跨字段（内置 tag 版）：Confirm 必须等于 Password。
	// eqfield 的参数是**目标字段的 Go 名**（Password），不是 json 名。
	//
	// ⚠ 已知副作用：validator 的中文翻译会把 eqfield 的 param 原样插进消息，
	// 渲染成「confirm必须等于Password」——这里的 "Password" 是 Go 导出字段名，
	// 属于对客消息不该泄漏的内部命名（本脚手架其余字段都只暴露 json 名）。
	// 若在意这条泄漏，两个干净的选择：
	//   1. 改用 WithStructValidation + sl.ReportError(..., "confirm", ...)，
	//      由你指定对外 json 名（见 bootstrap/validation_example_test.go）；
	//   2. 给 eqfield 注册一条不含 param 的自定义中文消息
	//      （verify.WithValidationTranslation("eqfield", "{0}两次输入不一致")）。
	// 简单的「等于另一字段」用 eqfield 最省事；需要读多字段做条件判断的复杂
	// 规则才用 WithStructValidation。
	Confirm string `json:"confirm" binding:"required,eqfield=Password"`

	// 日期：datetime 按 Go 参考布局校验（2006-01-02 是 Go 的布局常量写法，
	// 不是笔误）。omitempty 让可选日期留空时跳过。
	Birthday string `json:"birthday" binding:"omitempty,datetime=2006-01-02"`

	// 切片元素：dive 下沉到每个元素再套规则——min/max 约束切片长度，
	// dive 之后的 required/max=20 约束每个元素。
	Tags []string `json:"tags" binding:"omitempty,min=1,max=10,dive,required,max=20"`

	// 数值闭区间：gte/lte 同时给出上下界。
	Age int `json:"age" binding:"omitempty,gte=0,lte=150"`

	// http_url：内置 tag，但中文翻译包未收录，消息由 validationOptions 的
	// WithValidationTranslation("http_url", "{0}必须是 http 或 https 链接") 补齐。
	// 用 http_url 而非 url：url 不限 scheme，会放行 javascript:/data: 等
	// 存储型 XSS 载体（详见 validation.go 注释）。
	Homepage string `json:"homepage" binding:"omitempty,http_url,max=2048"`
}
